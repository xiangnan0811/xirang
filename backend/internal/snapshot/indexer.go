// Package snapshot provides the legacy snapshot-file cache. In exact lineage
// mode it is deliberately only a cache: committed RecoveryPoints remain the
// authority and a per-snapshot completion marker prevents partial rows from
// becoming searchable after a crash.
package snapshot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"

	"gorm.io/gorm"
)

// indexingJobs tracks active builds so a burst of searches cannot enumerate a
// repository repeatedly. A build always reacquires its own lineage session.
var indexingJobs sync.Map // map[uint]struct{}

const (
	batchSize                     = 500
	exactIndexPageSize            = 200
	exactIndexCompleteMarkerPath  = ""
	exactIndexCompleteMarkerMtime = "xirang-index-complete-v1"

	// Compatibility completion markers use a different value from exact
	// markers. This prevents a legacy cache row from ever satisfying managed
	// exact readiness.
	legacyIndexCompleteMarkerPath  = ""
	legacyIndexCompleteMarkerMtime = "xirang-compat-index-complete-v1"

	legacyIndexDefaultMaxOutputBytes int64 = 256 << 20
	legacyIndexDefaultMaxRecordBytes       = 1 << 20
	legacyIndexDefaultMaxEntries           = 1_000_000
	legacyIndexMaxStderrBytes        int64 = 64 << 10
)

var (
	errResticLSMalformed        = errors.New("malformed Restic ls output")
	errResticLSIncomplete       = errors.New("incomplete Restic ls output")
	errResticLSIdentityMismatch = errors.New("restic ls snapshot identity mismatch")
	errResticLSResourceLimit    = errors.New("restic ls output resource limit exceeded")
)

// Indexer owns the snapshot cache boundary. A nil guard/foundation is only
// used by compatibility wrappers retained for callers that predate the shared
// backup-asset runtime; production exact callers always inject both.
type Indexer struct {
	db         *gorm.DB
	guard      publication.LineageGuard
	foundation *backupasset.FoundationService
}

func NewIndexer(db *gorm.DB, guard publication.LineageGuard, foundation *backupasset.FoundationService) *Indexer {
	return &Indexer{db: db, guard: guard, foundation: foundation}
}

// IsIndexing reports whether a background build is currently active for Task.
func IsIndexing(taskID uint) bool {
	_, ok := indexingJobs.Load(taskID)
	return ok
}

// GetIndexStatus preserves the pre-runtime compatibility entry point.
func GetIndexStatus(ctx context.Context, db *gorm.DB, taskID uint) (indexed, total int, indexing bool, err error) {
	return NewIndexer(db, nil, nil).Status(ctx, taskID, nil)
}

// EnsureIndexed preserves the pre-runtime compatibility entry point.
func EnsureIndexed(ctx context.Context, db *gorm.DB, taskID uint) (bool, error) {
	return NewIndexer(db, nil, nil).EnsureIndexed(ctx, taskID, nil)
}

// BuildIndex preserves the pre-runtime compatibility entry point.
func BuildIndex(ctx context.Context, db *gorm.DB, task model.Task) error {
	return NewIndexer(db, nil, nil).Build(ctx, task)
}

// IncrementalIndex preserves the historic name; Build already avoids duplicate
// rows in compatibility mode and fully replaces each exact snapshot cache.
func IncrementalIndex(ctx context.Context, db *gorm.DB, task model.Task) error {
	return BuildIndex(ctx, db, task)
}

// EnsureIndexed determines readiness using the handler's already-admitted
// lineage view. Exact mode requires one completion marker for every committed
// point; raw rows alone are deliberately never sufficient.
func (indexer *Indexer) EnsureIndexed(ctx context.Context, taskID uint, session publication.LineageSession) (bool, error) {
	if indexer == nil || indexer.db == nil || taskID == 0 {
		return false, fmt.Errorf("%w: snapshot indexer dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if session != nil && session.Mode() == publication.LineageExact {
		ready, err := indexer.exactReady(ctx, taskID, session.CommittedPoints())
		if err != nil || ready {
			return ready, err
		}
		indexer.scheduleBuild(taskID)
		return false, nil
	}

	var count int64
	if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).
		Where("task_id = ? AND path = ? AND mtime = ?", taskID, legacyIndexCompleteMarkerPath, legacyIndexCompleteMarkerMtime).
		Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	indexer.scheduleBuild(taskID)
	return false, nil
}

