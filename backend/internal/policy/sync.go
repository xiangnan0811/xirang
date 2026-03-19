package policy

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// NodeTargetPath appends the node name as a subdirectory to the policy target path,
// ensuring backups from different nodes don't overwrite each other.
// 防御性校验：拒绝包含路径分隔符或遍历字符的节点名。
func NodeTargetPath(basePath string, nodeName string) string {
	if strings.ContainsAny(nodeName, "/\\") || strings.Contains(nodeName, "..") || nodeName == "" {
		return filepath.Join(strings.TrimRight(basePath, "/"), "_invalid_node_")
	}
	return filepath.Join(strings.TrimRight(basePath, "/"), nodeName)
}

// TaskRunner is the interface needed by sync logic to manage cron schedules.
type TaskRunner interface {
	SyncSchedule(task model.Task) error
	RemoveSchedule(taskID uint)
}

// SyncPolicyTasks synchronizes tasks for a policy based on its associated node IDs.
// It creates new tasks, updates existing ones, and orphans tasks for removed nodes.
func SyncPolicyTasks(db *gorm.DB, runner TaskRunner, policy model.Policy, nodeIDs []uint) error {
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

	// 查询该策略下所有 source='policy' 的现有任务
	var existingTasks []model.Task
	if err := db.Where("policy_id = ? AND source = ?", policy.ID, "policy").Find(&existingTasks).Error; err != nil {
		return fmt.Errorf("查询策略关联任务失败: %w", err)
	}
	taskByNode := make(map[uint]*model.Task, len(existingTasks))
	for i := range existingTasks {
		taskByNode[existingTasks[i].NodeID] = &existingTasks[i]
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
			// 更新现有任务
			updates := map[string]interface{}{
				"rsync_source": policy.SourcePath,
				"rsync_target": NodeTargetPath(policy.TargetPath, node.Name),
				"cron_spec":    cronSpec,
				"name":         fmt.Sprintf("%s-%s", policy.Name, node.Name),
			}
			if err := db.Model(task).Updates(updates).Error; err != nil {
				return fmt.Errorf("更新任务失败(task_id=%d): %w", task.ID, err)
			}
			task.CronSpec = cronSpec
			if policy.Enabled {
				if err := runner.SyncSchedule(*task); err != nil {
					log.Printf("warn: 同步任务调度失败(task_id=%d): %v", task.ID, err)
				}
			} else {
				runner.RemoveSchedule(task.ID)
			}
		} else {
			// 创建新任务
			policyID := policy.ID
			newTask := model.Task{
				Name:         fmt.Sprintf("%s-%s", policy.Name, node.Name),
				NodeID:       nid,
				PolicyID:     &policyID,
				RsyncSource:  policy.SourcePath,
				RsyncTarget:  NodeTargetPath(policy.TargetPath, node.Name),
				ExecutorType: "rsync",
				CronSpec:     cronSpec,
				Status:       "pending",
				Source:       "policy",
			}
			if err := db.Create(&newTask).Error; err != nil {
				return fmt.Errorf("创建任务失败(node_id=%d): %w", nid, err)
			}
			if policy.Enabled {
				if err := runner.SyncSchedule(newTask); err != nil {
					log.Printf("warn: 注册任务调度失败(task_id=%d): %v", newTask.ID, err)
				}
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
			runner.RemoveSchedule(task.ID)
		}
	}

	return nil
}

// PauseTasksForPolicy removes cron schedules for all tasks associated with a policy.
func PauseTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint) error {
	var tasks []model.Task
	if err := db.Where("policy_id = ? AND source = ?", policyID, "policy").Find(&tasks).Error; err != nil {
		return err
	}
	for _, t := range tasks {
		runner.RemoveSchedule(t.ID)
	}
	// 持久化清除 cron_spec，防止重启后重新加载调度
	if err := db.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Update("cron_spec", "").Error; err != nil {
		return fmt.Errorf("清除任务调度失败: %w", err)
	}
	return nil
}

// ResumeTasksForPolicy restores cron schedules for tasks whose nodes are still associated with the policy.
// Tasks for nodes that have been removed from the policy (cron_spec already cleared) are not resumed.
func ResumeTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint, cronSpec string) error {
	// 只恢复仍在 policy_nodes 关联中的节点对应的任务
	var activeNodeIDs []uint
	if err := db.Table("policy_nodes").Where("policy_id = ?", policyID).Pluck("node_id", &activeNodeIDs).Error; err != nil {
		return fmt.Errorf("查询策略关联节点失败: %w", err)
	}
	if len(activeNodeIDs) == 0 {
		return nil
	}

	var tasks []model.Task
	if err := db.Where("policy_id = ? AND source = ? AND node_id IN ?", policyID, "policy", activeNodeIDs).Find(&tasks).Error; err != nil {
		return err
	}
	if err := db.Model(&model.Task{}).Where("policy_id = ? AND source = ? AND node_id IN ?", policyID, "policy", activeNodeIDs).Update("cron_spec", cronSpec).Error; err != nil {
		return fmt.Errorf("恢复任务调度失败: %w", err)
	}
	for i := range tasks {
		tasks[i].CronSpec = cronSpec
		if err := runner.SyncSchedule(tasks[i]); err != nil {
			log.Printf("warn: 恢复任务调度失败(task_id=%d): %v", tasks[i].ID, err)
		}
	}
	return nil
}

// OrphanTasksForPolicy marks all tasks for a policy as orphaned and removes their schedules.
func OrphanTasksForPolicy(db *gorm.DB, runner TaskRunner, policyID uint) error {
	var tasks []model.Task
	if err := db.Where("policy_id = ? AND source = ?", policyID, "policy").Find(&tasks).Error; err != nil {
		return err
	}
	for _, t := range tasks {
		runner.RemoveSchedule(t.ID)
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
