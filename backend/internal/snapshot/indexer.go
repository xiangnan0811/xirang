// Package snapshot 提供快照文件索引构建与搜索功能。
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// indexingJobs 跟踪正在运行的索引任务，防止并发重复构建。
var indexingJobs sync.Map // map[uint]struct{}

const batchSize = 500

// IsIndexing 检查指定 task 是否正在建索引。
func IsIndexing(taskID uint) bool {
	_, ok := indexingJobs.Load(taskID)
	return ok
}

// GetIndexStatus 返回索引进度：已索引快照数、总快照数、是否索引中。
func GetIndexStatus(ctx context.Context, db *gorm.DB, taskID uint) (indexed, total int, indexing bool, err error) {
	indexing = IsIndexing(taskID)

	var task model.Task
	if err := db.First(&task, taskID).Error; err != nil {
		return 0, 0, indexing, err
	}
	if task.ExecutorType != "restic" {
		return 0, 0, indexing, fmt.Errorf("仅 restic 类型任务支持快照索引")
	}

	exec := &executor.ResticExecutor{}
	snapshots, err := exec.ListSnapshots(ctx, task)
	if err != nil {
		return 0, 0, indexing, err
	}
	total = len(snapshots)

	distinctSnaps := db.Model(&model.SnapshotFileIndex{}).
		Where("task_id = ?", taskID).
		Distinct("snapshot_id")
	var indexedCount int64
	if err := distinctSnaps.Count(&indexedCount).Error; err != nil {
		return 0, total, indexing, err
	}
	indexed = int(indexedCount)

	return indexed, total, indexing, nil
}

// ensureIndexed 确保指定 task 的文件索引已构建。若未构建则触发后台构建。
// 返回 true 表示索引已就绪（含已触发构建并完成），false 表示正在构建或触发失败。
func EnsureIndexed(ctx context.Context, db *gorm.DB, taskID uint) (bool, error) {
	var count int64
	if err := db.Model(&model.SnapshotFileIndex{}).
		Where("task_id = ?", taskID).
		Limit(1).
		Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil // 已有索引
	}

	// 未建索引，触发后台构建
	if IsIndexing(taskID) {
		return false, nil // 已在构建中
	}

	var task model.Task
	if err := db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		return false, err
	}
	if task.ExecutorType != "restic" {
		return false, fmt.Errorf("仅 restic 类型任务支持快照索引")
	}

	// 异步构建索引
	go func() {
		buildCtx, cancel := context.WithTimeout(context.Background(), maxIndexDuration())
		defer cancel()
		_ = BuildIndex(buildCtx, db, task)
	}()

	return false, nil
}

// maxIndexDuration 返回索引构建最大时长（默认 30 分钟）。
func maxIndexDuration() time.Duration {
	// 从环境变量读取或使用默认 30 分钟
	seconds := readEnvIntDefault("SNAPSHOT_INDEX_MAX_SECONDS", 1800)
	return time.Duration(seconds) * time.Second
}

// readEnvIntDefault 读取整型环境变量，失败或未设置时返回默认值。
func readEnvIntDefault(key string, defaultVal int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultVal
	}
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		return v
	}
	return defaultVal
}