// Status mirrors EnsureIndexed: exact status is marker-based and never opens
// a Provider handle through the current handler session.
func (indexer *Indexer) Status(ctx context.Context, taskID uint, session publication.LineageSession) (indexed, total int, building bool, err error) {
	if indexer == nil || indexer.db == nil || taskID == 0 {
		return 0, 0, false, fmt.Errorf("%w: snapshot indexer dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	building = IsIndexing(taskID)
	if session != nil && session.Mode() == publication.LineageExact {
		points, err := canonicalCommittedPoints(session.CommittedPoints())
		if err != nil {
			return 0, 0, building, err
		}
		if len(points) == 0 {
			return 0, 0, building, nil
		}
		var markerCount int64
		if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).
			Where("task_id = ? AND snapshot_id IN ? AND path = ? AND mtime = ?", taskID, points, exactIndexCompleteMarkerPath, exactIndexCompleteMarkerMtime).
			Count(&markerCount).Error; err != nil {
			return 0, len(points), building, err
		}
		return int(markerCount), len(points), building, nil
	}

	var task model.Task
	if err := indexer.db.WithContext(ctx).First(&task, taskID).Error; err != nil {
		return 0, 0, building, err
	}
	if !strings.EqualFold(strings.TrimSpace(task.ExecutorType), "restic") {
		return 0, 0, building, fmt.Errorf("仅 restic 类型任务支持快照索引")
	}
	snapshots, err := (&executor.ResticExecutor{}).ListSnapshots(ctx, task)
	if err != nil {
		return 0, 0, building, err
	}
	var indexedCount int64
	if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).
		Where("task_id = ? AND path = ? AND mtime = ?", taskID, legacyIndexCompleteMarkerPath, legacyIndexCompleteMarkerMtime).
		Distinct("snapshot_id").Count(&indexedCount).Error; err != nil {
		return 0, 0, building, err
	}
	return int(indexedCount), len(snapshots), building, nil
}

func (indexer *Indexer) exactReady(ctx context.Context, taskID uint, points []publication.CommittedPoint) (bool, error) {
	ids, err := canonicalCommittedPoints(points)
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return true, nil
	}
	var markers int64
	if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).
		Where("task_id = ? AND snapshot_id IN ? AND path = ? AND mtime = ?", taskID, ids, exactIndexCompleteMarkerPath, exactIndexCompleteMarkerMtime).
		Count(&markers).Error; err != nil {
		return false, err
	}
	return markers == int64(len(ids)), nil
}

func canonicalCommittedPoints(points []publication.CommittedPoint) ([]string, error) {
	ids := make([]string, 0, len(points))
	seen := make(map[string]struct{}, len(points))
	for _, point := range points {
		id := strings.ToLower(strings.TrimSpace(point.FullNativeID))
		if !validExactSnapshotID(id) {
			return nil, fmt.Errorf("%w: invalid committed exact snapshot", backupasset.ErrInvalidState)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate committed exact snapshot", backupasset.ErrConflict)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func validExactSnapshotID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// exactIndexStagingSnapshotID is deliberately not a valid Restic native ID.
// Exact search only admits committed lowercase IDs, so an interrupted staging
// build remains invisible until the short replacement transaction succeeds.
func exactIndexStagingSnapshotID(taskID uint, pointID string) string {
	sum := sha256.Sum256([]byte("xirang.snapshot-index-stage.v1\x00" + strconv.FormatUint(uint64(taskID), 10) + "\x00" + pointID))
	return "z" + hex.EncodeToString(sum[:])[1:]
}

func (indexer *Indexer) scheduleBuild(taskID uint) {
	if IsIndexing(taskID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), maxIndexDuration())
		defer cancel()
		var task model.Task
		if err := indexer.db.WithContext(ctx).Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
			return
		}
		_ = indexer.Build(ctx, task)
	}()
}

