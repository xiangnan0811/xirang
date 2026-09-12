package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"

	"github.com/gin-gonic/gin"
	"golang.org/x/sys/unix"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NodeMigrateRequest 节点迁移请求
type NodeMigrateRequest struct {
	TargetNodeID  uint `json:"targetNodeId" binding:"required"`
	ArchiveSource bool `json:"archiveSource"`
	PausePolicies bool `json:"pausePolicies"`
	MigrateData   bool `json:"migrateData"` // 是否迁移本地备份数据
}

// DataMigrateItem 单个策略的数据迁移结果
type DataMigrateItem struct {
	PolicyID   uint   `json:"policyId"`
	PolicyName string `json:"policyName"`
	Status     string `json:"status"` // copied / skipped / error
	Message    string `json:"message"`
}

// Migrate godoc
// @Summary      迁移节点
// @Description  将源节点的策略和任务安全迁移到目标节点，保留 TaskRun 历史和依赖链
// @Tags         node-migration
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                          true  "源节点 ID"
// @Param        body  body      handlers.NodeMigrateRequest  true  "迁移请求"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /nodes/{id}/migrate [post]
// Migrate 将源节点的策略和任务安全迁移到目标节点。
// 保留原有 Task 记录（保持 TaskRun 历史、executor_type、依赖链完整）。
func (h *NodeHandler) Migrate(c *gin.Context) {
	sourceID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req NodeMigrateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数无效")
		return
	}

	if sourceID == req.TargetNodeID {
		respondBadRequest(c, "源节点和目标节点不能相同")
		return
	}

	// 加载源节点和目标节点
	var sourceNode, targetNode model.Node
	if err := h.db.First(&sourceNode, sourceID).Error; err != nil {
		respondNotFound(c, "源节点不存在")
		return
	}
	if sourceNode.Archived {
		respondBadRequest(c, "源节点已归档")
		return
	}
	if err := h.db.First(&targetNode, req.TargetNodeID).Error; err != nil {
		respondNotFound(c, "目标节点不存在")
		return
	}
	if targetNode.Archived {
		respondBadRequest(c, "目标节点已归档")
		return
	}

	// operator 角色需对目标节点有 ownership
	if middleware.CurrentRole(c) == "operator" {
		userID := middleware.CurrentUserID(c)
		var count int64
		if err := h.db.Model(&model.NodeOwner{}).Where("node_id = ? AND user_id = ?", req.TargetNodeID, userID).Count(&count).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if count == 0 {
			respondForbidden(c, "无权迁移到该目标节点")
			return
		}
	}

	// 收集受影响的 policyIDs
	var policyIDs []uint
	if err := h.db.Model(&model.PolicyNode{}).Where("node_id = ?", sourceID).Pluck("policy_id", &policyIDs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if len(policyIDs) == 0 {
		respondOK(c, gin.H{"migratedPolicies": 0, "migratedTasks": 0, "archivedSource": false, "dataMigration": nil})
		return
	}

	// 收集受影响的策略和任务
	var policies []model.Policy
	if err := h.db.Where("id IN ?", policyIDs).Find(&policies).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var allTasks []model.Task
	if err := h.db.Preload("Policy").
		Where("node_id = ? AND source = ? AND policy_id IN ?", sourceID, "policy", policyIDs).
		Find(&allTasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	taskSnapshots := make(map[uint]migrationTaskSnapshot, len(allTasks))
	for _, task := range allTasks {
		taskSnapshots[task.ID] = snapshotMigrationTask(task)
	}
	policyTargetSnapshots := make(map[uint]string, len(policies))
	for _, policyEntity := range policies {
		policyTargetSnapshots[policyEntity.ID] = policyEntity.TargetPath
	}

	// Inventory all local targets before any cancellation or DB mutation. A
	// migration is rejected if legacy paths overlap or if a local source cannot
	// be copied into a fresh policy-ID/node-ID destination.
	var targetInventory []model.Task
	if err := h.db.Select("id", "node_id", "policy_id", "executor_type", "rsync_target").Find(&targetInventory).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	targetOwners := make([]policy.TargetOwner, 0, len(targetInventory))
	for _, task := range targetInventory {
		if !policy.IsCoreLocalTarget(task.ExecutorType, task.RsyncTarget) {
			continue
		}
		owner := policy.TargetOwner{NodeID: task.NodeID, TaskID: task.ID, Target: task.RsyncTarget}
		if task.PolicyID != nil {
			owner.PolicyID = *task.PolicyID
		}
		targetOwners = append(targetOwners, owner)
	}
	if err := policy.ValidateTargetOwners(targetOwners); err != nil {
		respondBadRequest(c, "本地备份目标存在重复或重叠，已拒绝迁移")
		return
	}
	if err := requireMigrationQuiescence(h.db, allTasks); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	migrationTargets := make(map[uint]string, len(allTasks))
	migrationClaims := make(map[uint]migrationDestinationClaim, len(allTasks))
	var dataMigration []DataMigrateItem
	var err error
	if req.MigrateData {
		migrateCtx, migrateCancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
		dataMigration, migrationTargets, migrationClaims, err = stageLocalBackupDataMigrationWithClaims(migrateCtx, allTasks, targetNode, targetOwners)
		migrateCancel()
		if err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}

	// 数据库事务
	migratedTasks := 0

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := policy.LockTargetOwnershipSpace(tx); err != nil {
			return fmt.Errorf("锁定迁移目标归属失败: %w", err)
		}
		// Keep the lock order: target ownership -> Policy -> Task -> Node.
		// The migration has already copied data while paused; these locks
		// revalidate the snapshot and ownership immediately before cutover.
		var lockedPolicies []model.Policy
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ?", policyIDs).Order("id").Find(&lockedPolicies).Error; err != nil {
			return fmt.Errorf("锁定策略失败: %w", err)
		}
		if len(lockedPolicies) != len(policyIDs) {
			return fmt.Errorf("迁移策略集合已变化，拒绝数据库切换")
		}
		for _, lockedPolicy := range lockedPolicies {
			if targetPath, ok := policyTargetSnapshots[lockedPolicy.ID]; !ok || targetPath != lockedPolicy.TargetPath {
				return fmt.Errorf("策略 %d 在迁移期间发生变化，拒绝数据库切换", lockedPolicy.ID)
			}
		}

		var lockedInventory []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "node_id", "policy_id", "executor_type", "rsync_target", "cron_spec", "enabled", "status", "updated_at").
			Order("id").Find(&lockedInventory).Error; err != nil {
			return fmt.Errorf("锁定任务失败: %w", err)
		}
		lockedByID := make(map[uint]model.Task, len(lockedInventory))
		for _, task := range lockedInventory {
			lockedByID[task.ID] = task
		}
		for _, original := range allTasks {
			current, ok := lockedByID[original.ID]
			if !ok || migrationTaskChanged(current, taskSnapshots[original.ID]) {
				return fmt.Errorf("任务 %d 在数据迁移期间发生变化，拒绝数据库切换", original.ID)
			}
			if current.Enabled || strings.TrimSpace(current.CronSpec) != "" ||
				current.Status == model.TaskRunStatusRunning || current.Status == model.TaskRunStatusRetrying {
				return fmt.Errorf("任务 %d 未保持暂停状态，拒绝数据库切换", current.ID)
			}
		}
		var activeRuns int64
		if err := tx.Model(&model.TaskRun{}).
			Where("task_id IN ? AND status IN ?", taskSnapshotsIDs(taskSnapshots), model.TaskRunActiveStatuses()).
			Count(&activeRuns).Error; err != nil {
			return fmt.Errorf("复核源节点活动执行失败: %w", err)
		}
		if activeRuns > 0 {
			return fmt.Errorf("源节点仍有 %d 个活动执行，拒绝数据库切换", activeRuns)
		}
		currentOwners := make([]policy.TargetOwner, 0, len(lockedInventory))
		for _, task := range lockedInventory {
			if !policy.IsCoreLocalTarget(task.ExecutorType, task.RsyncTarget) {
				continue
			}
			owner := policy.TargetOwner{NodeID: task.NodeID, TaskID: task.ID, Target: task.RsyncTarget}
			if task.PolicyID != nil {
				owner.PolicyID = *task.PolicyID
			}
			currentOwners = append(currentOwners, owner)
		}
		for _, original := range allTasks {
			target, ok := migrationTargets[original.ID]
			if !ok {
				continue
			}
			owner := policy.TargetOwner{PolicyID: originalPolicyID(original), NodeID: req.TargetNodeID, TaskID: original.ID, Target: target}
			if _, err := policy.ValidateTargetOwnership(target, owner, currentOwners); err != nil {
				return fmt.Errorf("任务 %d 的迁移目标归属已变化，拒绝数据库切换: %w", original.ID, err)
			}
			currentOwners = append(currentOwners, owner)
		}

		// 锁定源节点行，防止并发迁移冲突。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model.Node{}, sourceID).Error; err != nil {
			return fmt.Errorf("锁定源节点失败: %w", err)
		}

		// a. 迁移 PolicyNode 关联
		for _, pid := range policyIDs {
			var exists int64
			if err := tx.Model(&model.PolicyNode{}).Where("policy_id = ? AND node_id = ?", pid, req.TargetNodeID).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				if err := tx.Create(&model.PolicyNode{PolicyID: pid, NodeID: req.TargetNodeID}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Where("node_id = ? AND policy_id IN ?", sourceID, policyIDs).Delete(&model.PolicyNode{}).Error; err != nil {
			return err
		}

		// b. 迁移任务：更新 node_id、name、rsync_target，保留 executor_type 等所有其他字段
		for _, t := range allTasks {
			updates := map[string]any{
				"node_id": req.TargetNodeID,
			}

			if sourceNode.Name != "" && targetNode.Name != "" {
				updates["name"] = replaceLastOccurrence(t.Name, sourceNode.Name, targetNode.Name)
			}

			if target, ok := migrationTargets[t.ID]; ok {
				updates["rsync_target"] = target
			}

			if req.PausePolicies {
				updates["cron_spec"] = ""
			}

			if err := tx.Model(&model.Task{}).Where("id = ?", t.ID).Updates(updates).Error; err != nil {
				return err
			}
			migratedTasks++
		}

		// c. 可选归档源节点
		if req.ArchiveSource {
			if err := tx.Model(&model.Node{}).Where("id = ?", sourceID).Update("archived", true).Error; err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		if req.MigrateData {
			cleanupOwnedMigrationDestinations(migrationClaims)
		}
		respondInternalError(c, err)
		return
	}
	if req.MigrateData {
		if err := finalizeOwnedMigrationDestinations(migrationClaims); err != nil {
			logger.Module("migrate").Error().Err(err).Msg("清理迁移身份标记失败")
			respondInternalError(c, err)
			return
		}
	}

	// 事务成功后，更新内存中的 cron 调度（同步更新内存对象以匹配事务写入值）
	if h.trigger != nil {
		for i := range allTasks {
			h.trigger.RemoveSchedule(allTasks[i].ID)
			if !req.PausePolicies && allTasks[i].CronSpec != "" {
				allTasks[i].NodeID = req.TargetNodeID
				if sourceNode.Name != "" && targetNode.Name != "" {
					allTasks[i].Name = replaceLastOccurrence(allTasks[i].Name, sourceNode.Name, targetNode.Name)
				}
				if target, ok := migrationTargets[allTasks[i].ID]; ok {
					allTasks[i].RsyncTarget = target
				}
				_ = h.trigger.SyncSchedule(allTasks[i])
			}
		}
	}

	// Local data was staged and verified before the transaction. Never copy
	// after changing DB ownership, because a failed copy would leave a
	// partially migrated task graph.

	respondOK(c, gin.H{
		"migratedPolicies": len(policyIDs),
		"migratedTasks":    migratedTasks,
		"archivedSource":   req.ArchiveSource,
		"dataMigration":    dataMigration,
	})
}

// requireMigrationQuiescence fails closed unless every source task is already
// paused/disabled and has no durable pending, running, or retrying execution.
func requireMigrationQuiescence(db *gorm.DB, tasks []model.Task) error {
	if db == nil {
		return fmt.Errorf("迁移数据库不可用")
	}
	if len(tasks) == 0 {
		return nil
	}
	taskIDs := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
		if task.Enabled || strings.TrimSpace(task.CronSpec) != "" {
			return fmt.Errorf("迁移前必须先暂停并禁用所有源节点任务（task_id=%d）", task.ID)
		}
		if task.Status == model.TaskRunStatusRunning || task.Status == model.TaskRunStatusRetrying {
			return fmt.Errorf("源节点任务仍在运行（task_id=%d），已拒绝迁移", task.ID)
		}
	}
	var activeRuns int64

	if err := db.Model(&model.TaskRun{}).
		Where("task_id IN ? AND status IN ?", taskIDs, model.TaskRunActiveStatuses()).
		Count(&activeRuns).Error; err != nil {
		return fmt.Errorf("检查源节点活动执行失败: %w", err)
	}
	if activeRuns > 0 {
		return fmt.Errorf("源节点仍有 %d 个活动执行，已拒绝迁移", activeRuns)
	}
	return nil
}

