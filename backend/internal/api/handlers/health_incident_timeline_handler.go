package handlers

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	defaultHealthIncidentWindowHours = 72
	maxHealthIncidentWindowHours     = 168
	maxHealthIncidentSourceRows      = 300
	maxHealthIncidentSignalsPerGroup = 5
)

type HealthIncidentTimelineHandler struct {
	db *gorm.DB
}

func NewHealthIncidentTimelineHandler(db *gorm.DB) *HealthIncidentTimelineHandler {
	return &HealthIncidentTimelineHandler{db: db}
}

type healthIncidentTimelineResponse struct {
	GeneratedAt time.Time                     `json:"generated_at"`
	WindowHours int                           `json:"window_hours"`
	Summary     healthIncidentTimelineSummary `json:"summary"`
	Groups      []healthIncidentGroup         `json:"groups"`
}

type healthIncidentTimelineSummary struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

type healthIncidentResource struct {
	Type       string `json:"type"`
	ID         uint   `json:"id,omitempty"`
	Name       string `json:"name"`
	NodeID     uint   `json:"node_id,omitempty"`
	NodeName   string `json:"node_name,omitempty"`
	PolicyID   uint   `json:"policy_id,omitempty"`
	PolicyName string `json:"policy_name,omitempty"`
}

type healthIncidentAction struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Href  string `json:"href"`
}

type healthIncidentSignal struct {
	Type       string    `json:"type"`
	Severity   string    `json:"severity"`
	OccurredAt time.Time `json:"occurred_at"`
	Message    string    `json:"message"`
	AlertID    uint      `json:"alert_id,omitempty"`
	DeliveryID uint      `json:"delivery_id,omitempty"`
	TaskID     uint      `json:"task_id,omitempty"`
	TaskRunID  uint      `json:"task_run_id,omitempty"`
	NodeID     uint      `json:"node_id,omitempty"`
	PolicyID   uint      `json:"policy_id,omitempty"`
}

type healthIncidentGroup struct {
	ID            string                 `json:"id"`
	Severity      string                 `json:"severity"`
	Resource      healthIncidentResource `json:"resource"`
	LastSeenAt    time.Time              `json:"last_seen_at"`
	EventCount    int                    `json:"event_count"`
	LikelyCause   string                 `json:"likely_cause"`
	SourceTypes   []string               `json:"source_types"`
	NextActions   []healthIncidentAction `json:"next_actions"`
	Signals       []healthIncidentSignal `json:"signals"`
	sourceSet     map[string]struct{}    `json:"-"`
	actionSet     map[string]struct{}    `json:"-"`
	causeAt       time.Time              `json:"-"`
	causeSeverity string                 `json:"-"`
}

type healthIncidentAccumulator struct {
	now    time.Time
	groups map[string]*healthIncidentGroup
}

type healthIncidentTaskInfo struct {
	ID         uint   `gorm:"column:id"`
	Name       string `gorm:"column:name"`
	NodeID     uint   `gorm:"column:node_id"`
	NodeName   string `gorm:"column:node_name"`
	PolicyID   *uint  `gorm:"column:policy_id"`
	PolicyName string `gorm:"column:policy_name"`
}

type healthIncidentTaskFailureRow struct {
	TaskRunID  uint       `gorm:"column:task_run_id"`
	TaskID     uint       `gorm:"column:task_id"`
	TaskName   string     `gorm:"column:task_name"`
	NodeID     uint       `gorm:"column:node_id"`
	NodeName   string     `gorm:"column:node_name"`
	PolicyID   *uint      `gorm:"column:policy_id"`
	PolicyName string     `gorm:"column:policy_name"`
	LastError  string     `gorm:"column:last_error"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
	StartedAt  *time.Time `gorm:"column:started_at"`
	FinishedAt *time.Time `gorm:"column:finished_at"`
}

type healthIncidentDeliveryRow struct {
	DeliveryID    uint       `gorm:"column:delivery_id"`
	AlertID       uint       `gorm:"column:alert_id"`
	Status        string     `gorm:"column:status"`
	AttemptCount  int        `gorm:"column:attempt_count"`
	NextRetryAt   *time.Time `gorm:"column:next_retry_at"`
	LastError     string     `gorm:"column:last_error"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	NodeID        uint       `gorm:"column:node_id"`
	NodeName      string     `gorm:"column:node_name"`
	TaskID        *uint      `gorm:"column:task_id"`
	TaskRunID     *uint      `gorm:"column:task_run_id"`
	PolicyName    string     `gorm:"column:policy_name"`
	AlertSeverity string     `gorm:"column:alert_severity"`
	ErrorCode     string     `gorm:"column:error_code"`
}

