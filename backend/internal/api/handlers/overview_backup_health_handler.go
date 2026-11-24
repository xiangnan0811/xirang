package handlers

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"os"
	"strconv"
	"strings"
	"time"
	"xirang/backend/internal/backuphealth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
)

type BackupHealthHandler struct {
	db  *gorm.DB
	now func() time.Time
}

// backupHealthData describes the response data, including the empty fleet view.
type backupHealthData struct {
	StaleNodes             []backupHealthStaleNode      `json:"stale_nodes"`
	StaleNodeCount         int                          `json:"stale_node_count"`
	StaleThresholdHours    int                          `json:"stale_threshold_hours"`
	LatestVerifiedBackupAt *string                      `json:"latest_verified_backup_at" extensions:"x-nullable"`
	DegradedPolicies       []backupHealthDegradedPolicy `json:"degraded_policies"`
	DegradedCount          int                          `json:"degraded_count"`
	Trend                  []backupHealthTrendPoint     `json:"trend"`
	Summary                backupHealthSummary          `json:"summary"`
	GeneratedAt            string                       `json:"generated_at"`
}

type backupHealthStaleNode struct {
	ID           uint       `json:"id"`
	Name         string     `json:"name"`
	LastBackupAt *time.Time `json:"last_backup_at" extensions:"x-nullable"`
}

type backupHealthDegradedPolicy struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type backupHealthTrendPoint struct {
	Date    string `json:"date"`
	Total   int    `json:"total"`
	Success int    `json:"success"`
}

type backupHealthSummary struct {
	TotalNodes    int64 `json:"total_nodes"`
	TotalPolicies int64 `json:"total_policies"`
	HealthyNodes  int64 `json:"healthy_nodes"`
}

func NewBackupHealthHandler(db *gorm.DB) *BackupHealthHandler {
	return &BackupHealthHandler{db: db, now: time.Now}
}