// Build creates a cache from a fresh operation-specific lineage session. A
// handler session is never copied into a goroutine, so admission is reacquired
// after the HTTP response has closed its own token.
func (indexer *Indexer) Build(ctx context.Context, task model.Task) error {
	if indexer == nil || indexer.db == nil || task.ID == 0 {
		return fmt.Errorf("%w: snapshot indexer dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if !strings.EqualFold(strings.TrimSpace(task.ExecutorType), "restic") {
		return fmt.Errorf("仅 restic 类型任务支持快照索引")
	}
	if IsIndexing(task.ID) {
		return fmt.Errorf("任务 %d 的索引构建已在运行中", task.ID)
	}
	indexingJobs.Store(task.ID, struct{}{})
	defer indexingJobs.Delete(task.ID)
	if ctx == nil {
		ctx = context.Background()
	}

	if indexer.guard == nil {
		return indexer.buildCompatibility(ctx, task)
	}
	session, err := indexer.guard.Begin(ctx, task.ID, publication.OperationLegacyIndex)
	if err != nil || session == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: snapshot lineage session unavailable", backupasset.ErrInvalidState)
	}
	defer func() { _ = session.Close() }()
	switch session.Mode() {
	case publication.LineageCompatibility:
		return indexer.buildCompatibility(ctx, task)
	case publication.LineageExact:
		return indexer.buildExact(ctx, task, session)
	default:
		return fmt.Errorf("%w: unknown snapshot lineage mode", backupasset.ErrInvalidState)
	}
}

func (indexer *Indexer) buildExact(ctx context.Context, task model.Task, session publication.LineageSession) error {
	if indexer.foundation == nil {
		return fmt.Errorf("%w: exact snapshot indexer foundation unavailable", backupasset.ErrInvalidState)
	}
	config, err := indexer.foundation.PublicationConfig()
	if err != nil {
		return err
	}
	if config.ManifestMaxEntries <= 0 {
		return fmt.Errorf("%w: exact snapshot index entry limit unavailable", backupasset.ErrInvalidState)
	}
	points, err := canonicalCommittedPoints(session.CommittedPoints())
	if err != nil {
		return err
	}
	for _, pointID := range points {
		if err := indexer.indexExactPoint(ctx, task.ID, session, pointID, config.ManifestMaxEntries); err != nil {
			return err
		}
	}
	if len(points) == 0 {
		return indexer.db.WithContext(ctx).Where("task_id = ?", task.ID).Delete(&model.SnapshotFileIndex{}).Error
	}
	return indexer.db.WithContext(ctx).Where("task_id = ? AND snapshot_id NOT IN ?", task.ID, points).Delete(&model.SnapshotFileIndex{}).Error
}

func (indexer *Indexer) indexExactPoint(ctx context.Context, taskID uint, session publication.LineageSession, pointID string, maximumEntries int64) error {
	stagingID := exactIndexStagingSnapshotID(taskID, pointID)
	if err := indexer.db.WithContext(ctx).Where("task_id = ? AND snapshot_id = ?", taskID, stagingID).Delete(&model.SnapshotFileIndex{}).Error; err != nil {
		return fmt.Errorf("clear interrupted exact snapshot staging cache: %w", err)
	}

	stack := []provider.EntryLocator{{Native: "/"}}
	visitedDirectories := map[string]struct{}{"/": {}}
	records := make([]model.SnapshotFileIndex, 0, batchSize)
	var indexedEntries int64
	flush := func() error {
		if len(records) == 0 {
			return nil
		}
		if err := indexer.db.WithContext(ctx).CreateInBatches(records, batchSize).Error; err != nil {
			return fmt.Errorf("write exact snapshot cache batch: %w", err)
		}
		records = records[:0]
		return nil
	}

	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		parent := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		cursor := ""
		for {
			page, err := session.ListEntries(ctx, pointID, parent, provider.PageRequest{Limit: exactIndexPageSize, Cursor: cursor})
			if err != nil {
				return fmt.Errorf("list exact snapshot entries: %w", err)
			}
			for _, entry := range page.Items {
				path := strings.TrimSpace(entry.Locator.Native)
				if path == "" || !strings.HasPrefix(path, "/") || entry.Size < 0 {
					return fmt.Errorf("%w: invalid exact snapshot entry", backupasset.ErrInvalidState)
				}
				if entry.Type != backupasset.CatalogEntryFile && entry.Type != backupasset.CatalogEntryDirectory {
					return fmt.Errorf("%w: unsupported exact snapshot entry", backupasset.ErrInvalidState)
				}
				indexedEntries++
				if indexedEntries > maximumEntries {
					return fmt.Errorf("%w: exact snapshot index entry limit exceeded", backupasset.ErrInvalidState)
				}
				mtime := ""
				if !entry.ModTime.IsZero() {
					mtime = entry.ModTime.UTC().Format(time.RFC3339Nano)
				}
				records = append(records, model.SnapshotFileIndex{TaskID: taskID, SnapshotID: stagingID, Path: path, Size: entry.Size, Mtime: mtime})
				if entry.Type == backupasset.CatalogEntryDirectory {
					if _, seen := visitedDirectories[path]; !seen {
						visitedDirectories[path] = struct{}{}
						stack = append(stack, provider.EntryLocator{Native: path})
					}
				}
				if len(records) >= batchSize {
					if err := flush(); err != nil {
						return err
					}
				}
			}
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor {
				return fmt.Errorf("%w: exact snapshot entry cursor did not advance", backupasset.ErrInvalidState)
			}
			cursor = page.NextCursor
		}
	}
	if err := flush(); err != nil {
		return err
	}
	marker := model.SnapshotFileIndex{
		TaskID: taskID, SnapshotID: stagingID, Path: exactIndexCompleteMarkerPath,
		Size: indexedEntries, Mtime: exactIndexCompleteMarkerMtime,
	}
	if err := indexer.db.WithContext(ctx).Create(&marker).Error; err != nil {
		return fmt.Errorf("write exact snapshot completion marker: %w", err)
	}
	return indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ? AND snapshot_id = ?", taskID, pointID).Delete(&model.SnapshotFileIndex{}).Error; err != nil {
			return fmt.Errorf("clear prior complete exact snapshot cache: %w", err)
		}
		result := tx.Model(&model.SnapshotFileIndex{}).
			Where("task_id = ? AND snapshot_id = ?", taskID, stagingID).
			Update("snapshot_id", pointID)
		if result.Error != nil {
			return fmt.Errorf("activate complete exact snapshot cache: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: exact snapshot staging marker is missing", backupasset.ErrConflict)
		}
		return nil
	})
}

