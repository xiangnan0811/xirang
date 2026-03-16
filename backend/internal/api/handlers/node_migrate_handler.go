package handlers

import (
	"net/http"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NodeMigrateRequest 节点迁移请求
type NodeMigrateRequest struct {
	TargetNodeID uint `json:"targetNodeId" binding:"required"`
}

// Migrate 将源节点的策略和任务迁移到目标节点
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

	// 校验目标节点存在
	var target model.Node
	if err := h.db.First(&target, req.TargetNodeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "目标节点不存在"})
		return
	}

	migratedPolicies := 0
	migratedTasks := 0

	err := h.db.Transaction(func(tx *gorm.DB) error {
		// 迁移策略关联
		var policyNodes []model.PolicyNode
		if err := tx.Where("node_id = ?", sourceID).Find(&policyNodes).Error; err != nil {
			return err
		}

		for _, pn := range policyNodes {
			var exists int64
			tx.Model(&model.PolicyNode{}).Where("policy_id = ? AND node_id = ?", pn.PolicyID, req.TargetNodeID).Count(&exists)
			if exists == 0 {
				newPN := model.PolicyNode{PolicyID: pn.PolicyID, NodeID: req.TargetNodeID}
				if err := tx.Create(&newPN).Error; err != nil {
					return err
				}
				migratedPolicies++
			}
		}
		if err := tx.Where("node_id = ?", sourceID).Delete(&model.PolicyNode{}).Error; err != nil {
			return err
		}

		// 迁移策略生成的任务
		result := tx.Model(&model.Task{}).Where("node_id = ? AND source = ?", sourceID, "policy").
			Update("node_id", req.TargetNodeID)
		if result.Error != nil {
			return result.Error
		}
		migratedTasks = int(result.RowsAffected)

		return nil
	})

	if err != nil {
		respondInternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"migratedPolicies": migratedPolicies,
			"migratedTasks":    migratedTasks,
		},
	})
}
