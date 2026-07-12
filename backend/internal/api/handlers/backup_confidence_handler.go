package handlers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	confidenceStatusHealthy      = "healthy"
	confidenceStatusWarning      = "warning"
	confidenceStatusAtRisk       = "at_risk"
	confidenceStatusInsufficient = "insufficient"
)

type BackupConfidenceHandler struct {
	db *gorm.DB
}

func NewBackupConfidenceHandler(db *gorm.DB) *BackupConfidenceHandler {
	return &BackupConfidenceHandler{db: db}
}

type backupConfidenceSummary struct {
	Healthy      int `json:"healthy"`
	Warning      int `json:"warning"`
	AtRisk       int `json:"at_risk"`
	Insufficient int `json:"insufficient"`
	Total        int `json:"total"`
}

type backupConfidenceResponse struct {
	GeneratedAt time.Time               `json:"generated_at"`
	Summary     backupConfidenceSummary `json:"summary"`
	Items       []backupConfidenceItem  `json:"items"`
}

type backupConfidenceItem struct {
	ID         string                     `json:"id"`
	Scope      string                     `json:"scope"`
	PolicyID   uint                       `json:"policy_id,omitempty"`
	PolicyName string                     `json:"policy_name,omitempty"`
	NodeID     uint                       `json:"node_id,omitempty"`
	NodeName   string                     `json:"node_name,omitempty"`
	Status     string                     `json:"status"`
	Score      int                        `json:"score"`
	Reasons    []backupConfidenceReason   `json:"reasons"`
	Evidence   []backupConfidenceEvidence `json:"evidence"`
	NextSteps  []backupConfidenceNextStep `json:"next_steps"`
	Targets    []backupConfidenceTarget   `json:"targets,omitempty"`
}

type backupConfidenceTarget struct {
	NodeID       uint       `json:"node_id"`
	NodeName     string     `json:"node_name"`
	LastBackupAt *time.Time `json:"last_backup_at,omitempty"`
}

