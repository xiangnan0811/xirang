package handlers

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	staleHours := 48
	if v := os.Getenv("BACKUP_STALE_THRESHOLD_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			staleHours = n
		}
	}
	staleThreshold := now.Add(-time.Duration(staleHours) * time.Hour)

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

	// 2. 降级策略：最近 3 次 task_run 全部失败的策略（单次查询替代 N+1）
	type degradedPolicy struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	type policyRunInfo struct {
		PolicyID   uint   `gorm:"column:policy_id"`
		PolicyName string `gorm:"column:policy_name"`
		Status     string `gorm:"column:status"`
	}
	var runInfos []policyRunInfo
	if err := h.db.Raw(`
		SELECT t.policy_id AS policy_id, p.name AS policy_name, tr.status AS status
		FROM task_runs tr
		JOIN tasks t ON t.id = tr.task_id
		JOIN policies p ON p.id = t.policy_id
		WHERE p.enabled = 1
		ORDER BY t.policy_id, tr.created_at DESC
	`).Scan(&runInfos).Error; err != nil {
		runInfos = nil
	}

	var degradedPolicies []degradedPolicy
	policyRuns := make(map[uint][]string)
	policyNames := make(map[uint]string)
	for _, ri := range runInfos {
		if len(policyRuns[ri.PolicyID]) < 3 {
			policyRuns[ri.PolicyID] = append(policyRuns[ri.PolicyID], ri.Status)
			policyNames[ri.PolicyID] = ri.PolicyName
		}
	}
	for pid, statuses := range policyRuns {
		if len(statuses) < 3 {
			continue
		}
		allFailed := true
		for _, s := range statuses {
			if s != "failed" {
				allFailed = false
				break
			}
		}
		if allFailed {
			degradedPolicies = append(degradedPolicies, degradedPolicy{ID: pid, Name: policyNames[pid]})
		}
	}

	// 3. 7 天趋势：按日期分组统计（SQL 聚合替代全量加载）
	type trendPoint struct {
		Date    string `json:"date"`
		Total   int    `json:"total"`
		Success int    `json:"success"`
	}
	loc := now.Location()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	trendStart := startOfToday.AddDate(0, 0, -6)
	trendEnd := startOfToday.AddDate(0, 0, 1)

	trendMap := make(map[string]*trendPoint)
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		trendMap[d] = &trendPoint{Date: d}
	}
	type trendRow struct {
		Day    string `gorm:"column:day"`
		Status string `gorm:"column:status"`
		Cnt    int    `gorm:"column:cnt"`
	}
	caseBranches := make([]string, 0, 7)
	args := make([]interface{}, 0, 23)
	for i := 6; i >= 0; i-- {
		dayStart := startOfToday.AddDate(0, 0, -i)
		dayEnd := dayStart.Add(24 * time.Hour)
		caseBranches = append(caseBranches, "WHEN created_at >= ? AND created_at < ? THEN ?")
		args = append(args, dayStart, dayEnd, dayStart.Format("2006-01-02"))
	}
	caseExpr := "CASE " + strings.Join(caseBranches, " ") + " END"
	args = append(args, trendStart, trendEnd)
	query := fmt.Sprintf(`
		SELECT %s AS day, status, COUNT(*) AS cnt
		FROM task_runs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY day, status
	`, caseExpr)
	var rows []trendRow
	if err := h.db.Raw(query, args...).Scan(&rows).Error; err == nil {
		for _, item := range rows {
			tp, ok := trendMap[item.Day]
			if !ok {
				continue
			}
			tp.Total += item.Cnt
			if item.Status == "success" {
				tp.Success += item.Cnt
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