type migrationTaskSnapshot struct {
	NodeID       uint
	PolicyID     uint
	HasPolicy    bool
	ExecutorType string
	RsyncTarget  string
	CronSpec     string
	Enabled      bool
	Status       string
	UpdatedAt    time.Time
}

func snapshotMigrationTask(task model.Task) migrationTaskSnapshot {
	snapshot := migrationTaskSnapshot{
		NodeID:       task.NodeID,
		ExecutorType: task.ExecutorType,
		RsyncTarget:  task.RsyncTarget,
		CronSpec:     task.CronSpec,
		Enabled:      task.Enabled,
		Status:       task.Status,
		UpdatedAt:    task.UpdatedAt,
	}
	if task.PolicyID != nil {
		snapshot.PolicyID = *task.PolicyID
		snapshot.HasPolicy = true
	}
	return snapshot
}

func migrationTaskChanged(task model.Task, snapshot migrationTaskSnapshot) bool {
	current := snapshotMigrationTask(task)
	return current != snapshot
}

func taskSnapshotsIDs(snapshots map[uint]migrationTaskSnapshot) []uint {
	ids := make([]uint, 0, len(snapshots))
	for id := range snapshots {
		ids = append(ids, id)
	}
	return ids
}

func originalPolicyID(task model.Task) uint {
	if task.PolicyID != nil {
		return *task.PolicyID
	}
	return 0
}

