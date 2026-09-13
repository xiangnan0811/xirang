package policy

import (
	"fmt"
	"path/filepath"
	"strings"

	"xirang/backend/internal/cronutil"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NodeTargetPath retains the legacy backup-dir layout for read-only historical
// inventory. New policy tasks must use PolicyNodeTargetPath so policy IDs and
// node IDs own disjoint physical directories.
func NodeTargetPath(basePath string, backupDir string) string {
	if strings.ContainsAny(backupDir, "/\\") || strings.Contains(backupDir, "..") || backupDir == "" {
		return filepath.Join(strings.TrimRight(basePath, "/"), "_invalid_node_")
	}
	return filepath.Join(strings.TrimRight(basePath, "/"), backupDir)
}

func localTargetOwner(task model.Task) (TargetOwner, bool) {
	target := strings.TrimSpace(task.RsyncTarget)
	if !IsCoreLocalTarget(task.ExecutorType, target) {
		return TargetOwner{}, false
	}
	owner := TargetOwner{NodeID: task.NodeID, TaskID: task.ID, Target: target}
	if task.PolicyID != nil {
		owner.PolicyID = *task.PolicyID
	}
	return owner, true
}

func validateTaskTargets(db *gorm.DB, proposed []TargetOwner) error {
	var tasks []model.Task
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "node_id", "policy_id", "executor_type", "rsync_target").
		Order("id").Find(&tasks).Error; err != nil {
		return fmt.Errorf("查询现有任务目标失败: %w", err)
	}
	claims := make([]TargetOwner, 0, len(tasks)+len(proposed))
	for _, task := range tasks {
		if owner, ok := localTargetOwner(task); ok {
			claims = append(claims, owner)
		}
	}
	for _, candidate := range proposed {
		if _, err := ValidateTargetOwnership(candidate.Target, candidate, claims); err != nil {
			return fmt.Errorf("备份目标路径存在重叠或历史归属不明: %w", err)
		}
		claims = append(claims, candidate)
	}
	return nil
}

// TaskRunner is the interface needed by sync logic to manage cron schedules.
type TaskRunner interface {
	SyncSchedule(task model.Task) error
	RemoveSchedule(taskID uint)
}

const taskRetryingStatus = model.TaskRunStatusRetrying

func taskCronScheduleUpdatesForMode(
	task model.Task,
	cronSpec string,
	mode model.TaskRunCronCursorMode,
) map[string]interface{} {
	updates := map[string]interface{}{"cron_spec": cronSpec}
	if strings.TrimSpace(task.CronSpec) == strings.TrimSpace(cronSpec) &&
		strings.TrimSpace(cronSpec) != "" {
		return updates
	}
	if strings.EqualFold(strings.TrimSpace(task.Status), taskRetryingStatus) {
		if mode == model.TaskRunCronCursorModeRegularV1 {
			if !task.Enabled || strings.TrimSpace(cronSpec) == "" {
				updates["next_run_at"] = nil
			} else {
				updates["next_run_at"] = cronutil.Next(cronSpec)
			}
		}
		// Legacy retrying tasks retain Task.NextRunAt as their historical
		// retry deadline, even when cron is removed or replaced.
		return updates
	}
	if !task.Enabled {
		// Paused generated tasks have no active schedule generation. The next
		// resume must initialize the edited policy cron from its own boundary.
		updates["next_run_at"] = nil
		return updates
	}
	updates["next_run_at"] = cronutil.Next(cronSpec)
	return updates
}

// taskCronScheduleUpdatesTx reads retry provenance only after the caller has
// locked the Task row on this same transaction. A provenance decode failure is
// returned so no schedule field is changed under an ambiguous mode.
func taskCronScheduleUpdatesTx(tx *gorm.DB, task model.Task, cronSpec string) (map[string]interface{}, error) {
	mode := model.TaskRunCronCursorModeLegacy
	if strings.EqualFold(strings.TrimSpace(task.Status), taskRetryingStatus) {
		var err error
		mode, err = model.LatestTaskRetryEffectCronCursorModeTx(tx, task.ID)
		if err != nil {
			return nil, fmt.Errorf("读取任务重试调度归属失败(task_id=%d): %w", task.ID, err)
		}
	}
	return taskCronScheduleUpdatesForMode(task, cronSpec, mode), nil
}
func lockPolicyTasks(db *gorm.DB, policyID uint, nodeIDs []uint) ([]model.Task, error) {
	if db == nil {
		return nil, fmt.Errorf("策略任务查询不可用")
	}
	query := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("policy_id = ? AND source = ?", policyID, "policy")
	if len(nodeIDs) > 0 {
		query = query.Where("node_id IN ?", nodeIDs)
	}
	var tasks []model.Task
	if err := query.Order("id").Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("锁定策略关联任务失败: %w", err)
	}
	return tasks, nil
}