type backupConfidenceReason struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type backupConfidenceEvidence struct {
	Type       string     `json:"type"`
	Status     string     `json:"status"`
	Message    string     `json:"message"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	TaskID     uint       `json:"task_id,omitempty"`
	TaskRunID  uint       `json:"task_run_id,omitempty"`
	AlertID    uint       `json:"alert_id,omitempty"`
}

type backupConfidenceNextStep struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type confidencePolicyContext struct {
	Policy                    model.Policy
	Tasks                     []model.Task
	LatestRun                 *model.TaskRun
	LatestBackupRun           *model.TaskRun
	LatestSuccessfulBackupRun *model.TaskRun
	LatestDrill               *model.RestoreDrillEvidence
	OpenAlerts                []model.Alert
	Targets                   []backupConfidenceTarget
}

// Get godoc
// @Summary      获取备份可信度
// @Description  聚合策略、节点、执行记录、恢复演练、校验、RPO/RTO 和告警证据，返回可解释的备份可信度
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /overview/backup-confidence [get]
func (h *BackupConfidenceHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")

	now := time.Now()
	policies, err := h.loadVisiblePolicies(c)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	items := make([]backupConfidenceItem, 0, len(policies))
	for _, policy := range policies {
		ctx, err := h.loadPolicyContext(c, policy)
		if err != nil {
			respondInternalError(c, err)
			return
		}
		items = append(items, buildBackupConfidenceItem(now, ctx))
	}

	summary := backupConfidenceSummary{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case confidenceStatusHealthy:
			summary.Healthy++
		case confidenceStatusWarning:
			summary.Warning++
		case confidenceStatusAtRisk:
			summary.AtRisk++
		case confidenceStatusInsufficient:
			summary.Insufficient++
		}
	}

	respondOK(c, backupConfidenceResponse{
		GeneratedAt: now,
		Summary:     summary,
		Items:       items,
	})
}

func (h *BackupConfidenceHandler) loadVisiblePolicies(c *gin.Context) ([]model.Policy, error) {
	query := h.db.WithContext(c.Request.Context()).Where("enabled = ? AND is_template = ?", true, false).Order("id asc")
	if nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db); err != nil {
		return nil, err
	} else if needFilter {
		if len(nodeIDs) == 0 {
			return []model.Policy{}, nil
		}
		query = query.Where("id IN (SELECT policy_id FROM policy_nodes WHERE node_id IN ?)", nodeIDs)
		query = query.Preload("Nodes", "id IN ?", nodeIDs)
	} else {
		query = query.Preload("Nodes")
	}

	var policies []model.Policy
	if err := query.Find(&policies).Error; err != nil {
		return nil, err
	}
	return policies, nil
}

func (h *BackupConfidenceHandler) loadPolicyContext(c *gin.Context, policy model.Policy) (confidencePolicyContext, error) {
	ctx := confidencePolicyContext{Policy: policy}
	targetNodeIDs := make([]uint, 0, len(policy.Nodes))
	if len(policy.Nodes) > 0 {
		ctx.Targets = make([]backupConfidenceTarget, 0, len(policy.Nodes))
		for _, node := range policy.Nodes {
			ctx.Targets = append(ctx.Targets, backupConfidenceTarget{NodeID: node.ID, NodeName: node.Name, LastBackupAt: node.LastBackupAt})
			targetNodeIDs = append(targetNodeIDs, node.ID)
		}
	}

	var tasks []model.Task
	taskQuery := h.db.WithContext(c.Request.Context()).
		Select("id", "name", "node_id", "policy_id", "status", "last_run_at", "last_error", "verify_status", "enabled").
		Where("policy_id = ?", policy.ID).
		Order("id asc")
	if len(targetNodeIDs) > 0 {
		taskQuery = taskQuery.Where("node_id IN ?", targetNodeIDs)
	}
	if err := taskQuery.Find(&tasks).Error; err != nil {
		return ctx, err
	}
	ctx.Tasks = tasks

	taskIDs := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	if len(taskIDs) > 0 {
		latestRun, err := h.loadLatestRun(c, taskIDs, "")
		if err != nil {
			return ctx, err
		}
		ctx.LatestRun = latestRun

		latestBackupRun, err := h.loadLatestRun(c, taskIDs, "trigger_type NOT IN ?", []string{"restore", "drill"})
		if err != nil {
			return ctx, err
		}
		ctx.LatestBackupRun = latestBackupRun

		latestSuccessfulBackupRun, err := h.loadLatestRun(c, taskIDs, "trigger_type NOT IN ? AND status = ?", []string{"restore", "drill"}, "success")
		if err != nil {
			return ctx, err
		}
		ctx.LatestSuccessfulBackupRun = latestSuccessfulBackupRun
	}

	// Only attach drill evidence when the operator can see both ends:
	// source task (via visible taskIDs) AND sandbox node (owned set).
	// No visible tasks → no policy-wide fallback (would leak unowned drills).
	if len(taskIDs) > 0 {
		ownedIDs, needFilter, ownErr := ownershipNodeFilter(c, h.db)
		if ownErr != nil {
			return ctx, ownErr
		}
		drillQuery := h.db.WithContext(c.Request.Context()).
			Where("policy_id = ? AND task_id IN ?", policy.ID, taskIDs)
		if needFilter {
			if len(ownedIDs) == 0 {
				// No owned nodes → no sandbox authorization.
			} else {
				drillQuery = drillQuery.Where("sandbox_node_id IN ?", ownedIDs)
			}
		}
		if !needFilter || len(ownedIDs) > 0 {
			var latestDrill model.RestoreDrillEvidence
			err := drillQuery.Order("created_at desc, id desc").First(&latestDrill).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return ctx, err
			}
			if err == nil {
				ctx.LatestDrill = &latestDrill
			}
		}
	}

	alerts, err := h.loadOpenAlerts(c, policy, taskIDs, targetNodeIDs)
	if err != nil {
		return ctx, err
	}
	ctx.OpenAlerts = alerts

	return ctx, nil
}

func (h *BackupConfidenceHandler) loadLatestRun(c *gin.Context, taskIDs []uint, extraWhere string, args ...interface{}) (*model.TaskRun, error) {
	var run model.TaskRun
	query := h.db.WithContext(c.Request.Context()).Where("task_id IN ?", taskIDs)
	if strings.TrimSpace(extraWhere) != "" {
		query = query.Where(extraWhere, args...)
	}
	err := query.Order("created_at desc, id desc").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (h *BackupConfidenceHandler) loadOpenAlerts(c *gin.Context, policy model.Policy, taskIDs []uint, targetNodeIDs []uint) ([]model.Alert, error) {
	query := h.db.WithContext(c.Request.Context()).Model(&model.Alert{}).Where("status != ?", "resolved")
	if len(targetNodeIDs) == 0 {
		return []model.Alert{}, nil
	}
	query = query.Where("node_id IN ?", targetNodeIDs)
	if len(taskIDs) > 0 {
		query = query.Where("task_id IN ? OR policy_name = ? OR error_code = ?", taskIDs, policy.Name, fmt.Sprintf("XR-INTG-%d", policy.ID))
	} else {
		query = query.Where("policy_name = ? OR error_code = ?", policy.Name, fmt.Sprintf("XR-INTG-%d", policy.ID))
	}
	var alerts []model.Alert
	if err := query.Order("triggered_at desc, id desc").Limit(5).Find(&alerts).Error; err != nil {
		return nil, err
	}
	return alerts, nil
}

func buildBackupConfidenceItem(now time.Time, ctx confidencePolicyContext) backupConfidenceItem {
	item := backupConfidenceItem{
		ID:         fmt.Sprintf("policy-%d", ctx.Policy.ID),
		Scope:      "policy",
		PolicyID:   ctx.Policy.ID,
		PolicyName: ctx.Policy.Name,
		Score:      100,
		Targets:    ctx.Targets,
		Reasons:    []backupConfidenceReason{},
		Evidence:   []backupConfidenceEvidence{},
		NextSteps:  []backupConfidenceNextStep{},
	}
	if len(ctx.Targets) == 1 {
		item.NodeID = ctx.Targets[0].NodeID
		item.NodeName = ctx.Targets[0].NodeName
	}

	if len(ctx.Tasks) == 0 {
		item.addFinding("no_task", "critical", "策略尚未关联可执行任务", 35, "create_task", "为该策略创建或关联备份任务")
	}

	if ctx.LatestBackupRun == nil {
		item.addFinding("no_successful_backup", "critical", "尚未找到该策略的备份执行证据", 30, "run_backup", "立即执行一次备份任务")
		item.Evidence = append(item.Evidence, backupConfidenceEvidence{Type: "backup", Status: "missing", Message: "没有可用的备份 TaskRun 记录"})
	} else {
		item.Evidence = append(item.Evidence, backupConfidenceEvidence{
			Type:       "backup",
			Status:     ctx.LatestBackupRun.Status,
			Message:    buildRunEvidenceMessage(ctx.LatestBackupRun, "最近备份执行"),
			ObservedAt: preferredRunTime(ctx.LatestBackupRun),
			TaskID:     ctx.LatestBackupRun.TaskID,
			TaskRunID:  ctx.LatestBackupRun.ID,
		})
		if ctx.LatestBackupRun.Status == "failed" || ctx.LatestBackupRun.Status == "canceled" {
			item.addFinding("recent_backup_failed", "critical", "最近一次备份未成功", 35, "rerun_backup", "修复失败原因后重新执行备份")
		} else if ctx.LatestBackupRun.Status != "success" {
			item.addFinding("backup_not_completed", "warning", "最近一次备份尚未完成", 12, "check_running_backup", "确认备份任务是否仍在执行")
		}
		if ctx.LatestBackupRun.VerifyStatus == "failed" {
			item.addFinding("verify_failed", "critical", "最近备份校验失败", 25, "inspect_verify", "查看校验日志并修复源/目标差异")
		} else if ctx.LatestBackupRun.VerifyStatus == "warning" {
			item.addFinding("verify_warning", "warning", "最近备份校验存在告警", 12, "inspect_verify", "查看校验告警并确认样本一致性")
		} else if ctx.Policy.VerifyEnabled && ctx.LatestBackupRun.VerifyStatus == "none" {
			item.addFinding("verify_missing", "warning", "策略已启用校验但最近备份没有校验证据", 8, "enable_verify", "确认备份校验配置与执行日志")
		}
	}

	if ctx.LatestRun != nil && (ctx.LatestBackupRun == nil || ctx.LatestRun.ID != ctx.LatestBackupRun.ID) && (ctx.LatestRun.Status == "failed" || ctx.LatestRun.Status == "canceled") {
		severity := "warning"
		penalty := 12
		if ctx.LatestRun.TriggerType == "drill" {
			severity = "critical"
			penalty = 24
		}
		item.addFinding("recent_run_failed", severity, "最近一次任务执行未成功", penalty, "inspect_latest_run", "查看最近一次任务执行日志")
	}

	if ctx.Policy.RPOMinutes > 0 {
		if ctx.LatestSuccessfulBackupRun == nil {
			item.addFinding("rpo_unknown", "warning", "缺少成功备份，无法证明 RPO 达标", 14, "run_backup", "先完成一次成功备份以恢复 RPO 证据")
		} else if observedAt := preferredRunTime(ctx.LatestSuccessfulBackupRun); observedAt != nil {
			actualMinutes := int(now.Sub(*observedAt).Minutes())
			item.Evidence = append(item.Evidence, backupConfidenceEvidence{
				Type:       "rpo",
				Status:     "observed",
				Message:    fmt.Sprintf("最近成功备份距今约 %d 分钟，目标 RPO 为 %d 分钟", actualMinutes, ctx.Policy.RPOMinutes),
				ObservedAt: observedAt,
				TaskID:     ctx.LatestSuccessfulBackupRun.TaskID,
				TaskRunID:  ctx.LatestSuccessfulBackupRun.ID,
			})
			if actualMinutes > ctx.Policy.RPOMinutes {
				item.addFinding("rpo_exceeded", "critical", fmt.Sprintf("RPO 超限：最近成功备份距今约 %d 分钟，目标 %d 分钟", actualMinutes, ctx.Policy.RPOMinutes), 30, "run_backup", "立即执行备份并检查调度是否正常")
			}
		}
	}

	if ctx.LatestDrill == nil {
		item.addFinding("drill_missing", "warning", "缺少恢复演练证据，不能证明备份可恢复", 28, "run_restore_drill", "配置并执行一次恢复演练")
		item.Evidence = append(item.Evidence, backupConfidenceEvidence{Type: "drill", Status: "missing", Message: "没有结构化恢复演练证据"})
	} else {
		item.Evidence = append(item.Evidence, backupConfidenceEvidence{
			Type:       "drill",
			Status:     ctx.LatestDrill.Status,
			Message:    buildDrillEvidenceMessage(ctx.LatestDrill),
			ObservedAt: preferredDrillTime(ctx.LatestDrill),
			TaskID:     ctx.LatestDrill.TaskID,
			TaskRunID:  ctx.LatestDrill.TaskRunID,
		})
		if !ctx.LatestDrill.ConfidenceEligible {
			if ctx.LatestDrill.Status == "failed" {
				item.addFinding("drill_failed", "critical", drillFailureMessage(ctx.LatestDrill), 35, "rerun_restore_drill", "修复恢复演练失败原因后重新执行")
			} else {
				item.addFinding("drill_not_confident", "warning", "最近恢复演练不能作为可信恢复证据", 24, "rerun_restore_drill", "重新执行一次完整恢复演练")
			}
		}
	}

	for _, alert := range ctx.OpenAlerts {
		item.Evidence = append(item.Evidence, backupConfidenceEvidence{
			Type:       "alert",
			Status:     alert.Severity,
			Message:    sanitizeConfidenceMessage(alert.Message),
			ObservedAt: &alert.TriggeredAt,
			TaskRunID:  uintFromPtr(alert.TaskRunID),
			AlertID:    alert.ID,
		})
		if strings.HasPrefix(alert.ErrorCode, "XR-INTG-") {
			item.addFinding("integrity_alert", "critical", "存在未解决的完整性校验告警", 28, "resolve_integrity_alert", "处理完整性校验告警并重新校验")
		} else if strings.HasPrefix(alert.ErrorCode, "XR-VRFY-") {
			item.addFinding("verification_alert", "warning", "存在未解决的备份校验告警", 16, "resolve_verify_alert", "查看校验告警并修复差异")
		} else if strings.HasPrefix(alert.ErrorCode, "XR-DRILL-") {
			item.addFinding("drill_alert", "critical", "存在未解决的恢复演练告警", 24, "resolve_drill_alert", "处理恢复演练告警并重新演练")
		}
	}

	item.Status = confidenceStatusFor(item.Score, item.Reasons)
	if item.Status != confidenceStatusHealthy && len(item.NextSteps) == 0 {
		item.NextSteps = append(item.NextSteps, backupConfidenceNextStep{Code: "inspect_evidence", Label: "查看证据并处理异常项"})
	}
	return item
}

func (item *backupConfidenceItem) addFinding(code string, severity string, message string, penalty int, nextCode string, nextLabel string) {
	for _, reason := range item.Reasons {
		if reason.Code == code {
			return
		}
	}
	item.Reasons = append(item.Reasons, backupConfidenceReason{Code: code, Severity: severity, Message: message})
	item.Score -= penalty
	if item.Score < 0 {
		item.Score = 0
	}
	if nextCode != "" {
		for _, step := range item.NextSteps {
			if step.Code == nextCode {
				return
			}
		}
		item.NextSteps = append(item.NextSteps, backupConfidenceNextStep{Code: nextCode, Label: nextLabel})
	}
}

func confidenceStatusFor(score int, reasons []backupConfidenceReason) string {
	hasCritical := false
	hasInsufficient := false
	for _, reason := range reasons {
		if reason.Code == "drill_missing" || reason.Code == "no_successful_backup" || reason.Code == "no_task" {
			hasInsufficient = true
		}
		if reason.Severity == "critical" {
			hasCritical = true
		}
	}
	if hasCritical || score < 60 {
		return confidenceStatusAtRisk
	}
	if hasInsufficient {
		return confidenceStatusInsufficient
	}
	if len(reasons) > 0 || score < 90 {
		return confidenceStatusWarning
	}
	return confidenceStatusHealthy
}

func buildRunEvidenceMessage(run *model.TaskRun, prefix string) string {
	if run == nil {
		return prefix + "缺失"
	}
	parts := []string{prefix + "状态 " + run.Status}
	if run.VerifyStatus != "" && run.VerifyStatus != "none" {
		parts = append(parts, "校验 "+run.VerifyStatus)
	}
	if run.LastError != "" {
		parts = append(parts, "错误已记录")
	}
	return strings.Join(parts, "，")
}

func buildDrillEvidenceMessage(e *model.RestoreDrillEvidence) string {
	if e == nil {
		return "没有恢复演练证据"
	}
	parts := []string{"恢复演练状态 " + e.Status}
	if e.FailedStep != "" {
		parts = append(parts, "失败步骤 "+e.FailedStep)
	}
	if e.ConfidenceEligible {
		parts = append(parts, "可作为恢复信心证据")
	} else {
		parts = append(parts, "不能作为恢复信心证据")
	}
	return strings.Join(parts, "，")
}

func drillFailureMessage(e *model.RestoreDrillEvidence) string {
	if e == nil || e.FailedStep == "" {
		return "最近恢复演练失败"
	}
	return "最近恢复演练失败，失败步骤：" + e.FailedStep
}

var (
	confidenceSensitivePathPattern = regexp.MustCompile(`/(?:etc|usr|bin|sbin|boot|dev|proc|sys|run|var/run)(?:/[^\s，。；;]*)?`)
	confidenceSensitiveKeyPattern  = regexp.MustCompile(`(?i)(authorization|bearer|token|api[_-]?key|secret|password)=\*\*\*`)
)

func sanitizeConfidenceMessage(message string) string {
	message = util.SanitizeMessage(message)
	message = confidenceSensitivePathPattern.ReplaceAllString(message, "<路径已隐藏>")
	message = confidenceSensitiveKeyPattern.ReplaceAllString(message, "敏感信息=***")
	message = strings.NewReplacer(
		"private_key", "敏感信息",
		"PrivateKey", "敏感信息",
		"private key", "敏感信息",
		"Private key", "敏感信息",
		"PRIVATE KEY", "敏感信息",
	).Replace(message)
	return message
}

func preferredRunTime(run *model.TaskRun) *time.Time {
	if run == nil {
		return nil
	}
	if run.FinishedAt != nil {
		return run.FinishedAt
	}
	if run.StartedAt != nil {
		return run.StartedAt
	}
	return &run.CreatedAt
}

func preferredDrillTime(e *model.RestoreDrillEvidence) *time.Time {
	if e == nil {
		return nil
	}
	if e.FinishedAt != nil {
		return e.FinishedAt
	}
	if e.StartedAt != nil {
		return e.StartedAt
	}
	return &e.CreatedAt
}

func uintFromPtr(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}