func (indexer *Indexer) buildCompatibility(ctx context.Context, task model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := indexer.compatibilityResticLSLimits()
	if err != nil {
		return err
	}
	exec := &executor.ResticExecutor{}
	snapshots, err := exec.ListSnapshots(ctx, task)
	if err != nil {
		return fmt.Errorf("获取快照列表失败: %w", err)
	}
	for _, snapshot := range snapshots {
		if err := ctx.Err(); err != nil {
			return err
		}
		var existingCount int64
		if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).
			Where("task_id = ? AND snapshot_id = ? AND path = ? AND mtime = ?", task.ID, snapshot.ID, legacyIndexCompleteMarkerPath, legacyIndexCompleteMarkerMtime).
			Limit(1).Count(&existingCount).Error; err != nil {
			return fmt.Errorf("检查快照 %s 索引状态失败: %w", snapshot.ShortID, err)
		}
		if existingCount > 0 {
			continue
		}
		if err := legacyIndexSnapshotWithLimits(ctx, indexer.db, task, snapshot.ID, limits); err != nil {
			return fmt.Errorf("索引快照 %s 失败: %w", snapshot.ShortID, err)
		}
	}
	return nil
}

type resticLSLimits struct {
	timeout        time.Duration
	maxOutputBytes int64
	maxRecordBytes int
	maxEntries     int
	maxStderrBytes int64
}

func defaultResticLSLimits() resticLSLimits {
	return resticLSLimits{
		timeout:        maxIndexDuration(),
		maxOutputBytes: legacyIndexDefaultMaxOutputBytes,
		maxRecordBytes: legacyIndexDefaultMaxRecordBytes,
		maxEntries:     legacyIndexDefaultMaxEntries,
		maxStderrBytes: legacyIndexMaxStderrBytes,
	}
}

func (indexer *Indexer) compatibilityResticLSLimits() (resticLSLimits, error) {
	limits := defaultResticLSLimits()
	if indexer == nil || indexer.foundation == nil {
		return limits, nil
	}
	config, err := indexer.foundation.PublicationConfig()
	if err != nil {
		return resticLSLimits{}, fmt.Errorf("读取 Restic 索引限制失败: %w", err)
	}
	if config.BackupStreamMaxBytes > 0 && config.BackupStreamMaxBytes < limits.maxOutputBytes {
		limits.maxOutputBytes = config.BackupStreamMaxBytes
	}
	if config.ManifestMaxRecordBytes > 0 && config.ManifestMaxRecordBytes < limits.maxRecordBytes {
		limits.maxRecordBytes = config.ManifestMaxRecordBytes
	}
	if config.ManifestMaxEntries > 0 && config.ManifestMaxEntries < int64(limits.maxEntries) {
		limits.maxEntries = int(config.ManifestMaxEntries)
	}
	return limits, nil
}

