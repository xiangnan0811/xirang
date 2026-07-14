// Package snapshot provides the legacy snapshot-file cache. In exact lineage
// mode it is deliberately only a cache: committed RecoveryPoints remain the
// authority and a per-snapshot completion marker prevents partial rows from
// becoming searchable after a crash.
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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
	"gorm.io/gorm/clause"
)

// indexingJobs tracks active builds so a burst of searches cannot enumerate a
// repository repeatedly. A build always reacquires its own lineage session.
var indexingJobs sync.Map // map[uint]struct{}

const (
	batchSize                     = 500
	exactIndexPageSize            = 200
	exactIndexCompleteMarkerPath  = ""
	exactIndexCompleteMarkerMtime = "xirang-index-complete-v1"
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
	if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).Where("task_id = ?", taskID).Limit(1).Count(&count).Error; err != nil {
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
	if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).Where("task_id = ?", taskID).Distinct("snapshot_id").Count(&indexedCount).Error; err != nil {
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
		if err := indexer.db.WithContext(ctx).Model(&model.SnapshotFileIndex{}).Where("task_id = ? AND snapshot_id = ?", task.ID, snapshot.ID).Limit(1).Count(&existingCount).Error; err != nil {
			return fmt.Errorf("检查快照 %s 索引状态失败: %w", snapshot.ShortID, err)
		}
		if existingCount > 0 {
			continue
		}
		if err := legacyIndexSnapshot(ctx, indexer.db, task, snapshot.ID); err != nil {
			return fmt.Errorf("索引快照 %s 失败: %w", snapshot.ShortID, err)
		}
	}
	return nil
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

// legacyIndexSnapshot retains the old pristine command path. It is never
// called from exact mode, where ListEntries is the only Provider operation.
func legacyIndexSnapshot(ctx context.Context, db *gorm.DB, task model.Task, snapshotID string) error {
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
		cleanupCmd := executor.BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = executor.RunSSHCommandOutput(ctx, client, cleanupCmd)
	}()
	pwFileArg := executor.BuildResticPasswordFileArg(pwFilePath)
	cmd := fmt.Sprintf("%s %s find --json --long --path=/ %s -r %s 2>&1",
		pwFileArg, resolveResticBinary(), executor.ShellEscape(snapshotID), executor.ShellEscape(task.RsyncTarget))
	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return newResticFindFailureError(err, output)
	}
	entries := parseResticFindOutput(output)
	if len(entries) == 0 {
		return nil
	}
	records := make([]model.SnapshotFileIndex, 0, len(entries))
	for _, entry := range entries {
		records = append(records, model.SnapshotFileIndex{TaskID: task.ID, SnapshotID: snapshotID, Path: entry.Path, Size: entry.Size, Mtime: entry.Mtime})
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(records, batchSize).Error
}

type resticFindEntry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

func newResticFindFailureError(err error, output string) error {
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("restic find 执行失败: %w", err)
	}
	return fmt.Errorf("restic find 执行失败: %w, 输出: [输出已隐藏]", err)
}

func parseResticFindOutput(output string) []resticFindEntry {
	var entries []resticFindEntry
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var wrapper struct {
			Matches []resticFindEntry `json:"matches"`
		}
		if err := json.Unmarshal([]byte(line), &wrapper); err != nil {
			continue
		}
		for _, entry := range wrapper.Matches {
			if entry.Path != "" {
				entries = append(entries, entry)
			}
		}
	}
	return entries
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
