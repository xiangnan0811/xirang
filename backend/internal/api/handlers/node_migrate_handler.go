package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
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

// Migrate 将源节点的策略和任务安全迁移到目标节点。
// 保留原有 Task 记录（保持 TaskRun 历史、executor_type、依赖链完整）。
func (h *NodeHandler) Migrate(c *gin.Context) {
	sourceID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req NodeMigrateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	if sourceID == req.TargetNodeID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "源节点和目标节点不能相同"})
		return
	}

	// 加载源节点和目标节点
	var sourceNode, targetNode model.Node
	if err := h.db.First(&sourceNode, sourceID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "源节点不存在"})
		return
	}
	if sourceNode.Archived {
		c.JSON(http.StatusBadRequest, gin.H{"error": "源节点已归档"})
		return
	}
	if err := h.db.First(&targetNode, req.TargetNodeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "目标节点不存在"})
		return
	}
	if targetNode.Archived {
		c.JSON(http.StatusBadRequest, gin.H{"error": "目标节点已归档"})
		return
	}

	// operator 角色需对目标节点有 ownership
	if middleware.CurrentRole(c) == "operator" {
		userID := middleware.CurrentUserID(c)
		var count int64
		h.db.Model(&model.NodeOwner{}).Where("node_id = ? AND user_id = ?", req.TargetNodeID, userID).Count(&count)
		if count == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权迁移到该目标节点"})
			return
		}
	}

	// 收集受影响的 policyIDs
	var policyIDs []uint
	h.db.Model(&model.PolicyNode{}).Where("node_id = ?", sourceID).Pluck("policy_id", &policyIDs)
	if len(policyIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{"migratedPolicies": 0, "migratedTasks": 0, "archivedSource": false, "dataMigration": nil},
		})
		return
	}

	// 收集受影响的策略和任务
	var policies []model.Policy
	h.db.Where("id IN ?", policyIDs).Find(&policies)
	policyMap := make(map[uint]model.Policy, len(policies))
	for _, p := range policies {
		policyMap[p.ID] = p
	}

	var allTasks []model.Task
	h.db.Preload("Policy").
		Where("node_id = ? AND source = ? AND policy_id IN ?", sourceID, "policy", policyIDs).
		Find(&allTasks)

	// 取消运行中的任务（操作内存调度器，事务前执行）
	if h.trigger != nil {
		for _, t := range allTasks {
			if t.Status == "running" || t.Status == "retrying" {
				if err := h.trigger.Cancel(t.ID); err != nil {
					log.Printf("warn: cancel task %d: %v", t.ID, err)
				}
			}
		}
	}

	// 数据库事务
	migratedTasks := 0

	err := h.db.Transaction(func(tx *gorm.DB) error {
		// 锁定源节点行，防止并发迁移冲突
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model.Node{}, sourceID).Error; err != nil {
			return fmt.Errorf("锁定源节点失败: %w", err)
		}

		// a. 迁移 PolicyNode 关联
		for _, pid := range policyIDs {
			var exists int64
			tx.Model(&model.PolicyNode{}).Where("policy_id = ? AND node_id = ?", pid, req.TargetNodeID).Count(&exists)
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

			if t.Policy != nil && t.Policy.TargetPath != "" {
				updates["rsync_target"] = policy.NodeTargetPath(t.Policy.TargetPath, targetNode.Name)
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
		respondInternalError(c, err)
		return
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
				if allTasks[i].Policy != nil && allTasks[i].Policy.TargetPath != "" {
					allTasks[i].RsyncTarget = policy.NodeTargetPath(allTasks[i].Policy.TargetPath, targetNode.Name)
				}
				_ = h.trigger.SyncSchedule(allTasks[i])
			}
		}
	}

	// 数据迁移：复制本地备份目录
	var dataMigration []DataMigrateItem
	if req.MigrateData {
		migrateCtx, migrateCancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
		defer migrateCancel()
		dataMigration = migrateLocalBackupData(migrateCtx, policies, sourceNode.Name, targetNode.Name)
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"migratedPolicies": len(policyIDs),
			"migratedTasks":    migratedTasks,
			"archivedSource":   req.ArchiveSource,
			"dataMigration":    dataMigration,
		},
	})
}

// migrateLocalBackupData 对每个策略，将本地备份目录从旧节点名目录复制到新节点名目录。
// 仅处理本地路径的 rsync 备份；restic/rclone 等远程仓库跳过。
func migrateLocalBackupData(ctx context.Context, policies []model.Policy, sourceNodeName, targetNodeName string) []DataMigrateItem {
	var results []DataMigrateItem

	// 去重：同一个 TargetPath 只复制一次
	processed := make(map[string]struct{})

	for _, p := range policies {
		targetPath := p.TargetPath
		if targetPath == "" {
			continue
		}

		// 远程路径跳过（rsync://, user@host:path, s3:bucket 等）
		if util.IsRemotePathSpec(targetPath) {
			results = append(results, DataMigrateItem{
				PolicyID: p.ID, PolicyName: p.Name,
				Status: "skipped", Message: "备份目标为远程路径，跳过本地数据迁移",
			})
			continue
		}

		oldDir := policy.NodeTargetPath(targetPath, sourceNodeName)
		newDir := policy.NodeTargetPath(targetPath, targetNodeName)

		// 去重
		key := oldDir + " -> " + newDir
		if _, done := processed[key]; done {
			results = append(results, DataMigrateItem{
				PolicyID: p.ID, PolicyName: p.Name,
				Status: "skipped", Message: "已与其他策略合并迁移",
			})
			continue
		}
		processed[key] = struct{}{}

		// 检查源目录是否存在
		info, err := os.Stat(oldDir)
		if err != nil || !info.IsDir() {
			results = append(results, DataMigrateItem{
				PolicyID: p.ID, PolicyName: p.Name,
				Status: "skipped", Message: fmt.Sprintf("源备份目录不存在: %s", oldDir),
			})
			continue
		}

		// 使用 rsync -a 复制目录内容（增量、保留属性）
		policyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		// 确保 oldDir 以 / 结尾（rsync 语义：复制目录内容而非目录本身）
		cmd := exec.CommandContext(policyCtx, "rsync", "-a", oldDir+"/", newDir+"/")
		output, copyErr := cmd.CombinedOutput()
		cancel()

		if copyErr != nil {
			log.Printf("rsync copy failed [policy=%d]: %s — %s", p.ID, copyErr.Error(), string(output))
			results = append(results, DataMigrateItem{
				PolicyID: p.ID, PolicyName: p.Name,
				Status: "error", Message: fmt.Sprintf("复制失败: %s", copyErr.Error()),
			})
		} else {
			results = append(results, DataMigrateItem{
				PolicyID: p.ID, PolicyName: p.Name,
				Status: "copied", Message: fmt.Sprintf("%s → %s", oldDir, newDir),
			})
		}
	}

	return results
}

// truncateOutput 截断输出到指定长度（UTF-8 安全）
func truncateOutput(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// replaceLastOccurrence 替换字符串中最后一次出现的 old 为 new。
func replaceLastOccurrence(s, old, new string) string {
	idx := strings.LastIndex(s, old)
	if idx < 0 {
		return s
	}
	return s[:idx] + new + s[idx+len(old):]
}
