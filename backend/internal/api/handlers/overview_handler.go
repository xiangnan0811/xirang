package handlers

import (
	"math"
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

// Get godoc
// @Summary      获取总览数据
// @Description  返回节点数、活跃策略数、运行中/失败任务数及当前吞吐量
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /overview [get]
func (h *OverviewHandler) Get(c *gin.Context) {
	since24h := time.Now().UTC().Add(-24 * time.Hour)

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
		respondInternalError(c, err)
		return
	}

	// 聚合当前吞吐：每个 running 任务取最近 60s 内最新采样，求和
	var currentThroughput float64
	cutoff := time.Now().UTC().Add(-60 * time.Second)
	throughputRow := h.db.Raw(`
		SELECT COALESCE(SUM(t.throughput_mbps), 0)
		FROM task_traffic_samples t
		INNER JOIN (
			SELECT s.task_id, MAX(s.id) AS max_id
			FROM task_traffic_samples s
			INNER JOIN tasks ON tasks.id = s.task_id
				AND tasks.status = ?
				AND tasks.last_run_at IS NOT NULL
				AND s.run_started_at = tasks.last_run_at
			WHERE s.sampled_at >= ?
			GROUP BY s.task_id
		) latest ON t.id = latest.max_id
	`, string(task.StatusRunning), cutoff).Row()
	if throughputRow != nil {
		_ = throughputRow.Scan(&currentThroughput)
	}

	c.Header("Cache-Control", "public, max-age=30")
	respondOK(c, gin.H{
		"totalNodes":            counts.TotalNodes,
		"healthyNodes":          counts.HealthyNodes,
		"activePolicies":        counts.ActivePolicies,
		"runningTasks":          counts.RunningTasks,
		"failedTasks24h":        counts.FailedTasks,
		"currentThroughputMbps": math.Round(currentThroughput*10) / 10,
	})
}