func stageLocalBackupDataMigrationWithClaims(
	ctx context.Context,
	tasks []model.Task,
	targetNode model.Node,
	existingOwners []policy.TargetOwner,
) (results []DataMigrateItem, targets map[uint]string, claims map[uint]migrationDestinationClaim, retErr error) {
	results = make([]DataMigrateItem, 0, len(tasks))
	targets = make(map[uint]string, len(tasks))
	ownedClaims := make(map[uint]migrationDestinationClaim, len(tasks))
	claims = ownedClaims
	proposedOwners := make([]policy.TargetOwner, 0, len(tasks))
	processed := make(map[string]struct{}, len(tasks))
	destinationTasks := make(map[string]uint, len(tasks))
	defer func() {
		if retErr != nil {
			cleanupOwnedMigrationDestinations(ownedClaims)
		}
	}()

	for _, task := range tasks {
		oldDir := strings.TrimSpace(task.RsyncTarget)
		policyID := uint(0)
		policyName := task.Name
		if task.PolicyID != nil {
			policyID = *task.PolicyID
		}
		if task.Policy != nil {
			policyName = task.Policy.Name
		}
		if !policy.IsCoreLocalTarget(task.ExecutorType, oldDir) {
			results = append(results, DataMigrateItem{
				PolicyID: policyID, PolicyName: policyName,
				Status: "skipped", Message: "备份目标不在 Core 本地 rsync 路径，跳过本地数据迁移",
			})
			continue
		}
		if task.Policy == nil || task.Policy.ID == 0 || strings.TrimSpace(task.Policy.TargetPath) == "" {
			return nil, nil, nil, fmt.Errorf("任务 %d 的本地备份目标缺少策略隔离信息，已拒绝迁移", task.ID)
		}
		newDir := policy.PolicyNodeTargetPath(task.Policy.TargetPath, task.Policy.ID, targetNode.ID)
		if newDir == "" {
			return nil, nil, nil, fmt.Errorf("策略 %d 的目标路径无效，已拒绝迁移", task.Policy.ID)
		}
		canonicalOld, err := policy.CanonicalTargetPath(oldDir)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("任务 %d 的源目标路径无效: %w", task.ID, err)
		}
		sourceOwner := policy.TargetOwner{NodeID: task.NodeID, TaskID: task.ID, Target: canonicalOld}
		if task.PolicyID != nil {
			sourceOwner.PolicyID = *task.PolicyID
		}
		if _, err := policy.ValidateTargetOwnership(canonicalOld, sourceOwner, existingOwners); err != nil {
			return nil, nil, nil, fmt.Errorf("任务 %d 的历史源目标归属无法证明，已拒绝复制: %w", task.ID, err)
		}
		canonicalNew, err := policy.CanonicalTargetPath(newDir)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("策略 %d 的新目标路径无效: %w", task.Policy.ID, err)
		}
		if previousTaskID, exists := destinationTasks[canonicalNew]; exists && previousTaskID != task.ID {
			return nil, nil, nil, fmt.Errorf("迁移目标 %s 被多个任务占用，已拒绝合并", canonicalNew)
		}
		destinationTasks[canonicalNew] = task.ID
		targets[task.ID] = canonicalNew
		proposedOwners = append(proposedOwners, policy.TargetOwner{
			PolicyID: task.Policy.ID, NodeID: targetNode.ID, TaskID: task.ID, Target: canonicalNew,
		})
		key := canonicalOld + " -> " + canonicalNew
		if _, done := processed[key]; done {
			results = append(results, DataMigrateItem{
				PolicyID: policyID, PolicyName: policyName,
				Status: "skipped", Message: "已与其他策略合并迁移",
			})
			continue
		}
		processed[key] = struct{}{}
	}

	targetClaims := make([]policy.TargetOwner, 0, len(existingOwners)+len(proposedOwners))
	targetClaims = append(targetClaims, existingOwners...)
	for _, proposed := range proposedOwners {
		if _, err := policy.ValidateTargetOwnership(proposed.Target, proposed, targetClaims); err != nil {
			return nil, nil, nil, fmt.Errorf("迁移目标路径存在重叠，已拒绝数据库切换: %w", err)
		}
		targetClaims = append(targetClaims, proposed)
	}

	if len(processed) == 0 {
		return results, targets, claims, nil
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		return nil, nil, nil, fmt.Errorf("无法执行安全数据迁移: rsync 不可用")
	}
	attemptID, err := newMigrationAttemptID()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("生成迁移身份失败: %w", err)
	}

	for _, task := range tasks {
		oldDir := strings.TrimSpace(task.RsyncTarget)
		newDir, ok := targets[task.ID]
		if !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, fmt.Errorf("数据迁移已取消: %w", err)
		}
		sourceBefore, err := snapshotDirectoryTree(oldDir)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("任务 %d 的源备份目录不存在或不是安全目录: %w", task.ID, err)
		}
		if _, err := os.Lstat(newDir); err == nil {
			return nil, nil, nil, fmt.Errorf("策略 %d 的迁移目标已存在，拒绝合并数据: %s", task.Policy.ID, newDir)
		} else if !os.IsNotExist(err) {
			return nil, nil, nil, fmt.Errorf("检查策略 %d 的迁移目标失败: %w", task.Policy.ID, err)
		}
		if err := os.MkdirAll(filepath.Dir(newDir), 0750); err != nil {
			return nil, nil, nil, fmt.Errorf("创建迁移目标父目录失败: %w", err)
		}
		stageDir, err := os.MkdirTemp(filepath.Dir(newDir), ".xirang-migration-")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("创建迁移暂存目录失败: %w", err)
		}
		taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		cmd := exec.CommandContext(taskCtx, "rsync", "-a", "--", oldDir+"/", stageDir+"/")
		output, copyErr := cmd.CombinedOutput()
		cancel()
		if copyErr != nil {
			_ = os.RemoveAll(stageDir)
			logger.Module("migrate").Error().Err(copyErr).Uint("task_id", task.ID).Int("output_bytes", len(output)).Msg("rsync 数据复制失败")
			return nil, nil, nil, fmt.Errorf("任务 %d 复制失败: %w", task.ID, copyErr)
		}
		if err := os.Chmod(stageDir, sourceBefore.root.mode.Perm()); err != nil {
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 保留源目录权限失败: %w", task.ID, err)
		}
		sourceAfter, snapshotErr := snapshotDirectoryTree(oldDir)
		if snapshotErr != nil {
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 复制后重新读取源目录失败: %w", task.ID, snapshotErr)
		}
		stageSnapshot, snapshotErr := snapshotDirectoryTree(stageDir)
		if snapshotErr != nil {
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 读取迁移暂存目录失败: %w", task.ID, snapshotErr)
		}
		if !directoryTreeSnapshotsEqual(sourceBefore, sourceAfter) {
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 复制期间源备份目录发生变化，已拒绝迁移", task.ID)
		}
		if !directoryTreeSnapshotsEqual(sourceAfter, stageSnapshot) {
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 验证迁移数据不一致", task.ID)
		}
		stageInfo, err := os.Stat(stageDir)
		if err != nil {
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 获取迁移暂存身份失败: %w", task.ID, err)
		}
		markerName := migrationIdentityMarkerName(task.ID, attemptID)
		if err := writeMigrationIdentity(stageDir, task.ID, newDir, attemptID); err != nil {
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 写入迁移身份失败: %w", task.ID, err)
		}
		ownedClaims[task.ID] = migrationDestinationClaim{
			Target:         newDir,
			Marker:         filepath.Join(newDir, markerName),
			MarkerContents: migrationIdentityMarkerContents(task.ID, newDir, attemptID),
			Identity:       stageInfo,
		}
		if err := renameMigrationDirectoryNoReplace(stageDir, newDir); err != nil {
			delete(ownedClaims, task.ID)
			_ = os.RemoveAll(stageDir)
			return nil, nil, nil, fmt.Errorf("任务 %d 提交迁移目录失败: %w", task.ID, err)
		}
		targetInfo, statErr := os.Stat(newDir)
		if statErr != nil || !os.SameFile(stageInfo, targetInfo) {
			return nil, nil, nil, fmt.Errorf("任务 %d 迁移目标身份校验失败", task.ID)
		}
		results = append(results, DataMigrateItem{
			PolicyID: policyIDForTask(task), PolicyName: policyNameForTask(task),
			Status: "copied", Message: fmt.Sprintf("%s → %s", oldDir, newDir),
		})
	}
	return results, targets, claims, nil
}