// maxIndexDuration returns the bounded compatibility-cache build duration.
func maxIndexDuration() time.Duration {
	seconds := readEnvIntDefault("SNAPSHOT_INDEX_MAX_SECONDS", 1800)
	return time.Duration(seconds) * time.Second
}

func readEnvIntDefault(key string, defaultVal int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultVal
	}
	if value, err := strconv.Atoi(raw); err == nil && value > 0 {
		return value
	}
	return defaultVal
}

func legacyIndexSnapshotWithLimits(ctx context.Context, db *gorm.DB, task model.Task, snapshotID string, limits resticLSLimits) error {
	if db == nil {
		return fmt.Errorf("%w: snapshot index database unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !validExactSnapshotID(snapshotID) {
		return fmt.Errorf("%w: invalid Restic snapshot ID", errResticLSIdentityMismatch)
	}
	if strings.TrimSpace(task.RsyncTarget) == "" {
		return fmt.Errorf("%w: Restic repository path is empty", backupasset.ErrInvalidState)
	}
	if err := limits.validate(); err != nil {
		return err
	}

	client, err := executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeSnapshot)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	access, err := executor.ResolveResticRepositoryAccess(task.ExecutorConfig)
	if err != nil {
		return err
	}
	pwFilePath := executor.BuildResticPasswordFilePath()
	createPwCmd := executor.BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if _, err := executor.RunSSHCommandOutput(ctx, client, createPwCmd); err != nil {
		return fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupCmd := executor.BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = executor.RunSSHCommandOutput(cleanupCtx, client, cleanupCmd)
	}()

	cmd := buildLegacyResticLSCommand(
		executor.ShellEscape(resolveResticBinary()), pwFilePath, snapshotID, strings.TrimSpace(task.RsyncTarget),
	)
	runner := sshutil.NewSSHCommandRunnerWithTransportClose(client, 1)
	stream, err := runner.OpenRawExecution(ctx, sshutil.RawCommandSpec{
		Command:        cmd,
		Timeout:        limits.timeout,
		MaxStdoutBytes: limits.maxOutputBytes,
		MaxStderrBytes: limits.maxStderrBytes,
		MaxRecordBytes: limits.maxRecordBytes,
	})
	if err != nil {
		return fmt.Errorf("打开 restic ls 执行流失败: %w", err)
	}

	entries, err := collectResticLSExecution(stream, snapshotID, limits)
	if err != nil {
		return err
	}
	return publishResticLSIndex(ctx, db, task.ID, snapshotID, entries)
}
func collectResticLSExecution(execution sshutil.CommandExecutionStream, snapshotID string, limits resticLSLimits) ([]resticLSEntry, error) {
	if execution == nil {
		return nil, fmt.Errorf("%w: Restic ls execution unavailable", backupasset.ErrInvalidState)
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	entries, parseErr := parseResticLSReader(execution, snapshotID, limits.maxEntries, limits.maxRecordBytes)
	if parseErr != nil {
		_ = execution.Cancel()
		return nil, fmt.Errorf("解析 restic ls 输出失败: %w", parseErr)
	}
	completion, joinErr := execution.Join()
	if joinErr != nil {
		return nil, newResticLSFailureError(joinErr, completion)
	}
	if !completion.ExitCodeKnown || completion.ExitCode != 0 {
		return nil, newResticLSFailureError(nil, completion)
	}
	return entries, nil
}

func (limits resticLSLimits) validate() error {
	if limits.timeout <= 0 || limits.maxOutputBytes <= 0 || limits.maxRecordBytes <= 0 ||
		limits.maxEntries <= 0 || limits.maxStderrBytes <= 0 {
		return fmt.Errorf("%w: invalid Restic ls limits", backupasset.ErrInvalidState)
	}
	return nil
}

type resticLSEntry struct {
	Path  string
	Size  int64
	Mtime string
}

func buildLegacyResticLSCommand(resticBin, passwordFilePath, snapshotID, repository string) string {
	return fmt.Sprintf("%s ls --json --long -r %s -- %s",
		executor.BuildResticCommandPrefix(resticBin, passwordFilePath),
		executor.ShellEscape(repository), executor.ShellEscape(snapshotID))
}

func newResticLSFailureError(err error, completion sshutil.CommandCompletion) error {
	if err != nil {
		return fmt.Errorf("restic ls 执行失败: %w", err)
	}
	if !completion.ExitCodeKnown {
		return fmt.Errorf("restic ls 执行结果未知")
	}
	return fmt.Errorf("restic ls 执行失败: exit code %d", completion.ExitCode)
}

func parseResticLSOutput(output, snapshotID string) ([]resticLSEntry, error) {
	return parseResticLSReader(bytes.NewReader([]byte(output)), snapshotID,
		legacyIndexDefaultMaxEntries, legacyIndexDefaultMaxRecordBytes)
}

func parseResticLSReader(reader io.Reader, snapshotID string, maxEntries, maxRecordBytes int) ([]resticLSEntry, error) {
	if reader == nil {
		return nil, errResticLSMalformed
	}
	if !validExactSnapshotID(snapshotID) {
		return nil, fmt.Errorf("%w: invalid expected snapshot ID", errResticLSIdentityMismatch)
	}
	if maxEntries <= 0 || maxRecordBytes <= 0 {
		return nil, fmt.Errorf("%w: invalid parser limits", errResticLSResourceLimit)
	}

	buffered := bufio.NewReaderSize(reader, maxRecordBytes+1)
	entries := make([]resticLSEntry, 0)
	seenPaths := make(map[string]struct{})
	headerSeen := false
	for {
		line, err := buffered.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			return nil, fmt.Errorf("%w: Restic ls record exceeds limit", errResticLSResourceLimit)
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				return nil, errResticLSIncomplete
			}
			if !headerSeen {
				return nil, fmt.Errorf("%w: snapshot header missing", errResticLSMalformed)
			}
			return entries, nil
		case errors.Is(err, sshutil.ErrCommandOutputLimit):
			return nil, fmt.Errorf("%w: Restic ls output exceeds limit", errResticLSResourceLimit)
		case err != nil:
			return nil, fmt.Errorf("read Restic ls output: %w", err)
		}
		if len(line) == 0 || len(line)-1 > maxRecordBytes {
			return nil, fmt.Errorf("%w: Restic ls record exceeds limit", errResticLSResourceLimit)
		}
		line = bytes.TrimSpace(line[:len(line)-1])
		if len(line) == 0 {
			return nil, fmt.Errorf("%w: empty Restic ls record", errResticLSMalformed)
		}

		var object map[string]json.RawMessage
		if err := json.Unmarshal(line, &object); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON record", errResticLSMalformed)
		}
		kind, err := resticLSRecordKind(object)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "snapshot":
			if headerSeen {
				return nil, fmt.Errorf("%w: duplicate snapshot header", errResticLSMalformed)
			}
			headerID, err := resticLSRequiredString(object, "id")
			if err != nil {
				return nil, err
			}
			if headerID != snapshotID {
				return nil, fmt.Errorf("%w: got %q", errResticLSIdentityMismatch, headerID)
			}
			if !validExactSnapshotID(headerID) {
				return nil, fmt.Errorf("%w: invalid snapshot header ID", errResticLSIdentityMismatch)
			}
			headerSeen = true
		case "node":
			if !headerSeen {
				return nil, fmt.Errorf("%w: node appeared before snapshot header", errResticLSMalformed)
			}
			if len(entries) >= maxEntries {
				return nil, fmt.Errorf("%w: too many Restic ls entries", errResticLSResourceLimit)
			}
			entry, err := parseResticLSNode(object)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seenPaths[entry.Path]; duplicate {
				return nil, fmt.Errorf("%w: duplicate path %q", errResticLSMalformed, entry.Path)
			}
			seenPaths[entry.Path] = struct{}{}
			entries = append(entries, entry)
		default:
			return nil, fmt.Errorf("%w: unsupported record type %q", errResticLSMalformed, kind)
		}
	}
}