func updateLockedPolicyTaskSchedules(db *gorm.DB, tasks []model.Task, cronSpec string) error {
	for i := range tasks {
		updates, err := taskCronScheduleUpdatesTx(db, tasks[i], cronSpec)
		if err != nil {
			return err
		}
		if err := db.Model(&model.Task{}).Where("id = ?", tasks[i].ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新任务调度失败(task_id=%d): %w", tasks[i].ID, err)
		}
	}
	return nil
}

// SyncPolicyTasks synchronizes policy-owned tasks in the caller's database
// transaction. Scheduler mutations are deliberately excluded: the database
// commit is authoritative and callers must invoke SyncPolicySchedules only
// after that commit succeeds.
func SyncPolicyTasks(db *gorm.DB, runner TaskRunner, policy model.Policy, nodeIDs []uint) error {
	_ = runner
	if policy.ID == 0 {
		return fmt.Errorf("策略尚未持久化，无法生成隔离备份目标")
	}
	if err := LockTargetOwnershipSpace(db); err != nil {
		return fmt.Errorf("锁定备份目标归属失败: %w", err)
	}
	var lockedPolicy model.Policy
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").First(&lockedPolicy, policy.ID).Error; err != nil {
		return fmt.Errorf("锁定策略失败: %w", err)
	}

	// 加载关联节点信息（用于拼接任务名称）
	var nodes []model.Node
	if len(nodeIDs) > 0 {
		if err := db.Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
			return fmt.Errorf("查询关联节点失败: %w", err)
		}
	}
	nodeMap := make(map[uint]model.Node, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n
	}

	// 策略未启用时不写入 cron_spec，避免重启后被 LoadSchedules 自动加载
	cronSpec := policy.CronSpec
	if !policy.Enabled {
		cronSpec = ""
	}

	// 查询该策略下所有 source='policy' 的现有任务。其 RsyncTarget 是
	// 历史事实：同步/编辑策略或节点标识时绝不能静默重指向。
	var existingTasks []model.Task
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("policy_id = ? AND source = ?", policy.ID, "policy").
		Order("id").Find(&existingTasks).Error; err != nil {
		return fmt.Errorf("查询策略关联任务失败: %w", err)
	}
	taskByNode := make(map[uint]*model.Task, len(existingTasks))
	for i := range existingTasks {
		if previous, exists := taskByNode[existingTasks[i].NodeID]; exists && previous.ID != existingTasks[i].ID {
			return fmt.Errorf("策略 %d 的节点 %d 存在多个策略任务，拒绝同步", policy.ID, existingTasks[i].NodeID)
		}
		taskByNode[existingTasks[i].NodeID] = &existingTasks[i]
	}

	// Validate every persisted local target and each proposed new target before
	// any row is changed. This fails closed on legacy shared paths, aliases, and
	// ancestor/descendant targets discoverable in the task inventory.
	proposed := make([]TargetOwner, 0, len(nodeIDs))
	for _, nid := range nodeIDs {
		if _, ok := nodeMap[nid]; !ok {
			continue
		}
		if _, exists := taskByNode[nid]; exists {
			continue
		}
		target := PolicyNodeTargetPath(policy.TargetPath, policy.ID, nid)
		if target == "" {
			return fmt.Errorf("策略 %d 的备份根目录不安全，无法生成隔离目标", policy.ID)
		}
		proposed = append(proposed, TargetOwner{PolicyID: policy.ID, NodeID: nid, Target: target})
	}
	if err := validateTaskTargets(db, proposed); err != nil {
		return err
	}

	newNodeSet := make(map[uint]struct{}, len(nodeIDs))
	for _, nid := range nodeIDs {
		newNodeSet[nid] = struct{}{}
	}

	// 更新或创建任务
	for _, nid := range nodeIDs {
		node, ok := nodeMap[nid]
		if !ok {
			continue
		}
		if task, exists := taskByNode[nid]; exists {
			// 更新现有任务，但保留其历史物理目标。任务行已在本事务
			// 中加锁，再读取重试归属后才决定 cursor 所有权。
			updates, err := taskCronScheduleUpdatesTx(db, *task, cronSpec)
			if err != nil {
				return err
			}
			updates["rsync_source"] = policy.SourcePath
			updates["name"] = fmt.Sprintf("%s-%s", policy.Name, node.Name)
			updates["escalation_policy_id"] = policy.EscalationPolicyID
			if err := db.Model(task).Updates(updates).Error; err != nil {
				return fmt.Errorf("更新任务失败(task_id=%d): %w", task.ID, err)
			}
		} else {
			// 创建新任务到 policy-ID/node-ID 隔离目录。
			policyID := policy.ID
			newTask := model.Task{
				Name:               fmt.Sprintf("%s-%s", policy.Name, node.Name),
				NodeID:             nid,
				PolicyID:           &policyID,
				RsyncSource:        policy.SourcePath,
				RsyncTarget:        PolicyNodeTargetPath(policy.TargetPath, policy.ID, nid),
				ExecutorType:       "rsync",
				CronSpec:           cronSpec,
				NextRunAt:          cronutil.Next(cronSpec),
				Status:             "pending",
				Source:             "policy",
				EscalationPolicyID: policy.EscalationPolicyID,
			}
			if err := db.Create(&newTask).Error; err != nil {
				return fmt.Errorf("创建任务失败(node_id=%d): %w", nid, err)
			}
		}
	}

	// 将不再关联的节点对应的任务暂停调度（保留策略归属，以便重新加入时复用）。
	for nid, task := range taskByNode {
		if _, inNew := newNodeSet[nid]; !inNew {
			updates, err := taskCronScheduleUpdatesTx(db, *task, "")
			if err != nil {
				return err
			}
			if err := db.Model(task).Updates(updates).Error; err != nil {
				return fmt.Errorf("暂停任务失败(task_id=%d): %w", task.ID, err)
			}
		}
	}

	return nil
}

