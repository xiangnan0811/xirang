package handlers

import (
	"log"
	"net/http"
	"time"

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
	since24h := time.Now().Add(-24 * time.Hour)

	type overviewCounts struct {
		TotalNodes     int64
		HealthyNodes   int64
		ActivePolicies int64
		RunningTasks   int64
		FailedTasks    int64
	}

	var counts overviewCounts
	row := h.db.Raw(`
		SELECT
			(SELECT COUNT(*) FROM nodes) AS total_nodes,
			(SELECT COUNT(*) FROM nodes WHERE status = 'online') AS healthy_nodes,
			(SELECT COUNT(*) FROM policies WHERE enabled = true) AS active_policies,
			(SELECT COUNT(*) FROM tasks WHERE status = ?) AS running_tasks,
			(SELECT COUNT(*) FROM tasks WHERE status = ? AND created_at >= ?) AS failed_tasks
	`, string(task.StatusRunning), string(task.StatusFailed), since24h).Row()

	if err := row.Scan(&counts.TotalNodes, &counts.HealthyNodes, &counts.ActivePolicies, &counts.RunningTasks, &counts.FailedTasks); err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	c.Header("Cache-Control", "public, max-age=30")
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"totalNodes":     counts.TotalNodes,
		"healthyNodes":   counts.HealthyNodes,
		"activePolicies": counts.ActivePolicies,
		"runningTasks":   counts.RunningTasks,
		"failedTasks24h": counts.FailedTasks,
	}})
}