func resticLSRecordKind(object map[string]json.RawMessage) (string, error) {
	var values []string
	for _, key := range []string{"message_type", "struct_type"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		var value string
		if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &value) != nil || value == "" {
			return "", fmt.Errorf("%w: invalid %s", errResticLSMalformed, key)
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return "", fmt.Errorf("%w: record type missing", errResticLSMalformed)
	}
	if len(values) == 2 && values[0] != values[1] {
		return "", fmt.Errorf("%w: record type fields disagree", errResticLSMalformed)
	}
	switch values[0] {
	case "snapshot", "node":
		return values[0], nil
	default:
		return "", fmt.Errorf("%w: unsupported record type %q", errResticLSMalformed, values[0])
	}
}

func resticLSRequiredString(object map[string]json.RawMessage, key string) (string, error) {
	raw, ok := object[key]
	if !ok {
		return "", fmt.Errorf("%w: %s missing", errResticLSMalformed, key)
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return "", fmt.Errorf("%w: invalid %s", errResticLSMalformed, key)
	}
	return value, nil
}

func parseResticLSNode(object map[string]json.RawMessage) (resticLSEntry, error) {
	name, err := resticLSRequiredString(object, "name")
	if err != nil {
		return resticLSEntry{}, err
	}
	nodeType, err := resticLSRequiredString(object, "type")
	if err != nil {
		return resticLSEntry{}, err
	}
	switch nodeType {
	case "file", "dir", "symlink", "dev", "chardev", "fifo", "socket", "irregular":
	default:
		return resticLSEntry{}, fmt.Errorf("%w: unsupported node type %q", errResticLSMalformed, nodeType)
	}
	nodePath, err := resticLSRequiredString(object, "path")
	if err != nil {
		return resticLSEntry{}, err
	}
	if strings.ContainsRune(nodePath, '\x00') || !strings.HasPrefix(nodePath, "/") ||
		path.Clean(nodePath) != nodePath || nodePath == "/" || path.Base(nodePath) != name {
		return resticLSEntry{}, fmt.Errorf("%w: invalid node path %q", errResticLSMalformed, nodePath)
	}

	var size int64
	rawSize, hasSize := object["size"]
	if nodeType == "file" && !hasSize {
		return resticLSEntry{}, fmt.Errorf("%w: size missing for %q", errResticLSMalformed, nodePath)
	}
	if hasSize {
		if bytes.Equal(bytes.TrimSpace(rawSize), []byte("null")) || json.Unmarshal(rawSize, &size) != nil || size < 0 {
			return resticLSEntry{}, fmt.Errorf("%w: invalid size for %q", errResticLSMalformed, nodePath)
		}
	}

	mtime := ""
	if raw, ok := object["mtime"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return resticLSEntry{}, fmt.Errorf("%w: invalid mtime for %q", errResticLSMalformed, nodePath)
		}
		var timestamp time.Time
		if json.Unmarshal(raw, &timestamp) != nil || timestamp.IsZero() {
			return resticLSEntry{}, fmt.Errorf("%w: invalid mtime for %q", errResticLSMalformed, nodePath)
		}
		mtime = timestamp.UTC().Format(time.RFC3339Nano)
	}
	return resticLSEntry{Path: nodePath, Size: size, Mtime: mtime}, nil
}

