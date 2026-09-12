package policy

import (
	"fmt"
	"path/filepath"
	"strings"

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
			// 更新现有任务，但保留其历史物理目标。
			updates := map[string]interface{}{
				"rsync_source":         policy.SourcePath,
				"cron_spec":            cronSpec,
				"name":                 fmt.Sprintf("%s-%s", policy.Name, node.Name),
				"escalation_policy_id": policy.EscalationPolicyID,
			}
			if err := db.Model(task).Updates(updates).Error; err != nil {
				return fmt.Errorf("更新任务失败(task_id=%d): %w", task.ID, err)
			}
			task.CronSpec = cronSpec
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
				Status:             "pending",
				Source:             "policy",
				EscalationPolicyID: policy.EscalationPolicyID,
			}
			if err := db.Create(&newTask).Error; err != nil {
				return fmt.Errorf("创建任务失败(node_id=%d): %w", nid, err)
			}
		}
	}

	// 将不再关联的节点对应的任务暂停调度（保留策略归属，以便重新加入时复用）
	for nid, task := range taskByNode {
		if _, inNew := newNodeSet[nid]; !inNew {
			if err := db.Model(task).Updates(map[string]interface{}{
				"cron_spec": "",
			}).Error; err != nil {
				return fmt.Errorf("暂停任务失败(task_id=%d): %w", task.ID, err)
			}
		}
	}

	return nil
}

// PauseTasksForPolicy removes cron schedules for all tasks associated with a policy.
func PauseTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint) error {
	_ = runner
	// 持久化清除 cron_spec，防止重启后重新加载调度
	if err := db.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Update("cron_spec", "").Error; err != nil {
		return fmt.Errorf("清除任务调度失败: %w", err)
	}
	return nil
}

// ResumeTasksForPolicy restores cron schedules for tasks whose nodes are still associated with the policy.
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
	if err := db.Model(&model.Task{}).
		Where("policy_id = ? AND source = ? AND node_id IN ?", policyID, "policy", activeNodeIDs).
		Update("cron_spec", cronSpec).Error; err != nil {
		return fmt.Errorf("恢复任务调度失败: %w", err)
	}
	return nil
}

// OrphanTasksForPolicy marks all tasks for a policy as orphaned. Scheduler
// removal is performed by RemovePolicySchedules after the database commit.
func OrphanTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint) error {
	_ = runner
	var tasks []model.Task
	if err := db.Where("policy_id = ? AND source = ?", policyID, "policy").Find(&tasks).Error; err != nil {
		return err
	}
	for _, t := range tasks {
		if err := db.Model(&t).Updates(map[string]interface{}{
			"source":    "orphaned",
			"policy_id": nil,
			"cron_spec": "",
		}).Error; err != nil {
			return fmt.Errorf("孤立任务失败(task_id=%d): %w", t.ID, err)
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
