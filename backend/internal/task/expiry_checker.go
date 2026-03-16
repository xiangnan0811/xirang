package task

import (
	"fmt"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
)

// checkNodeExpiry 检查节点到期状态，触发告警、紧急备份或归档。
func (m *Manager) checkNodeExpiry() {
	var nodes []model.Node
	if err := m.db.Where("expiry_date IS NOT NULL AND archived = ?", false).Find(&nodes).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("查询节点到期信息失败")
		return
	}

	for _, node := range nodes {
		if node.ExpiryDate == nil {
			continue
		}
		remaining := time.Until(*node.ExpiryDate)

		if remaining <= 0 {
			// 已过期：归档节点并移除关联任务调度
			if err := m.db.Model(&model.Node{}).Where("id = ?", node.ID).Update("archived", true).Error; err != nil {
				logger.Module("task").Warn().Uint("node_id", node.ID).Err(err).Msg("归档过期节点失败")
				continue
			}
			var tasks []model.Task
			if err := m.db.Where("node_id = ?", node.ID).Find(&tasks).Error; err == nil {
				for _, t := range tasks {
					m.RemoveSchedule(t.ID)
				}
			}
			logger.Module("task").Info().Uint("node_id", node.ID).Str("node_name", node.Name).Msg("节点已过期，自动归档")
			if err := alerting.RaiseNodeExpiryWarning(m.db, node, fmt.Sprintf("节点 %s 已过期并自动归档", node.Name)); err != nil {
				logger.Module("task").Warn().Uint("node_id", node.ID).Err(err).Msg("创建节点过期告警失败")
			}
		} else if remaining <= 24*time.Hour {
			// 1 天内到期：告警 + 紧急备份
			msg := fmt.Sprintf("节点 %s 将在 %.0f 小时后过期，已触发紧急备份", node.Name, remaining.Hours())
			if err := alerting.RaiseNodeExpiryWarning(m.db, node, msg); err != nil {
				logger.Module("task").Warn().Uint("node_id", node.ID).Err(err).Msg("创建节点过期告警失败")
			}
			// 触发紧急备份
			var tasks []model.Task
			if err := m.db.Where("node_id = ? AND source = ? AND executor_type IN ?",
				node.ID, "policy", []string{"rsync", "restic", "rclone"}).Find(&tasks).Error; err == nil {
				for _, t := range tasks {
					if _, err := m.TriggerManual(t.ID); err != nil {
						logger.Module("task").Warn().Uint("task_id", t.ID).Err(err).Msg("紧急备份触发失败")
					}
				}
			}
		} else if remaining <= 3*24*time.Hour {
			// 3 天内到期：仅告警
			msg := fmt.Sprintf("节点 %s 将在 %.0f 小时后过期，请及时处理", node.Name, remaining.Hours())
			if err := alerting.RaiseNodeExpiryWarning(m.db, node, msg); err != nil {
				logger.Module("task").Warn().Uint("node_id", node.ID).Err(err).Msg("创建节点过期告警失败")
			}
		}
	}
}