// Get godoc
// @Summary      获取备份健康状态
// @Description  返回过期节点、降级策略、7 天趋势及汇总统计（operator 仅统计自己拥有的节点）
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=backupHealthData}
// @Failure      401  {object}  handlers.Response
// @Router       /overview/backup-health [get]
func (h *BackupHealthHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")

	now := h.now()
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
		respondOK(c, emptyBackupHealth(now, staleHours))
		return
	}

	ctx := c.Request.Context()
	// 1. 备份过期节点：只读取已验证的、分类完成事实。旧的
	// nodes.last_backup_at 会在迁移时保留为 unverified，不能让节点变新鲜。
	var nodes []model.Node
	nodeQ := h.db.WithContext(ctx).Model(&model.Node{}).Select("id, name")
	if needFilter {
		nodeQ = nodeQ.Where("id IN ?", ownedIDs)
	}
	if err := nodeQ.Find(&nodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	nodeIDs := make([]uint, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.ID)
	}
	latestFacts, err := backuphealth.LatestVerifiedForNodes(ctx, h.db, nodeIDs)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	staleNodes := make([]backupHealthStaleNode, 0, len(nodes))
	var latestVerifiedBackupAt *time.Time
	for _, node := range nodes {
		var lastBackupAt *time.Time
		if fact, ok := latestFacts[node.ID]; ok {
			completedAt := fact.CompletedAt.UTC()
			lastBackupAt = &completedAt
			if latestVerifiedBackupAt == nil || completedAt.After(*latestVerifiedBackupAt) {
				ts := completedAt
				latestVerifiedBackupAt = &ts
			}
		}
		if lastBackupAt == nil || lastBackupAt.Before(staleThreshold) {
			staleNodes = append(staleNodes, backupHealthStaleNode{ID: node.ID, Name: node.Name, LastBackupAt: lastBackupAt})
		}
	}

	// 2. 降级策略：最近 3 次 task_run 全部失败的策略
	// Operator: require current policy ownership and owned-node run snapshots.
	type policyRunInfo struct {
		PolicyID   uint   `gorm:"column:policy_id"`
		PolicyName string `gorm:"column:policy_name"`
		Status     string `gorm:"column:status"`
	}
	var runInfos []policyRunInfo
	// Attempts remain classified from immutable TaskRun snapshots for the
	// denominator. A verified completion fact is the only success signal; this
	// also handles a managed commit becoming durable before its TaskRun settles.
	degradedSQL := `
		SELECT t.policy_id AS policy_id, p.name AS policy_name,
			CASE
				WHEN EXISTS (
					SELECT 1 FROM backup_completions bc
					WHERE bc.task_run_id = tr.id AND bc.evidence_status = 'verified'
				) THEN 'success'
				WHEN tr.status = 'success' THEN 'incomplete'
				ELSE tr.status
			END AS status
		FROM task_runs tr
		JOIN tasks t ON t.id = tr.task_id
		JOIN policies p ON p.id = t.policy_id
		WHERE p.enabled = ? AND p.is_template = ? AND ` + backuphealth.ClassifiedAttemptPredicate("tr")
	degradedArgs := []any{true, false}
	if needFilter {
		degradedSQL = `WITH owned_nodes AS (SELECT node_id FROM node_owners WHERE user_id = ?) ` + degradedSQL
		degradedSQL += ` AND tr.node_id_snapshot IN (SELECT node_id FROM owned_nodes) AND EXISTS (SELECT 1 FROM policy_nodes pn WHERE pn.policy_id = p.id AND pn.node_id IN (SELECT node_id FROM owned_nodes))`
		degradedArgs = append([]any{middleware.CurrentUserID(c)}, degradedArgs...)
	}
	degradedSQL += `
		ORDER BY t.policy_id, tr.created_at DESC`
	if err := h.db.WithContext(ctx).Raw(degradedSQL, degradedArgs...).Scan(&runInfos).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	degradedPolicies := make([]backupHealthDegradedPolicy, 0)
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
			degradedPolicies = append(degradedPolicies, backupHealthDegradedPolicy{ID: pid, Name: policyNames[pid]})
		}
	}

	// 3. 7 天趋势：按日期分组统计（operator 仅 owned 节点上的 task_runs）
	loc := now.Location()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	trendStart := startOfToday.AddDate(0, 0, -6)
	trendEnd := startOfToday.AddDate(0, 0, 1)

	trend := make([]backupHealthTrendPoint, 7)
	trendMap := make(map[string]*backupHealthTrendPoint, len(trend))
	for i := range trend {
		day := trendStart.AddDate(0, 0, i).Format("2006-01-02")
		trend[i].Date = day
		trendMap[day] = &trend[i]
	}
	type trendRow struct {
		Day    string `gorm:"column:day"`
		Status string `gorm:"column:status"`
		Cnt    int    `gorm:"column:cnt"`
	}
	// SQLite's UTC cutover stored whole-second text without an offset, while
	// current writes include one. A whole-second bound compares correctly to both.
	boundary := func(t time.Time) any {
		utc := t.UTC()
		if h.db.Name() == "sqlite" {
			return utc.Format("2006-01-02 15:04:05")
		}
		return utc
	}
	caseBranches := make([]string, 0, 7)
	args := make([]interface{}, 0, 24)
	for i := 6; i >= 0; i-- {
		dayStart := startOfToday.AddDate(0, 0, -i)
		dayEnd := dayStart.AddDate(0, 0, 1)
		caseBranches = append(caseBranches, "WHEN tr.created_at >= ? AND tr.created_at < ? THEN ?")
		args = append(args, boundary(dayStart), boundary(dayEnd), dayStart.Format("2006-01-02"))
	}
	caseExpr := "CASE " + strings.Join(caseBranches, " ") + " END"
	args = append(args, boundary(trendStart), boundary(trendEnd))
	trendSQL := fmt.Sprintf(`
		SELECT classified.day, classified.status, COUNT(*) AS cnt
		FROM (
			SELECT %s AS day,
				CASE
					WHEN EXISTS (
						SELECT 1 FROM backup_completions bc
						WHERE bc.task_run_id = tr.id AND bc.evidence_status = 'verified'
					) THEN 'success'
					WHEN tr.status = 'success' THEN 'incomplete'
					ELSE tr.status
				END AS status
			FROM task_runs tr
			WHERE tr.created_at >= ? AND tr.created_at < ? AND %s`,
		caseExpr, backuphealth.ClassifiedAttemptPredicate("tr"))
	if needFilter {
		trendSQL += ` AND tr.node_id_snapshot IN ?`
		args = append(args, ownedIDs)
	}
	trendSQL += `
		) AS classified
		GROUP BY classified.day, classified.status`
	var rows []trendRow
	if err := h.db.WithContext(ctx).Raw(trendSQL, args...).Scan(&rows).Error; err != nil {
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
	// 4. 汇总统计（scoped）
	var totalNodes int64
	nodeCountQ := h.db.WithContext(ctx).Model(&model.Node{})
	if needFilter {
		nodeCountQ = nodeCountQ.Where("id IN ?", ownedIDs)
	}
	if err := nodeCountQ.Count(&totalNodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var totalPolicies int64
	var policyCountErr error
	if needFilter {
		policyCountErr = h.db.WithContext(ctx).Model(&model.Policy{}).
			Where("enabled = ? AND is_template = ? AND id IN (SELECT policy_id FROM policy_nodes WHERE node_id IN ?)", true, false, ownedIDs).
			Count(&totalPolicies).Error
	} else {
		policyCountErr = h.db.WithContext(ctx).Model(&model.Policy{}).Where("enabled = ? AND is_template = ?", true, false).Count(&totalPolicies).Error
	}
	if policyCountErr != nil {
		respondInternalError(c, policyCountErr)
		return
	}

	var latestVerifiedBackupStr *string
	if latestVerifiedBackupAt != nil {
		formatted := latestVerifiedBackupAt.UTC().Format(time.RFC3339)
		latestVerifiedBackupStr = &formatted
	}

	respondOK(c, backupHealthData{
		StaleNodes:             staleNodes,
		StaleNodeCount:         len(staleNodes),
		StaleThresholdHours:    staleHours,
		LatestVerifiedBackupAt: latestVerifiedBackupStr,
		DegradedPolicies:       degradedPolicies,
		DegradedCount:          len(degradedPolicies),
		Trend:                  trend,
		Summary: backupHealthSummary{
			TotalNodes:    totalNodes,
			TotalPolicies: totalPolicies,
			HealthyNodes:  totalNodes - int64(len(staleNodes)),
		},
		GeneratedAt: now.Format(time.RFC3339),
	})
}

func emptyBackupHealth(now time.Time, staleHours int) backupHealthData {
	trend := make([]backupHealthTrendPoint, 0, 7)
	for i := 6; i >= 0; i-- {
		trend = append(trend, backupHealthTrendPoint{Date: now.AddDate(0, 0, -i).Format("2006-01-02")})
	}
	return backupHealthData{
		StaleNodes:          []backupHealthStaleNode{},
		StaleThresholdHours: staleHours,
		DegradedPolicies:    []backupHealthDegradedPolicy{},
		Trend:               trend,
		GeneratedAt:         now.Format(time.RFC3339),
	}
}
