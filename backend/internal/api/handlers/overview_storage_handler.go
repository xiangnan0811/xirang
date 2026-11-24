package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"

	"github.com/gin-gonic/gin"
	"golang.org/x/sys/unix"
	"gorm.io/gorm"
)

type mountVolumeStatFunc func(path string) (volumeKey string, stat unix.Statfs_t, err error)

// StorageUsageHandler 提供存储用量概览。
type StorageUsageHandler struct {
	db             *gorm.DB
	scanTimeout    time.Duration
	newScanContext func(context.Context) (context.Context, context.CancelFunc)
	statMount      mountVolumeStatFunc
	scanDir        func(context.Context, string, bool) (float64, bool)
}

func NewStorageUsageHandler(db *gorm.DB) *StorageUsageHandler {
	return &StorageUsageHandler{
		db:          db,
		scanTimeout: 10 * time.Second,
	}
}

// storageUsageResponse describes the storage overview returned to the caller.
type storageUsageResponse struct {
	// MountPoints contains whole-volume statistics; operators receive an empty array.
	MountPoints []mountPointInfo `json:"mount_points"`
	PerNode     []perNodeUsage   `json:"per_node"`
	// Incomplete is present and true if any mount or node scan is incomplete.
	Incomplete bool `json:"incomplete,omitempty"`
	// MountPointsIncomplete is present and true if a mount scan is incomplete.
	MountPointsIncomplete bool `json:"mount_points_incomplete,omitempty"`
}

type mountPointInfo struct {
	Path    string  `json:"path"`
	UsedGB  float64 `json:"used_gb"`  // Total volume bytes minus bytes available to unprivileged users, divided by 2^30 and truncated to two decimals.
	TotalGB float64 `json:"total_gb"` // Whole-volume total bytes divided by 2^30, truncated to two decimals.
	Pct     float64 `json:"pct"`      // Whole-volume usage percentage, truncated to two decimals.
}

type perNodeUsage struct {
	NodeID     uint    `json:"node_id"`
	NodeName   string  `json:"node_name"`
	Path       string  `json:"path"`
	UsedGB     float64 `json:"used_gb"`              // Sum of logical sizes of non-directory entries, excluding symlinks, divided by 2^30 and truncated to two decimals; a lower bound when incomplete.
	Incomplete bool    `json:"incomplete,omitempty"` // Present and true when this node's scan is incomplete.
}