// BuildIndex 为指定 task 的 restic 仓库构建全量文件索引。
// 通过 SSH 连接到节点，遍历所有快照，递归列出文件并批量写入 DB。
func BuildIndex(ctx context.Context, db *gorm.DB, task model.Task) error {
	if IsIndexing(task.ID) {
		return fmt.Errorf("任务 %d 的索引构建已在运行中", task.ID)
	}
	indexingJobs.Store(task.ID, struct{}{})
	defer indexingJobs.Delete(task.ID)

	if task.ExecutorType != "restic" {
		return fmt.Errorf("仅 restic 类型任务支持快照索引")
	}

	exec := &executor.ResticExecutor{}
	snapshots, err := exec.ListSnapshots(ctx, task)
	if err != nil {
		return fmt.Errorf("获取快照列表失败: %w", err)
	}

	for _, snap := range snapshots {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 跳过已索引的快照
		var existingCount int64
		if err := db.Model(&model.SnapshotFileIndex{}).
			Where("task_id = ? AND snapshot_id = ?", task.ID, snap.ID).
			Limit(1).
			Count(&existingCount).Error; err != nil {
			return fmt.Errorf("检查快照 %s 索引状态失败: %w", snap.ShortID, err)
		}
		if existingCount > 0 {
			continue
		}

		if err := indexSnapshot(ctx, db, task, snap.ID); err != nil {
			return fmt.Errorf("索引快照 %s 失败: %w", snap.ShortID, err)
		}
	}

	return nil
}

// IncrementalIndex 增量索引新快照（仅索引尚未在 DB 中的快照）。
func IncrementalIndex(ctx context.Context, db *gorm.DB, task model.Task) error {
	return BuildIndex(ctx, db, task) // BuildIndex 内部已做去重
}

// indexSnapshot 索引单个快照中的所有文件。
func indexSnapshot(ctx context.Context, db *gorm.DB, task model.Task, snapshotID string) error {
	client, err := executor.DialSSHForNode(ctx, task.Node)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	cfg, err := parseResticIndexConfig(task.ExecutorConfig)
	if err != nil {
		return err
	}

	resticBin := resolveResticBinary()
	envPrefix := buildIndexEnvPrefix(cfg.RepositoryPassword)
	repoArg := executor.ShellEscape(task.RsyncTarget)
	snapArg := executor.ShellEscape(snapshotID)

	// 使用 restic find --json --long 递归列出快照中所有文件。
	// find 命令天然递归整个快照树，无需手动遍历目录。
	cmd := fmt.Sprintf("%s %s find --json --long --path=/ %s -r %s 2>&1",
		envPrefix, resticBin, snapArg, repoArg)

	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return fmt.Errorf("restic find 执行失败: %w, 输出: %s", err, strings.TrimSpace(output))
	}

	entries := parseResticFindOutput(output)
	if len(entries) == 0 {
		return nil
	}

	// 批量写入（INSERT ... ON CONFLICT DO NOTHING）
	records := make([]model.SnapshotFileIndex, 0, len(entries))
	for _, entry := range entries {
		records = append(records, model.SnapshotFileIndex{
			TaskID:     task.ID,
			SnapshotID: snapshotID,
			Path:       entry.Path,
			Size:       entry.Size,
			Mtime:      entry.Mtime,
		})
	}

	return db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(records, batchSize).Error
}

// resticFindEntry 表示 restic find --json --long 输出中单个文件条目。
type resticFindEntry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// parseResticFindOutput 解析 restic find --json --long 的 NDJSON 输出。
// 每行格式：{"matches":[{"path":"...","type":"file","size":...,"mtime":"..."}],"hits":N}
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

// indexConfig 索引器使用的内部 restic 配置。
type indexConfig struct {
	RepositoryPassword string `json:"repository_password,omitempty"`
}

func parseResticIndexConfig(raw string) (indexConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return indexConfig{}, nil
	}
	var c indexConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return indexConfig{}, err
	}
	return c, nil
}

// resolveResticBinary 返回 restic 二进制名称。
func resolveResticBinary() string {
	raw := strings.TrimSpace(os.Getenv("RESTIC_BINARY"))
	if raw != "" {
		return raw
	}
	return "restic"
}

// buildIndexEnvPrefix 构造 restic 命令环境变量前缀。
func buildIndexEnvPrefix(password string) string {
	if password == "" {
		return "RESTIC_PASSWORD=''"
	}
	return "RESTIC_PASSWORD=" + executor.ShellEscape(password)
}

// EscapeLikePattern 转义 LIKE 模式中的通配符 % 和 _。
// 用于参数化查询中的用户输入逃脱。
func EscapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
