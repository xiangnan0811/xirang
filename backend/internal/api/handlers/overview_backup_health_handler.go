package handlers

import (
	"net/http"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type BackupHealthHandler struct {
	db *gorm.DB
}

func NewBackupHealthHandler(db *gorm.DB) *BackupHealthHandler {
	return &BackupHealthHandler{db: db}
}

func (h *BackupHealthHandler) Get(c *gin.Context) {
	now := time.Now()
	staleThreshold := now.Add(-48 * time.Hour)

	// 1. 备份过期节点：从未备份或最后备份超过 48 小时
	type staleNode struct {
		ID           uint       `json:"id"`
		Name         string     `json:"name"`
		LastBackupAt *time.Time `json:"last_backup_at"`
	}
	var staleNodes []staleNode
	h.db.Model(&model.Node{}).
		Select("id, name, last_backup_at").
		Where("last_backup_at IS NULL OR last_backup_at < ?", staleThreshold).
		Find(&staleNodes)

	// 2. 降级策略：最近 3 次 task_run 全部失败的策略
	type degradedPolicy struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	var policies []model.Policy
	h.db.Where("enabled = ?", true).Find(&policies)

	var degradedPolicies []degradedPolicy
	for _, p := range policies {
		var recentRuns []model.TaskRun
		h.db.Joins("JOIN tasks ON tasks.id = task_runs.task_id").
			Where("tasks.policy_id = ?", p.ID).
			Order("task_runs.created_at DESC").
			Limit(3).
			Find(&recentRuns)

		if len(recentRuns) < 3 {
			continue
		}
		allFailed := true
		for _, r := range recentRuns {
			if r.Status != "failed" {
				allFailed = false
				break
			}
		}
		if allFailed {
			degradedPolicies = append(degradedPolicies, degradedPolicy{ID: p.ID, Name: p.Name})
		}
	}

	// 3. 7 天趋势：按日期分组统计 task_run 总数和成功数
	type trendPoint struct {
		Date    string `json:"date"`
		Total   int    `json:"total"`
		Success int    `json:"success"`
	}
	sevenDaysAgo := now.AddDate(0, 0, -7)
	var runs []model.TaskRun
	h.db.Where("created_at >= ?", sevenDaysAgo).Find(&runs)

	trendMap := make(map[string]*trendPoint)
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		trendMap[d] = &trendPoint{Date: d}
	}
	for _, r := range runs {
		d := r.CreatedAt.Format("2006-01-02")
		if tp, ok := trendMap[d]; ok {
			tp.Total++
			if r.Status == "success" {
				tp.Success++
			}
		}
	}
	trend := make([]trendPoint, 0, 7)
	for i := 6; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		if tp, ok := trendMap[d]; ok {
			trend = append(trend, *tp)
		}
	}

	// 4. 汇总统计
	var totalNodes int64
	h.db.Model(&model.Node{}).Count(&totalNodes)
	var totalPolicies int64
	h.db.Model(&model.Policy{}).Where("enabled = ?", true).Count(&totalPolicies)

	if staleNodes == nil {
		staleNodes = []staleNode{}
	}
	if degradedPolicies == nil {
		degradedPolicies = []degradedPolicy{}
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"stale_nodes":       staleNodes,
		"stale_node_count":  len(staleNodes),
		"degraded_policies": degradedPolicies,
		"degraded_count":    len(degradedPolicies),
		"trend":             trend,
		"summary": gin.H{
			"total_nodes":    totalNodes,
			"total_policies": totalPolicies,
			"healthy_nodes":  totalNodes - int64(len(staleNodes)),
		},
		"generated_at": now.Format(time.RFC3339),
	}})
}
