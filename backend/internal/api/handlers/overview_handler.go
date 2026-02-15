package handlers

import (
	"net/http"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type OverviewHandler struct {
	db *gorm.DB
}

func NewOverviewHandler(db *gorm.DB) *OverviewHandler {
	return &OverviewHandler{db: db}
}

func (h *OverviewHandler) Get(c *gin.Context) {
	var totalNodes int64
	var healthyNodes int64
	var activePolicies int64
	var runningTasks int64
	var failedTasks int64

	if err := h.db.Model(&model.Node{}).Count(&totalNodes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := h.db.Model(&model.Node{}).Where("status = ?", "online").Count(&healthyNodes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := h.db.Model(&model.Policy{}).Where("enabled = ?", true).Count(&activePolicies).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := h.db.Model(&model.Task{}).Where("status = ?", string(task.StatusRunning)).Count(&runningTasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := h.db.Model(&model.Task{}).Where("status = ?", string(task.StatusFailed)).Count(&failedTasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"totalNodes":     totalNodes,
		"healthyNodes":   healthyNodes,
		"activePolicies": activePolicies,
		"runningTasks":   runningTasks,
		"failedTasks24h": failedTasks,
	}})
}
