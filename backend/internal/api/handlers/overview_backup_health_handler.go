package handlers

import (
	"fmt"
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

// Get godoc
// @Summary      获取备份健康状态
// @Description  返回过期节点、降级策略、7 天趋势及汇总统计（operator 仅统计自己拥有的节点）
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /overview/backup-health [get]
func (h *BackupHealthHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")

	now := time.Now()
	staleHours := 48
	if v := os.Getenv("BACKUP_STALE_THRESHOLD_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			staleHours = n
		}
	}
	staleThreshold := now.Add(-time.Duration(staleHours) * time.Hour)

	ownedIDs, needFilter, ownErr := ownershipNodeFilter(c, h.db)
	if ownErr != nil {
		respondInternalError(c, ownErr)
		return
	}
	// Operator with zero owned nodes: empty fleet view.
	if needFilter && len(ownedIDs) == 0 {
		respondOK(c, emptyBackupHealth(now))
		return
	}

	// 1. 备份过期节点：从未备份或最后备份超过 48 小时
	type staleNode struct {
		ID           uint       `json:"id"`
		Name         string     `json:"name"`
		LastBackupAt *time.Time `json:"last_backup_at"`
	}
	var staleNodes []staleNode
	staleQ := h.db.Model(&model.Node{}).
		Select("id, name, last_backup_at").
		Where("last_backup_at IS NULL OR last_backup_at < ?", staleThreshold)
	if needFilter {
		staleQ = staleQ.Where("id IN ?", ownedIDs)
	}
	if err := staleQ.Find(&staleNodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	// 2. 降级策略：最近 3 次 task_run 全部失败的策略
	// Operator: only tasks on owned nodes (union: policy visible if any owned node run).
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
	// Use bound boolean (not = 1) so PostgreSQL accepts the predicate.
	degradedSQL := `
		SELECT t.policy_id AS policy_id, p.name AS policy_name, tr.status AS status
		FROM task_runs tr
		JOIN tasks t ON t.id = tr.task_id
		JOIN policies p ON p.id = t.policy_id
		WHERE p.enabled = ?`
	degradedArgs := []any{true}
	if needFilter {
		degradedSQL += ` AND t.node_id IN ?`
		degradedArgs = append(degradedArgs, ownedIDs)
	}
	degradedSQL += `
		ORDER BY t.policy_id, tr.created_at DESC`
	if err := h.db.Raw(degradedSQL, degradedArgs...).Scan(&runInfos).Error; err != nil {
		respondInternalError(c, err)
		return
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

	// 3. 7 天趋势：按日期分组统计（operator 仅 owned 节点上的 task_runs）
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
	args := make([]interface{}, 0, 24)
	for i := 6; i >= 0; i-- {
		dayStart := startOfToday.AddDate(0, 0, -i)
		dayEnd := dayStart.Add(24 * time.Hour)
		caseBranches = append(caseBranches, "WHEN tr.created_at >= ? AND tr.created_at < ? THEN ?")
		args = append(args, dayStart, dayEnd, dayStart.Format("2006-01-02"))
	}
	caseExpr := "CASE " + strings.Join(caseBranches, " ") + " END"
	args = append(args, trendStart, trendEnd)
	trendSQL := fmt.Sprintf(`
		SELECT %s AS day, tr.status AS status, COUNT(*) AS cnt
		FROM task_runs tr
		JOIN tasks t ON t.id = tr.task_id
		WHERE tr.created_at >= ? AND tr.created_at < ?`, caseExpr)
	if needFilter {
		trendSQL += ` AND t.node_id IN ?`
		args = append(args, ownedIDs)
	}
	trendSQL += `
		GROUP BY day, tr.status`
	var rows []trendRow
	if err := h.db.Raw(trendSQL, args...).Scan(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}
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
	trend := make([]trendPoint, 0, 7)
	for i := 6; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		if tp, ok := trendMap[d]; ok {
			trend = append(trend, *tp)
		}
	}

	// 4. 汇总统计（scoped）
	var totalNodes int64
	nodeQ := h.db.Model(&model.Node{})
	if needFilter {
		nodeQ = nodeQ.Where("id IN ?", ownedIDs)
	}
	if err := nodeQ.Count(&totalNodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var totalPolicies int64
	var policyCountErr error
	if needFilter {
		policyCountErr = h.db.Model(&model.Policy{}).
			Where("enabled = ? AND id IN (SELECT policy_id FROM policy_nodes WHERE node_id IN ?)", true, ownedIDs).
			Count(&totalPolicies).Error
	} else {
		policyCountErr = h.db.Model(&model.Policy{}).Where("enabled = ?", true).Count(&totalPolicies).Error
	}
	if policyCountErr != nil {
		respondInternalError(c, policyCountErr)
		return
	}

	if staleNodes == nil {
		staleNodes = []staleNode{}
	}
	if degradedPolicies == nil {
		degradedPolicies = []degradedPolicy{}
	}

	respondOK(c, gin.H{
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
	})
}

func emptyBackupHealth(now time.Time) gin.H {
	trend := make([]gin.H, 0, 7)
	for i := 6; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		trend = append(trend, gin.H{"date": d, "total": 0, "success": 0})
	}
	return gin.H{
		"stale_nodes":       []any{},
		"stale_node_count":  0,
		"degraded_policies": []any{},
		"degraded_count":    0,
		"trend":             trend,
		"summary": gin.H{
			"total_nodes":    0,
			"total_policies": 0,
			"healthy_nodes":  0,
		},
		"generated_at": now.Format(time.RFC3339),
	}
}