func publishResticLSIndex(ctx context.Context, db *gorm.DB, taskID uint, snapshotID string, entries []resticLSEntry) error {
	if db == nil || taskID == 0 || !validExactSnapshotID(snapshotID) {
		return fmt.Errorf("%w: invalid Restic ls index publication", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records := make([]model.SnapshotFileIndex, 0, len(entries)+1)
	for _, entry := range entries {
		records = append(records, model.SnapshotFileIndex{
			TaskID: taskID, SnapshotID: snapshotID, Path: entry.Path, Size: entry.Size, Mtime: entry.Mtime,
		})
	}
	records = append(records, model.SnapshotFileIndex{
		TaskID: taskID, SnapshotID: snapshotID, Path: legacyIndexCompleteMarkerPath,
		Size: int64(len(entries)), Mtime: legacyIndexCompleteMarkerMtime,
	})
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ? AND snapshot_id = ?", taskID, snapshotID).
			Delete(&model.SnapshotFileIndex{}).Error; err != nil {
			return fmt.Errorf("clear prior compatibility snapshot index: %w", err)
		}
		if err := tx.CreateInBatches(records, batchSize).Error; err != nil {
			return fmt.Errorf("publish compatibility snapshot index: %w", err)
		}
		return nil
	})
}

func resolveResticBinary() string {
	if value := strings.TrimSpace(os.Getenv("RESTIC_BINARY")); value != "" {
		return value
	}
	return "restic"
}

// EscapeLikePattern escapes user text before it is passed to a parameterized
// SQL LIKE expression.
func EscapeLikePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