type healthIncidentAnomalyRow struct {
	ID            uint      `gorm:"column:id"`
	NodeID        uint      `gorm:"column:node_id"`
	NodeName      string    `gorm:"column:node_name"`
	Detector      string    `gorm:"column:detector"`
	Metric        string    `gorm:"column:metric"`
	Severity      string    `gorm:"column:severity"`
	ObservedValue float64   `gorm:"column:observed_value"`
	BaselineValue float64   `gorm:"column:baseline_value"`
	AlertID       *uint     `gorm:"column:alert_id"`
	FiredAt       time.Time `gorm:"column:fired_at"`
}

type healthIncidentMetricRow struct {
	ID        uint      `gorm:"column:id"`
	NodeID    uint      `gorm:"column:node_id"`
	NodeName  string    `gorm:"column:node_name"`
	CpuPct    float64   `gorm:"column:cpu_pct"`
	MemPct    float64   `gorm:"column:mem_pct"`
	DiskPct   float64   `gorm:"column:disk_pct"`
	ProbeOK   bool      `gorm:"column:probe_ok"`
	SampledAt time.Time `gorm:"column:sampled_at"`
}

type healthIncidentPolicyRunRow struct {
	PolicyID   uint      `gorm:"column:policy_id"`
	PolicyName string    `gorm:"column:policy_name"`
	TaskID     uint      `gorm:"column:task_id"`
	NodeID     uint      `gorm:"column:node_id"`
	NodeName   string    `gorm:"column:node_name"`
	Status     string    `gorm:"column:status"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// Get godoc
// @Summary      获取健康事件时间线
// @Description  只读聚合近期告警、任务失败、节点探测/指标、通知失败和备份健康降级
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Param        window_hours  query     int  false  "时间窗口小时数（默认 72，最大 168）"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /overview/health-incident-timeline [get]
func (h *HealthIncidentTimelineHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")

	now := time.Now().UTC()
	windowHours := parseHealthIncidentWindowHours(c.Query("window_hours"))
	since := now.Add(-time.Duration(windowHours) * time.Hour)
	db := h.db.WithContext(c.Request.Context())

	ownedIDs, needOwnerFilter, err := ownershipNodeFilter(c, db)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	acc := newHealthIncidentAccumulator(now)
	if needOwnerFilter && len(ownedIDs) == 0 {
		respondOK(c, acc.response(windowHours))
		return
	}

	alerts, err := h.recentAlerts(db, since, ownedIDs, needOwnerFilter)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	failures, err := h.recentTaskFailures(db, since, ownedIDs, needOwnerFilter)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	deliveries, err := h.recentDeliveryFailures(db, since, ownedIDs, needOwnerFilter)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	taskIDs := map[uint]struct{}{}
	policyNames := map[string]struct{}{}
	for _, alert := range alerts {
		if alert.TaskID != nil && *alert.TaskID > 0 {
			taskIDs[*alert.TaskID] = struct{}{}
		}
		if strings.TrimSpace(alert.PolicyName) != "" {
			policyNames[alert.PolicyName] = struct{}{}
		}
	}
	for _, row := range failures {
		if row.TaskID > 0 {
			taskIDs[row.TaskID] = struct{}{}
		}
		if strings.TrimSpace(row.PolicyName) != "" {
			policyNames[row.PolicyName] = struct{}{}
		}
	}
	for _, row := range deliveries {
		if row.TaskID != nil && *row.TaskID > 0 {
			taskIDs[*row.TaskID] = struct{}{}
		}
		if strings.TrimSpace(row.PolicyName) != "" {
			policyNames[row.PolicyName] = struct{}{}
		}
	}

	taskInfos, err := h.taskInfos(db, taskIDs, ownedIDs, needOwnerFilter)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	policyIDs, err := h.policyIDsByName(db, policyNames)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	for _, alert := range alerts {
		resource := resourceForAlert(alert, taskInfos, policyIDs)
		severity := normalizeHealthIncidentSeverity(alert.Severity)
		message := util.SanitizeMessage(alert.Message)
		if message == "" {
			message = "告警已触发"
		}
		signal := healthIncidentSignal{
			Type:       "alert",
			Severity:   severity,
			OccurredAt: alert.TriggeredAt.UTC(),
			Message:    message,
			AlertID:    alert.ID,
			NodeID:     alert.NodeID,
		}
		if alert.TaskID != nil {
			signal.TaskID = *alert.TaskID
		}
		if alert.TaskRunID != nil {
			signal.TaskRunID = *alert.TaskRunID
		}
		if resource.PolicyID > 0 {
			signal.PolicyID = resource.PolicyID
		}
		acc.addSignal(resource, signal, message, actionsForResource(resource, alert.ID, "alert"))
	}

	for _, row := range failures {
		resource := resourceForTaskFailure(row)
		message := util.SanitizeMessage(row.LastError)
		if message == "" {
			message = "任务执行失败"
		}
		signal := healthIncidentSignal{
			Type:       "task_failure",
			Severity:   "critical",
			OccurredAt: bestTaskRunTime(row.FinishedAt, row.UpdatedAt, row.CreatedAt),
			Message:    message,
			TaskID:     row.TaskID,
			TaskRunID:  row.TaskRunID,
			NodeID:     row.NodeID,
		}
		if row.PolicyID != nil {
			signal.PolicyID = *row.PolicyID
		}
		acc.addSignal(resource, signal, message, actionsForResource(resource, 0, "task_failure"))
	}

	for _, row := range deliveries {
		resource := resourceForDelivery(row, taskInfos, policyIDs)
		severity := "warning"
		if row.Status == "failed" {
			severity = "critical"
		}
		message := util.SanitizeMessage(row.LastError)
		if message == "" {
			message = fmt.Sprintf("告警通知状态为 %s", row.Status)
		}
		signal := healthIncidentSignal{
			Type:       "notification_failure",
			Severity:   severity,
			OccurredAt: row.CreatedAt.UTC(),
			Message:    message,
			AlertID:    row.AlertID,
			DeliveryID: row.DeliveryID,
			NodeID:     row.NodeID,
		}
		if row.TaskID != nil {
			signal.TaskID = *row.TaskID
		}
		if row.TaskRunID != nil {
			signal.TaskRunID = *row.TaskRunID
		}
		if resource.PolicyID > 0 {
			signal.PolicyID = resource.PolicyID
		}
		acc.addSignal(resource, signal, message, actionsForResource(resource, row.AlertID, "notification_failure"))
	}

	if err := h.addAnomalySignals(db, acc, since, ownedIDs, needOwnerFilter); err != nil {
		respondInternalError(c, err)
		return
	}
	if err := h.addNodeProbeSignals(db, acc, ownedIDs, needOwnerFilter); err != nil {
		respondInternalError(c, err)
		return
	}
	if err := h.addMetricSignals(db, acc, since, ownedIDs, needOwnerFilter); err != nil {
		respondInternalError(c, err)
		return
	}
	if err := h.addBackupStaleSignals(db, acc, now, ownedIDs, needOwnerFilter); err != nil {
		respondInternalError(c, err)
		return
	}
	if err := h.addDegradedPolicySignals(db, acc, ownedIDs, needOwnerFilter); err != nil {
		respondInternalError(c, err)
		return
	}

	respondOK(c, acc.response(windowHours))
}

func parseHealthIncidentWindowHours(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return defaultHealthIncidentWindowHours
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultHealthIncidentWindowHours
	}
	if value > maxHealthIncidentWindowHours {
		return maxHealthIncidentWindowHours
	}
	return value
}

func newHealthIncidentAccumulator(now time.Time) *healthIncidentAccumulator {
	return &healthIncidentAccumulator{now: now, groups: map[string]*healthIncidentGroup{}}
}

func (a *healthIncidentAccumulator) addSignal(resource healthIncidentResource, signal healthIncidentSignal, cause string, actions []healthIncidentAction) {
	resource = normalizeHealthIncidentResource(resource)
	signal.Severity = normalizeHealthIncidentSeverity(signal.Severity)
	if signal.OccurredAt.IsZero() {
		signal.OccurredAt = a.now
	}
	signal.OccurredAt = signal.OccurredAt.UTC()
	signal.Message = util.SanitizeMessage(signal.Message)
	if signal.Message == "" {
		signal.Message = cause
	}

	key := healthIncidentResourceKey(resource)
	group, ok := a.groups[key]
	if !ok {
		group = &healthIncidentGroup{
			ID:          healthIncidentGroupID(key),
			Severity:    "info",
			Resource:    resource,
			LastSeenAt:  signal.OccurredAt,
			LikelyCause: "健康信号需要关注",
			sourceSet:   map[string]struct{}{},
			actionSet:   map[string]struct{}{},
		}
		a.groups[key] = group
	}

	group.EventCount++
	if signal.OccurredAt.After(group.LastSeenAt) {
		group.LastSeenAt = signal.OccurredAt
	}
	if severityPriority(signal.Severity) > severityPriority(group.Severity) {
		group.Severity = signal.Severity
	}
	if _, exists := group.sourceSet[signal.Type]; !exists {
		group.sourceSet[signal.Type] = struct{}{}
		group.SourceTypes = append(group.SourceTypes, signal.Type)
	}
	if cause = util.SanitizeMessage(cause); cause != "" {
		if group.LikelyCause == "健康信号需要关注" ||
			severityPriority(signal.Severity) > severityPriority(group.causeSeverity) ||
			(severityPriority(signal.Severity) == severityPriority(group.causeSeverity) && signal.OccurredAt.After(group.causeAt)) {
			group.LikelyCause = cause
			group.causeAt = signal.OccurredAt
			group.causeSeverity = signal.Severity
		}
	}
	if len(group.Signals) < maxHealthIncidentSignalsPerGroup {
		group.Signals = append(group.Signals, signal)
	}
	for _, action := range actions {
		if strings.TrimSpace(action.Href) == "" || strings.TrimSpace(action.Code) == "" {
			continue
		}
		actionKey := action.Code + "|" + action.Href
		if _, exists := group.actionSet[actionKey]; exists {
			continue
		}
		group.actionSet[actionKey] = struct{}{}
		group.NextActions = append(group.NextActions, action)
	}
}

func (a *healthIncidentAccumulator) response(windowHours int) healthIncidentTimelineResponse {
	groups := make([]healthIncidentGroup, 0, len(a.groups))
	for _, group := range a.groups {
		sort.Strings(group.SourceTypes)
		if len(group.NextActions) == 0 {
			group.NextActions = actionsForResource(group.Resource, 0, "")
		}
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if !groups[i].LastSeenAt.Equal(groups[j].LastSeenAt) {
			return groups[i].LastSeenAt.After(groups[j].LastSeenAt)
		}
		if severityPriority(groups[i].Severity) != severityPriority(groups[j].Severity) {
			return severityPriority(groups[i].Severity) > severityPriority(groups[j].Severity)
		}
		return groups[i].ID < groups[j].ID
	})

	summary := healthIncidentTimelineSummary{Total: len(groups)}
	for _, group := range groups {
		switch group.Severity {
		case "critical":
			summary.Critical++
		case "warning":
			summary.Warning++
		default:
			summary.Info++
		}
	}
	return healthIncidentTimelineResponse{
		GeneratedAt: a.now,
		WindowHours: windowHours,
		Summary:     summary,
		Groups:      groups,
	}
}

func (h *HealthIncidentTimelineHandler) recentAlerts(db *gorm.DB, since time.Time, ownedIDs []uint, needOwnerFilter bool) ([]model.Alert, error) {
	query := db.Model(&model.Alert{}).
		Where("triggered_at >= ?", since).
		Where("status <> ?", "resolved")
	if needOwnerFilter {
		query = query.Where("node_id IN ?", ownedIDs)
	}
	var alerts []model.Alert
	err := query.Order("triggered_at DESC").Limit(maxHealthIncidentSourceRows).Find(&alerts).Error
	return alerts, err
}

func (h *HealthIncidentTimelineHandler) recentTaskFailures(db *gorm.DB, since time.Time, ownedIDs []uint, needOwnerFilter bool) ([]healthIncidentTaskFailureRow, error) {
	query := db.Table("task_runs AS tr").
		Select(`tr.id AS task_run_id, tr.task_id AS task_id, tasks.name AS task_name, tasks.node_id AS node_id,
			COALESCE(nodes.name, '') AS node_name, tasks.policy_id AS policy_id, COALESCE(policies.name, '') AS policy_name,
			tr.last_error AS last_error, tr.created_at AS created_at, tr.updated_at AS updated_at,
			tr.started_at AS started_at, tr.finished_at AS finished_at`).
		Joins("JOIN tasks ON tasks.id = tr.task_id").
		Joins("LEFT JOIN nodes ON nodes.id = tasks.node_id").
		Joins("LEFT JOIN policies ON policies.id = tasks.policy_id").
		Where("tr.status = ? AND tr.created_at >= ?", "failed", since)
	if needOwnerFilter {
		query = query.Where("tasks.node_id IN ?", ownedIDs)
	}
	var rows []healthIncidentTaskFailureRow
	err := query.Order("tr.created_at DESC").Limit(maxHealthIncidentSourceRows).Scan(&rows).Error
	return rows, err
}

func (h *HealthIncidentTimelineHandler) recentDeliveryFailures(db *gorm.DB, since time.Time, ownedIDs []uint, needOwnerFilter bool) ([]healthIncidentDeliveryRow, error) {
	query := db.Table("alert_deliveries AS ad").
		Select(`ad.id AS delivery_id, ad.alert_id AS alert_id, ad.status AS status, ad.attempt_count AS attempt_count,
			ad.next_retry_at AS next_retry_at, ad.last_error AS last_error, ad.created_at AS created_at,
			a.node_id AS node_id, a.node_name AS node_name, a.task_id AS task_id, a.task_run_id AS task_run_id,
			a.policy_name AS policy_name, a.severity AS alert_severity, a.error_code AS error_code`).
		Joins("JOIN alerts AS a ON a.id = ad.alert_id").
		Where("ad.status IN ? AND ad.created_at >= ?", []string{"failed", "retrying"}, since)
	if needOwnerFilter {
		query = query.Where("a.node_id IN ?", ownedIDs)
	}
	var rows []healthIncidentDeliveryRow
	err := query.Order("ad.created_at DESC").Limit(maxHealthIncidentSourceRows).Scan(&rows).Error
	return rows, err
}

func (h *HealthIncidentTimelineHandler) taskInfos(db *gorm.DB, taskIDs map[uint]struct{}, ownedIDs []uint, needOwnerFilter bool) (map[uint]healthIncidentTaskInfo, error) {
	result := map[uint]healthIncidentTaskInfo{}
	ids := make([]uint, 0, len(taskIDs))
	for id := range taskIDs {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	query := db.Table("tasks").
		Select("tasks.id AS id, tasks.name AS name, tasks.node_id AS node_id, COALESCE(nodes.name, '') AS node_name, tasks.policy_id AS policy_id, COALESCE(policies.name, '') AS policy_name").
		Joins("LEFT JOIN nodes ON nodes.id = tasks.node_id").
		Joins("LEFT JOIN policies ON policies.id = tasks.policy_id").
		Where("tasks.id IN ?", ids)
	if needOwnerFilter {
		query = query.Where("tasks.node_id IN ?", ownedIDs)
	}
	var rows []healthIncidentTaskInfo
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func (h *HealthIncidentTimelineHandler) policyIDsByName(db *gorm.DB, policyNames map[string]struct{}) (map[string]uint, error) {
	result := map[string]uint{}
	names := make([]string, 0, len(policyNames))
	for name := range policyNames {
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return result, nil
	}
	type row struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	var rows []row
	if err := db.Model(&model.Policy{}).Select("id", "name").Where("name IN ?", names).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, item := range rows {
		result[item.Name] = item.ID
	}
	return result, nil
}

func (h *HealthIncidentTimelineHandler) addAnomalySignals(db *gorm.DB, acc *healthIncidentAccumulator, since time.Time, ownedIDs []uint, needOwnerFilter bool) error {
	query := db.Table("anomaly_events AS ae").
		Select("ae.id AS id, ae.node_id AS node_id, COALESCE(nodes.name, '') AS node_name, ae.detector AS detector, ae.metric AS metric, ae.severity AS severity, ae.observed_value AS observed_value, ae.baseline_value AS baseline_value, ae.alert_id AS alert_id, ae.fired_at AS fired_at").
		Joins("LEFT JOIN nodes ON nodes.id = ae.node_id").
		Where("ae.fired_at >= ?", since)
	if needOwnerFilter {
		query = query.Where("ae.node_id IN ?", ownedIDs)
	}
	var rows []healthIncidentAnomalyRow
	if err := query.Order("ae.fired_at DESC").Limit(maxHealthIncidentSourceRows).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		resource := nodeHealthIncidentResource(row.NodeID, row.NodeName)
		message := fmt.Sprintf("%s/%s 指标异常：观测 %.1f，基线 %.1f", row.Detector, row.Metric, row.ObservedValue, row.BaselineValue)
		signal := healthIncidentSignal{
			Type:       "anomaly",
			Severity:   normalizeHealthIncidentSeverity(row.Severity),
			OccurredAt: row.FiredAt.UTC(),
			Message:    message,
			NodeID:     row.NodeID,
		}
		alertID := uint(0)
		if row.AlertID != nil {
			signal.AlertID = *row.AlertID
			alertID = *row.AlertID
		}
		acc.addSignal(resource, signal, message, actionsForResource(resource, alertID, "anomaly"))
	}
	return nil
}

func (h *HealthIncidentTimelineHandler) addNodeProbeSignals(db *gorm.DB, acc *healthIncidentAccumulator, ownedIDs []uint, needOwnerFilter bool) error {
	query := db.Model(&model.Node{}).
		Where("status <> ? OR consecutive_failures > ?", "online", 0)
	if needOwnerFilter {
		query = query.Where("id IN ?", ownedIDs)
	}
	var nodes []model.Node
	if err := query.Order("updated_at DESC").Limit(maxHealthIncidentSourceRows).Find(&nodes).Error; err != nil {
		return err
	}
	for _, node := range nodes {
		resource := nodeHealthIncidentResource(node.ID, node.Name)
		lastSeen := node.UpdatedAt.UTC()
		if node.LastProbeAt != nil {
			lastSeen = node.LastProbeAt.UTC()
		}
		severity := "warning"
		if node.Status == "offline" && node.ConsecutiveFailures >= 5 {
			severity = "critical"
		}
		message := fmt.Sprintf("节点状态为 %s", node.Status)
		if node.ConsecutiveFailures > 0 {
			message = fmt.Sprintf("节点状态为 %s，连续探测失败 %d 次", node.Status, node.ConsecutiveFailures)
		}
		acc.addSignal(resource, healthIncidentSignal{
			Type:       "probe",
			Severity:   severity,
			OccurredAt: lastSeen,
			Message:    message,
			NodeID:     node.ID,
		}, message, actionsForResource(resource, 0, "probe"))
	}
	return nil
}

func (h *HealthIncidentTimelineHandler) addMetricSignals(db *gorm.DB, acc *healthIncidentAccumulator, since time.Time, ownedIDs []uint, needOwnerFilter bool) error {
	query := db.Table("node_metric_samples AS nms").
		Select("nms.id AS id, nms.node_id AS node_id, COALESCE(nodes.name, '') AS node_name, nms.cpu_pct AS cpu_pct, nms.mem_pct AS mem_pct, nms.disk_pct AS disk_pct, nms.probe_ok AS probe_ok, nms.sampled_at AS sampled_at").
		Joins("LEFT JOIN nodes ON nodes.id = nms.node_id").
		Where("nms.sampled_at >= ?", since).
		Where("nms.probe_ok = ? OR nms.cpu_pct >= ? OR nms.mem_pct >= ? OR nms.disk_pct >= ?", false, 90, 90, 90)
	if needOwnerFilter {
		query = query.Where("nms.node_id IN ?", ownedIDs)
	}
	var rows []healthIncidentMetricRow
	if err := query.Order("nms.sampled_at DESC").Limit(maxHealthIncidentSourceRows).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		resource := nodeHealthIncidentResource(row.NodeID, row.NodeName)
		severity := "warning"
		message := "节点指标超过阈值"
		if !row.ProbeOK {
			message = "节点探测样本失败"
		}
		if row.DiskPct >= 90 {
			message = fmt.Sprintf("磁盘使用率 %.1f%% 超过阈值", row.DiskPct)
		}
		if row.CpuPct >= 90 && row.CpuPct >= row.MemPct && row.CpuPct >= row.DiskPct {
			message = fmt.Sprintf("CPU 使用率 %.1f%% 超过阈值", row.CpuPct)
		}
		if row.MemPct >= 90 && row.MemPct >= row.CpuPct && row.MemPct >= row.DiskPct {
			message = fmt.Sprintf("内存使用率 %.1f%% 超过阈值", row.MemPct)
		}
		if row.CpuPct >= 95 || row.MemPct >= 95 || row.DiskPct >= 95 {
			severity = "critical"
		}
		acc.addSignal(resource, healthIncidentSignal{
			Type:       "metric",
			Severity:   severity,
			OccurredAt: row.SampledAt.UTC(),
			Message:    message,
			NodeID:     row.NodeID,
		}, message, actionsForResource(resource, 0, "metric"))
	}
	return nil
}

func (h *HealthIncidentTimelineHandler) addBackupStaleSignals(db *gorm.DB, acc *healthIncidentAccumulator, now time.Time, ownedIDs []uint, needOwnerFilter bool) error {
	staleThreshold := now.Add(-time.Duration(backupStaleThresholdHours()) * time.Hour)
	query := db.Model(&model.Node{}).
		Where("archived = ?", false).
		Where("last_backup_at IS NULL OR last_backup_at < ?", staleThreshold)
	if needOwnerFilter {
		query = query.Where("id IN ?", ownedIDs)
	}
	var nodes []model.Node
	if err := query.Order("last_backup_at ASC").Limit(maxHealthIncidentSourceRows).Find(&nodes).Error; err != nil {
		return err
	}
	for _, node := range nodes {
		resource := nodeHealthIncidentResource(node.ID, node.Name)
		message := "节点尚未完成备份"
		if node.LastBackupAt != nil {
			hours := int(now.Sub(*node.LastBackupAt).Hours())
			message = fmt.Sprintf("节点最近一次备份距今约 %d 小时，超过健康阈值", hours)
		}
		acc.addSignal(resource, healthIncidentSignal{
			Type:       "backup_stale",
			Severity:   "warning",
			OccurredAt: now,
			Message:    message,
			NodeID:     node.ID,
		}, message, actionsForResource(resource, 0, "backup_stale"))
	}
	return nil
}

func (h *HealthIncidentTimelineHandler) addDegradedPolicySignals(db *gorm.DB, acc *healthIncidentAccumulator, ownedIDs []uint, needOwnerFilter bool) error {
	query := db.Table("task_runs AS tr").
		Select("policies.id AS policy_id, policies.name AS policy_name, tasks.id AS task_id, tasks.node_id AS node_id, COALESCE(nodes.name, '') AS node_name, tr.status AS status, tr.created_at AS created_at").
		Joins("JOIN tasks ON tasks.id = tr.task_id").
		Joins("JOIN policies ON policies.id = tasks.policy_id").
		Joins("LEFT JOIN nodes ON nodes.id = tasks.node_id").
		Where("policies.enabled = ? AND policies.is_template = ?", true, false)
	if needOwnerFilter {
		query = query.Where("tasks.node_id IN ?", ownedIDs)
	}
	var rows []healthIncidentPolicyRunRow
	if err := query.Order("policies.id ASC, tr.created_at DESC").Limit(1000).Scan(&rows).Error; err != nil {
		return err
	}
	perPolicy := map[uint][]healthIncidentPolicyRunRow{}
	for _, row := range rows {
		if len(perPolicy[row.PolicyID]) < 3 {
			perPolicy[row.PolicyID] = append(perPolicy[row.PolicyID], row)
		}
	}
	for policyID, latestRows := range perPolicy {
		if len(latestRows) < 3 {
			continue
		}
		allFailed := true
		for _, row := range latestRows {
			if row.Status != "failed" {
				allFailed = false
				break
			}
		}
		if !allFailed {
			continue
		}
		first := latestRows[0]
		resource := policyHealthIncidentResource(policyID, first.PolicyName, first.NodeID, first.NodeName)
		message := "策略最近 3 次备份任务均失败，备份可信度降级"
		acc.addSignal(resource, healthIncidentSignal{
			Type:       "backup_degraded",
			Severity:   "critical",
			OccurredAt: first.CreatedAt.UTC(),
			Message:    message,
			TaskID:     first.TaskID,
			NodeID:     first.NodeID,
			PolicyID:   policyID,
		}, message, actionsForResource(resource, 0, "backup_degraded"))
	}
	return nil
}

func backupStaleThresholdHours() int {
	staleHours := 48
	if v := os.Getenv("BACKUP_STALE_THRESHOLD_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			staleHours = n
		}
	}
	return staleHours
}

func resourceForAlert(alert model.Alert, taskInfos map[uint]healthIncidentTaskInfo, policyIDs map[string]uint) healthIncidentResource {
	if alert.TaskID != nil && *alert.TaskID > 0 {
		if info, ok := taskInfos[*alert.TaskID]; ok {
			return taskHealthIncidentResource(info)
		}
		res := healthIncidentResource{Type: "task", ID: *alert.TaskID, Name: fmt.Sprintf("任务 #%d", *alert.TaskID), NodeID: alert.NodeID, NodeName: alert.NodeName}
		if policyID := policyIDs[alert.PolicyName]; policyID > 0 {
			res.PolicyID = policyID
		}
		res.PolicyName = alert.PolicyName
		return res
	}
	if strings.TrimSpace(alert.PolicyName) != "" {
		return policyHealthIncidentResource(policyIDs[alert.PolicyName], alert.PolicyName, alert.NodeID, alert.NodeName)
	}
	if alert.NodeID > 0 {
		return nodeHealthIncidentResource(alert.NodeID, alert.NodeName)
	}
	return platformHealthIncidentResource(alert.NodeName)
}

func resourceForDelivery(row healthIncidentDeliveryRow, taskInfos map[uint]healthIncidentTaskInfo, policyIDs map[string]uint) healthIncidentResource {
	if row.TaskID != nil && *row.TaskID > 0 {
		if info, ok := taskInfos[*row.TaskID]; ok {
			return taskHealthIncidentResource(info)
		}
		res := healthIncidentResource{Type: "task", ID: *row.TaskID, Name: fmt.Sprintf("任务 #%d", *row.TaskID), NodeID: row.NodeID, NodeName: row.NodeName}
		if policyID := policyIDs[row.PolicyName]; policyID > 0 {
			res.PolicyID = policyID
		}
		res.PolicyName = row.PolicyName
		return res
	}
	if strings.TrimSpace(row.PolicyName) != "" {
		return policyHealthIncidentResource(policyIDs[row.PolicyName], row.PolicyName, row.NodeID, row.NodeName)
	}
	if row.NodeID > 0 {
		return nodeHealthIncidentResource(row.NodeID, row.NodeName)
	}
	return platformHealthIncidentResource(row.NodeName)
}

func resourceForTaskFailure(row healthIncidentTaskFailureRow) healthIncidentResource {
	res := healthIncidentResource{
		Type:       "task",
		ID:         row.TaskID,
		Name:       row.TaskName,
		NodeID:     row.NodeID,
		NodeName:   row.NodeName,
		PolicyName: row.PolicyName,
	}
	if row.PolicyID != nil {
		res.PolicyID = *row.PolicyID
	}
	return normalizeHealthIncidentResource(res)
}

func taskHealthIncidentResource(info healthIncidentTaskInfo) healthIncidentResource {
	res := healthIncidentResource{
		Type:       "task",
		ID:         info.ID,
		Name:       info.Name,
		NodeID:     info.NodeID,
		NodeName:   info.NodeName,
		PolicyName: info.PolicyName,
	}
	if info.PolicyID != nil {
		res.PolicyID = *info.PolicyID
	}
	return normalizeHealthIncidentResource(res)
}

func nodeHealthIncidentResource(id uint, name string) healthIncidentResource {
	return normalizeHealthIncidentResource(healthIncidentResource{Type: "node", ID: id, Name: name, NodeID: id, NodeName: name})
}

func policyHealthIncidentResource(id uint, name string, nodeID uint, nodeName string) healthIncidentResource {
	return normalizeHealthIncidentResource(healthIncidentResource{Type: "policy", ID: id, Name: name, NodeID: nodeID, NodeName: nodeName, PolicyID: id, PolicyName: name})
}

func platformHealthIncidentResource(name string) healthIncidentResource {
	return normalizeHealthIncidentResource(healthIncidentResource{Type: "platform", Name: name})
}

func normalizeHealthIncidentResource(resource healthIncidentResource) healthIncidentResource {
	resource.Type = strings.TrimSpace(resource.Type)
	switch resource.Type {
	case "node", "task", "policy", "platform":
	default:
		resource.Type = "platform"
	}
	resource.Name = strings.TrimSpace(resource.Name)
	if resource.Name == "" {
		switch resource.Type {
		case "node":
			resource.Name = fmt.Sprintf("节点 #%d", resource.ID)
		case "task":
			resource.Name = fmt.Sprintf("任务 #%d", resource.ID)
		case "policy":
			resource.Name = fmt.Sprintf("策略 #%d", resource.ID)
		default:
			resource.Name = "平台"
		}
	}
	if resource.Type == "node" {
		resource.NodeID = resource.ID
		resource.NodeName = resource.Name
	}
	if resource.Type == "policy" {
		resource.PolicyID = resource.ID
		resource.PolicyName = resource.Name
	}
	return resource
}

func healthIncidentResourceKey(resource healthIncidentResource) string {
	if resource.Type == "platform" {
		return "platform"
	}
	if resource.ID > 0 {
		return fmt.Sprintf("%s:%d", resource.Type, resource.ID)
	}
	return fmt.Sprintf("%s:%s", resource.Type, strings.ToLower(resource.Name))
}

func healthIncidentGroupID(key string) string {
	replacer := strings.NewReplacer(":", "-", " ", "-", "/", "-", "\\", "-", "?", "-", "&", "-", "=", "-")
	return replacer.Replace(key)
}

func normalizeHealthIncidentSeverity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical":
		return "critical"
	case "warning", "warn":
		return "warning"
	case "info", "success":
		return "info"
	default:
		return "warning"
	}
}

func severityPriority(severity string) int {
	switch severity {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func bestTaskRunTime(finishedAt *time.Time, updatedAt time.Time, createdAt time.Time) time.Time {
	if finishedAt != nil && !finishedAt.IsZero() {
		return finishedAt.UTC()
	}
	if !updatedAt.IsZero() {
		return updatedAt.UTC()
	}
	if !createdAt.IsZero() {
		return createdAt.UTC()
	}
	return time.Now().UTC()
}

func actionsForResource(resource healthIncidentResource, alertID uint, sourceType string) []healthIncidentAction {
	actions := make([]healthIncidentAction, 0, 4)
	if alertID > 0 {
		actions = append(actions, healthIncidentAction{Code: "view_alert", Label: "查看告警", Href: fmt.Sprintf("/app/notifications?alert=alert-%d", alertID)})
	}
	switch resource.Type {
	case "task":
		if resource.ID > 0 {
			actions = append(actions,
				healthIncidentAction{Code: "view_task_logs", Label: "查看任务日志", Href: fmt.Sprintf("/app/logs?task=%d", resource.ID)},
				healthIncidentAction{Code: "view_tasks", Label: "查看任务", Href: "/app/tasks"},
			)
		}
	case "node":
		if resource.ID > 0 {
			actions = append(actions,
				healthIncidentAction{Code: "view_node_metrics", Label: "查看节点指标", Href: fmt.Sprintf("/app/nodes/%d?tab=metrics", resource.ID)},
				healthIncidentAction{Code: "view_node_alerts", Label: "查看节点告警", Href: fmt.Sprintf("/app/nodes/%d?tab=alerts", resource.ID)},
			)
			if sourceType == "probe" || sourceType == "metric" {
				actions = append(actions, healthIncidentAction{Code: "open_node_doctor", Label: "打开节点诊断入口", Href: "/app/nodes?keyword=" + url.QueryEscape(resource.Name)})
			}
		}
	case "policy":
		actions = append(actions,
			healthIncidentAction{Code: "view_backup_confidence", Label: "查看备份可信度", Href: "/app/backups"},
			healthIncidentAction{Code: "view_policies", Label: "查看策略", Href: "/app/policies"},
		)
	default:
		actions = append(actions, healthIncidentAction{Code: "view_notifications", Label: "查看告警中心", Href: "/app/notifications"})
	}
	if sourceType == "backup_stale" || sourceType == "backup_degraded" {
		actions = append(actions, healthIncidentAction{Code: "view_backup_confidence", Label: "查看备份可信度", Href: "/app/backups"})
	}
	if len(actions) == 0 {
		actions = append(actions, healthIncidentAction{Code: "view_notifications", Label: "查看告警中心", Href: "/app/notifications"})
	}
	return actions
}