func policyIDForTask(task model.Task) uint {
	if task.PolicyID != nil {
		return *task.PolicyID
	}
	return 0
}

func policyNameForTask(task model.Task) string {
	if task.Policy != nil {
		return task.Policy.Name
	}
	return task.Name
}

const migrationIdentityMarkerPrefix = ".xirang-migration-owned-"

type directoryTreeEntry struct {
	mode   os.FileMode
	size   int64
	link   string
	digest [sha256.Size]byte
}

type directoryTreeSnapshot struct {
	root    directoryTreeEntry
	entries map[string]directoryTreeEntry
}

func snapshotDirectoryTree(root string) (directoryTreeSnapshot, error) {
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return directoryTreeSnapshot{}, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return directoryTreeSnapshot{}, fmt.Errorf("目录不是安全目录")
	}
	snapshot := directoryTreeSnapshot{
		root:    directoryTreeEntry{mode: rootInfo.Mode()},
		entries: make(map[string]directoryTreeEntry),
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := directoryTreeEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			entry.size = info.Size()
			entry.digest, err = hashRegularFile(path)
			if err != nil {
				return err
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		snapshot.entries[rel] = entry
		return nil
	})
	if err != nil {
		return directoryTreeSnapshot{}, err
	}
	return snapshot, nil
}