// Get godoc
// @Summary      获取存储用量
// @Description  收集本地备份目标路径的挂载点用量和按节点分布统计
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=storageUsageResponse}
// @Failure      401  {object}  handlers.Response
// @Router       /overview/storage-usage [get]
func (h *StorageUsageHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")

	reqCtx := c.Request.Context()
	ownedIDs, needFilter, ownErr := ownershipNodeFilter(c, h.db)
	if ownErr != nil {
		respondInternalError(c, ownErr)
		return
	}
	if needFilter && len(ownedIDs) == 0 {
		respondOK(c, storageUsageResponse{MountPoints: []mountPointInfo{}, PerNode: []perNodeUsage{}})
		return
	}

	// 1. 先完成全部轻量数据库元数据读取，避免大目录递归扫描耗尽超时预算后拖垮后续 DB 查询。
	var policies []model.Policy
	policyQ := h.db.WithContext(reqCtx).Select("id, name, target_path").Where("enabled = ? AND is_template = ?", true, false)
	if needFilter {
		policyQ = policyQ.Where("id IN (SELECT policy_id FROM policy_nodes WHERE node_id IN ?)", ownedIDs)
	}
	if err := policyQ.Find(&policies).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	// Read only display and target metadata before deciding which tasks are local.
	// In particular, remote Rsync targets must never trigger protected-field hooks.
	type storageTask struct {
		ID           uint
		NodeID       uint
		NodeName     string
		ExecutorType string
		RsyncTarget  string
	}
	var taskRows []storageTask
	taskQ := h.db.WithContext(reqCtx).Table("tasks t").
		Select("t.id, t.node_id, COALESCE(n.name, '') AS node_name, t.executor_type, t.rsync_target").
		Joins("LEFT JOIN nodes n ON n.id = t.node_id").
		Where("t.rsync_target <> '' AND LOWER(TRIM(t.executor_type)) = ?", "rsync")
	if needFilter {
		taskQ = taskQ.Where("t.node_id IN ?", ownedIDs)
	}
	if err := taskQ.Scan(&taskRows).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	type expectedTask struct {
		id          uint
		nodeID      uint
		fingerprint string
	}
	localTasks := make([]storageTask, 0, len(taskRows))
	for _, task := range taskRows {
		if policy.IsCoreLocalTarget(task.ExecutorType, task.RsyncTarget) && task.NodeID != 0 {
			localTasks = append(localTasks, task)
		}
	}

	establishedTasks := make(map[uint]bool)
	if len(localTasks) > 0 {
		hasRunsTable, err := hasTaskRunTable(reqCtx, h.db)
		if err != nil {
			respondInternalError(c, err)
			return
		}
		// Bound task and association preloads as well as the evidence lookup.
		const batchSize = 100
		for start := 0; start < len(localTasks); start += batchSize {
			end := start + batchSize
			if end > len(localTasks) {
				end = len(localTasks)
			}
			conditions := make([]string, 0, end-start)
			filterArgs := make([]interface{}, 0, (end-start)*4)
			for _, task := range localTasks[start:end] {
				// Exclude retargeted or reassigned tasks before any protected-field hooks run.
				conditions = append(conditions, "(id = ? AND node_id = ? AND rsync_target = ? AND executor_type = ?)")
				filterArgs = append(filterArgs, task.ID, task.NodeID, task.RsyncTarget, task.ExecutorType)
			}
			var fingerprintTasks []model.Task
			if err := h.db.WithContext(reqCtx).
				Select("id, node_id, policy_id, executor_type, rsync_source, rsync_target, executor_config").
				Preload("Node", func(db *gorm.DB) *gorm.DB {
					return db.Select("id, name, host, port, username, auth_type, ssh_key_id, use_sudo, backup_dir")
				}).
				Preload("Policy", func(db *gorm.DB) *gorm.DB {
					return db.Select("id, exclude_rules, bwlimit, bandwidth_schedule, pre_hook, post_hook, hook_timeout_seconds, max_execution_seconds, app_profile, app_credential_id")
				}).
				Where(strings.Join(conditions, " OR "), filterArgs...).Find(&fingerprintTasks).Error; err != nil {
				respondInternalError(c, err)
				return
			}
			if !hasRunsTable {
				continue
			}
			expectedTasks := make([]expectedTask, 0, len(fingerprintTasks))
			for _, task := range fingerprintTasks {
				if fingerprint := model.TaskRunBackupConfigFingerprint(task); fingerprint != "" {
					expectedTasks = append(expectedTasks, expectedTask{task.ID, task.NodeID, fingerprint})
				}
			}
			if len(expectedTasks) == 0 {
				continue
			}
			values := make([]string, 0, len(expectedTasks))
			args := make([]interface{}, 0, len(expectedTasks)*3+4)
			for _, task := range expectedTasks {
				values = append(values, "(CAST(? AS BIGINT), CAST(? AS BIGINT), CAST(? AS TEXT))")
				args = append(args, task.id, task.nodeID, task.fingerprint)
			}
			args = append(args, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified,
				model.TaskRunStatusWarning, model.TaskRunGenerationStateDirty)
			query := `WITH expected(task_id, node_id, fingerprint) AS (VALUES ` + strings.Join(values, ", ") + `)
				SELECT expected.task_id FROM expected WHERE EXISTS (
					SELECT 1 FROM task_runs tr
					WHERE tr.task_id = expected.task_id AND tr.node_id_snapshot = expected.node_id
						AND tr.backup_config_fingerprint = expected.fingerprint
						AND tr.started_at IS NOT NULL AND tr.finished_at IS NOT NULL
						AND LOWER(TRIM(tr.trigger_type)) NOT IN ('restore', 'drill', 'migration')
						AND (TRIM(COALESCE(tr.executor_type_snapshot, '')) = '' OR LOWER(TRIM(tr.executor_type_snapshot)) = 'rsync')
						AND ((tr.status = ? AND tr.backup_generation_state = ?)
							OR (tr.status = ? AND tr.backup_generation_state = ?))
				)`
			var taskIDs []uint
			if err := h.db.WithContext(reqCtx).Raw(query, args...).Scan(&taskIDs).Error; err != nil {
				respondInternalError(c, err)
				return
			}
			for _, id := range taskIDs {
				establishedTasks[id] = true
			}
		}
	}

	var policyNodes []struct {
		PolicyID uint
		Target   string
		NodeID   uint
		NodeName string
	}
	policyNodeQ := h.db.WithContext(reqCtx).Table("policies p").
		Select("p.id AS policy_id, p.target_path AS target, n.id AS node_id, n.name AS node_name").
		Joins("JOIN policy_nodes pn ON pn.policy_id = p.id JOIN nodes n ON n.id = pn.node_id").
		Where("p.enabled = ? AND p.is_template = ?", true, false)
	if needFilter {
		policyNodeQ = policyNodeQ.Where("n.id IN ?", ownedIDs)
	}
	if err := policyNodeQ.Scan(&policyNodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	// 2. 独立的文件系统扫描预算，仅用于 Statfs 与目录递归统计；超预算时标记 incomplete 而不丢弃整次响应。
	var scanCtx context.Context
	var cancel context.CancelFunc
	if h.newScanContext != nil {
		scanCtx, cancel = h.newScanContext(reqCtx)
	} else {
		scanTimeout := h.scanTimeout
		if scanTimeout <= 0 {
			scanTimeout = 10 * time.Second
		}
		scanCtx, cancel = context.WithTimeout(reqCtx, scanTimeout)
	}
	defer cancel()

	// 去重挂载点候选：优先保留策略根目录，再补充已有任务落盘的绝对路径（禁用策略不隐藏已有备份）。
	mountPathSet := make(map[string]bool)
	mountExpectedToExist := make(map[string]bool)
	targetPaths := make([]string, 0)
	addMountCandidate := func(raw string, expectedToExist bool) {
		tp := strings.TrimSpace(raw)
		if tp == "" || strings.Contains(tp, ":") {
			return
		}
		if expectedToExist {
			mountExpectedToExist[tp] = true
		}
		if !mountPathSet[tp] {
			mountPathSet[tp] = true
			targetPaths = append(targetPaths, tp)
		}
	}
	for _, p := range policies {
		addMountCandidate(p.TargetPath, false)
	}
	for _, t := range taskRows {
		if policy.IsCoreLocalTarget(t.ExecutorType, t.RsyncTarget) {
			addMountCandidate(t.RsyncTarget, establishedTasks[t.ID])
		}
	}

	// Mount-point totals reflect the whole volume. Operators must not learn
	// shared-disk capacity/usage outside their nodes — only admin/viewer get
	// Statfs aggregates; deduplicate by backing filesystem identity (Linux
	// mountinfo superblock major:minor via STATX_MNT_ID, falling back to st_dev)
	// so subdirectories and Btrfs subvolumes on the same volume are never reported twice.
	mountPoints := make([]mountPointInfo, 0)
	mountPointsIncomplete := false
	if !needFilter && len(targetPaths) > 0 {
		statMount := h.statMount
		if statMount == nil {
			statMount = newDefaultMountVolumeStatter()
		}
		seenVolumes := make(map[string]bool)
		for _, tp := range targetPaths {
			if scanCtx.Err() != nil {
				mountPointsIncomplete = true
				break
			}
			volKey, stat, err := statMount(tp)
			if err != nil {
				if scanCtx.Err() != nil {
					mountPointsIncomplete = true
					break
				}
				if !errors.Is(err, os.ErrNotExist) || mountExpectedToExist[tp] {
					mountPointsIncomplete = true
				}
				continue
			}
			if seenVolumes[volKey] {
				continue
			}
			seenVolumes[volKey] = true
			totalGB := float64(stat.Blocks) * float64(stat.Bsize) / (1024 * 1024 * 1024)
			freeGB := float64(stat.Bavail) * float64(stat.Bsize) / (1024 * 1024 * 1024)
			usedGB := totalGB - freeGB
			pct := 0.0
			if totalGB > 0 {
				pct = usedGB / totalGB * 100
			}
			mountPoints = append(mountPoints, mountPointInfo{
				Path:    tp,
				UsedGB:  round2(usedGB),
				TotalGB: round2(totalGB),
				Pct:     round2(pct),
			})
		}
	}

	scanDir := h.scanDir
	if scanDir == nil {
		scanDir = dirSizeGB
	}

	type mergedTaskTarget struct {
		nodeID          uint
		nodeName        string
		target          string
		expectedToExist bool
	}
	mergedTasks := make([]mergedTaskTarget, 0, len(taskRows))
	taskTargetIndex := make(map[string]int)
	seenTaskTargets := make(map[string]struct{})
	for _, task := range taskRows {
		target := strings.TrimSpace(task.RsyncTarget)
		if !policy.IsCoreLocalTarget(task.ExecutorType, target) {
			continue
		}
		expected := establishedTasks[task.ID]
		key := fmt.Sprintf("%d:%s", task.NodeID, target)
		if idx, exists := taskTargetIndex[key]; exists {
			if expected {
				mergedTasks[idx].expectedToExist = true
			}
			continue
		}
		taskTargetIndex[key] = len(mergedTasks)
		seenTaskTargets[key] = struct{}{}
		mergedTasks = append(mergedTasks, mergedTaskTarget{
			nodeID:          task.NodeID,
			nodeName:        task.NodeName,
			target:          target,
			expectedToExist: expected,
		})
	}

	perNode := make([]perNodeUsage, 0)
	perNodeIncomplete := false
	for _, entry := range mergedTasks {
		usedGB, complete := scanDir(scanCtx, entry.target, !entry.expectedToExist)
		if !complete {
			perNodeIncomplete = true
		}
		perNode = append(perNode, perNodeUsage{
			NodeID:     entry.nodeID,
			NodeName:   entry.nodeName,
			Path:       entry.target,
			UsedGB:     round2(usedGB),
			Incomplete: !complete,
		})
	}

	// If a policy has no materialized task yet, expose its isolated new target
	// without treating the policy base path as a shared node directory.
	for _, candidate := range policyNodes {
		target := policy.PolicyNodeTargetPath(candidate.Target, candidate.PolicyID, candidate.NodeID)
		if target == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s", candidate.NodeID, target)
		if _, exists := seenTaskTargets[key]; exists {
			continue
		}
		seenTaskTargets[key] = struct{}{}
		usedGB, complete := scanDir(scanCtx, target, true)
		if !complete {
			perNodeIncomplete = true
		}
		perNode = append(perNode, perNodeUsage{
			NodeID:     candidate.NodeID,
			NodeName:   candidate.NodeName,
			Path:       target,
			UsedGB:     round2(usedGB),
			Incomplete: !complete,
		})
	}

	respondOK(c, storageUsageResponse{
		MountPoints:           mountPoints,
		PerNode:               perNode,
		Incomplete:            mountPointsIncomplete || perNodeIncomplete,
		MountPointsIncomplete: mountPointsIncomplete,
	})
}

func dirSizeGB(ctx context.Context, path string, allowMissingRoot bool) (float64, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	// Strip only trailing separators and dot components so Lstat can inspect
	// the final entry without changing how the OS resolves intermediate links.
	for len(path) > 1 {
		if os.IsPathSeparator(path[len(path)-1]) {
			path = path[:len(path)-1]
		} else if path[len(path)-1] == '.' && os.IsPathSeparator(path[len(path)-2]) {
			path = path[:len(path)-1]
		} else {
			break
		}
	}
	rootInfo, err := os.Lstat(path)
	if err != nil {
		return 0, ctx.Err() == nil && errors.Is(err, os.ErrNotExist) && allowMissingRoot
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return 0, false
	}
	if !rootInfo.IsDir() {
		return float64(rootInfo.Size()) / (1024 * 1024 * 1024), ctx.Err() == nil
	}
	var totalSize int64
	incomplete := false
	var walkDir func(string, bool) bool
	walkDir = func(dir string, root bool) bool {
		if ctx.Err() != nil {
			return false
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) || (root && !allowMissingRoot) {
				incomplete = true
			}
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return false
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				// Keep the original root spelling and raw directory name: Join
				// would clean intermediate symlink/.. components.
				if !walkDir(dir+string(os.PathSeparator)+entry.Name(), false) {
					return false
				}
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				if !errors.Is(infoErr, os.ErrNotExist) {
					incomplete = true
				}
				continue
			}
			totalSize += info.Size()
		}
		return ctx.Err() == nil
	}
	if !walkDir(path, true) || ctx.Err() != nil {
		incomplete = true
	}
	return float64(totalSize) / (1024 * 1024 * 1024), !incomplete
}

func round2(v float64) float64 {
	return math.Floor(v*100) / 100
}