// PauseTasksForPolicy removes cron schedules for all tasks associated with a policy.
func PauseTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint) error {
	_ = runner
	tasks, err := lockPolicyTasks(db, policyID, nil)
	if err != nil {
		return err
	}
	// Persisting an empty cron spec clears a marked task's regular cursor, but
	// preserves a legacy task's retry deadline. Both decisions are made after
	// locking each task in this transaction.
	return updateLockedPolicyTaskSchedules(db, tasks, "")
}

// ResumeTasksForPolicy restores cron schedules for tasks whose nodes are still
// associated with the policy.
// ResumeTasksForPolicy restores cron specifications in the database. The
// scheduler is reconciled only after the surrounding transaction commits.
func ResumeTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint, cronSpec string) error {
	_ = runner
	var activeNodeIDs []uint
	if err := db.Table("policy_nodes").Where("policy_id = ?", policyID).Pluck("node_id", &activeNodeIDs).Error; err != nil {
		return fmt.Errorf("查询策略关联节点失败: %w", err)
	}
	if len(activeNodeIDs) == 0 {
		return nil
	}
	tasks, err := lockPolicyTasks(db, policyID, activeNodeIDs)
	if err != nil {
		return err
	}
	return updateLockedPolicyTaskSchedules(db, tasks, cronSpec)
}

// OrphanTasksForPolicy marks all tasks for a policy as orphaned. Scheduler
// removal is performed by RemovePolicySchedules after the database commit.
func OrphanTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint) error {
	_ = runner
	tasks, err := lockPolicyTasks(db, policyID, nil)
	if err != nil {
		return err
	}
	for i := range tasks {
		updates, err := taskCronScheduleUpdatesTx(db, tasks[i], "")
		if err != nil {
			return err
		}
		updates["source"] = "orphaned"
		updates["policy_id"] = nil
		if err := db.Model(&model.Task{}).Where("id = ?", tasks[i].ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("孤立任务失败(task_id=%d): %w", tasks[i].ID, err)
		}
	}
	return nil
}

// SyncPolicySchedules reconciles the process-local scheduler from committed
// database rows. It is intentionally called outside write transactions.
func SyncPolicySchedules(db *gorm.DB, runner TaskRunner, policyID uint) error {
	if runner == nil {
		return nil
	}
	var tasks []model.Task
	if err := db.Where("policy_id = ? AND source = ?", policyID, "policy").Find(&tasks).Error; err != nil {
		return fmt.Errorf("查询策略任务调度失败: %w", err)
	}
	for _, task := range tasks {
		if err := runner.SyncSchedule(task); err != nil {
			return fmt.Errorf("同步任务调度失败(task_id=%d): %w", task.ID, err)
		}
	}
	return nil
}

func RemovePolicySchedules(db *gorm.DB, runner TaskRunner, taskIDs []uint) error {
	if runner == nil {
		return nil
	}
	for _, taskID := range taskIDs {
		runner.RemoveSchedule(taskID)
	}
	return nil
}