func hashRegularFile(path string) (digest [sha256.Size]byte, err error) {
	file, err := os.Open(path)
	if err != nil {
		return digest, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return digest, err
	}
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func directoryTreeSnapshotsEqual(left, right directoryTreeSnapshot) bool {
	if left.root != right.root || len(left.entries) != len(right.entries) {
		return false
	}
	for name, leftEntry := range left.entries {
		if rightEntry, ok := right.entries[name]; !ok || leftEntry != rightEntry {
			return false
		}
	}
	return true
}

func directoryTreesEqual(left, right string) (bool, error) {
	leftSnapshot, err := snapshotDirectoryTree(left)
	if err != nil {
		return false, err
	}
	rightSnapshot, err := snapshotDirectoryTree(right)
	if err != nil {
		return false, err
	}
	return directoryTreeSnapshotsEqual(leftSnapshot, rightSnapshot), nil
}

func newMigrationAttemptID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func migrationIdentityMarkerName(taskID uint, attemptID string) string {
	return fmt.Sprintf("%s%d-%s", migrationIdentityMarkerPrefix, taskID, attemptID)
}

func migrationIdentityMarkerContents(taskID uint, target, attemptID string) []byte {
	return []byte(fmt.Sprintf("task_id=%d\nattempt_id=%s\ntarget=%s\n", taskID, attemptID, filepath.Clean(target)))
}

func writeMigrationIdentity(stageDir string, taskID uint, target, attemptID string) error {
	marker := filepath.Join(stageDir, migrationIdentityMarkerName(taskID, attemptID))
	if _, err := os.Lstat(marker); err == nil {
		return fmt.Errorf("迁移暂存目录已包含身份标记")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(marker, migrationIdentityMarkerContents(taskID, target, attemptID), 0600)
}

func renameMigrationDirectoryNoReplace(stageDir, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, stageDir, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}

type migrationDestinationClaim struct {
	Target         string
	Marker         string
	MarkerContents []byte
	Identity       os.FileInfo
}

func cleanupOwnedMigrationDestinations(destinations map[uint]migrationDestinationClaim) {
	for taskID, claim := range destinations {
		if err := removeOwnedMigrationDestination(claim); err != nil {
			logger.Module("migrate").Warn().Err(err).Uint("task_id", taskID).Str("target", claim.Target).
				Msg("清理迁移目录失败，保留非确认归属目录")
		}
	}
}

func removeOwnedMigrationDestination(claim migrationDestinationClaim) error {
	info, err := os.Lstat(claim.Target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("迁移目标不是安全目录")
	}
	if claim.Identity == nil || !os.SameFile(claim.Identity, info) {
		return fmt.Errorf("迁移目标身份已变化，拒绝删除")
	}
	contents, err := os.ReadFile(claim.Marker)
	if err != nil {
		return fmt.Errorf("迁移目标身份标记不可用: %w", err)
	}
	if string(contents) != string(claim.MarkerContents) {
		return fmt.Errorf("迁移目标身份标记不匹配")
	}
	return os.RemoveAll(claim.Target)
}

func finalizeOwnedMigrationDestinations(destinations map[uint]migrationDestinationClaim) error {
	for taskID, claim := range destinations {
		info, err := os.Lstat(claim.Target)
		if err != nil {
			return fmt.Errorf("读取迁移目标失败(task_id=%d): %w", taskID, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || claim.Identity == nil || !os.SameFile(claim.Identity, info) {
			return fmt.Errorf("迁移目标身份已变化(task_id=%d)", taskID)
		}
		contents, err := os.ReadFile(claim.Marker)
		if err != nil {
			return fmt.Errorf("读取迁移身份标记失败(task_id=%d): %w", taskID, err)
		}
		if string(contents) != string(claim.MarkerContents) {
			return fmt.Errorf("迁移目标身份标记不匹配(task_id=%d)", taskID)
		}
		if err := os.Remove(claim.Marker); err != nil {
			return fmt.Errorf("删除迁移身份标记失败(task_id=%d): %w", taskID, err)
		}
	}
	return nil
}

// replaceLastOccurrence 替换字符串中最后一次出现的 old 为 new。
func replaceLastOccurrence(s, old, new string) string {
	idx := strings.LastIndex(s, old)
	if idx < 0 {
		return s
	}
	return s[:idx] + new + s[idx+len(old):]
}
