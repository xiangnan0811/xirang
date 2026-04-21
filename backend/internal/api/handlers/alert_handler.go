package handlers

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AlertHandler struct {
	db *gorm.DB
}

type retryDeliveryRequest struct {
	IntegrationID uint `json:"integration_id" binding:"required"`
}

type retryDeliveryResponse struct {
	OK       bool                `json:"ok"`
	Message  string              `json:"message"`
	Delivery model.AlertDelivery `json:"delivery"`
}

type retryFailedDeliveriesResponse struct {
	OK            bool                  `json:"ok"`
	Message       string                `json:"message"`
	TotalFailed   int                   `json:"total_failed"`
	SuccessCount  int                   `json:"success_count"`
	FailedCount   int                   `json:"failed_count"`
	NewDeliveries []model.AlertDelivery `json:"new_deliveries"`
}

type deliveryStatsByIntegration struct {
	IntegrationID uint   `json:"integration_id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Sent          int64  `json:"sent"`
	Failed        int64  `json:"failed"`
}

type deliveryStatsResponse struct {
	WindowHours   int                          `json:"window_hours"`
	TotalSent     int64                        `json:"total_sent"`
	TotalFailed   int64                        `json:"total_failed"`
	SuccessRate   float64                      `json:"success_rate"`
	ByIntegration []deliveryStatsByIntegration `json:"by_integration"`
}

func NewAlertHandler(db *gorm.DB) *AlertHandler {
	return &AlertHandler{db: db}
}

// List godoc
// @Summary      列出告警
// @Description  返回告警列表（分页），支持按状态、节点、任务、严重级别、关键字过滤
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Param        page       query     int     false  "页码（默认 1）"
// @Param        page_size  query     int     false  "每页条数（默认 20，最大 200）"
// @Param        status     query     string  false  "告警状态（open/acked/resolved/unresolved）"
// @Param        node_id    query     int     false  "节点 ID 过滤"
// @Param        task_id    query     int     false  "任务 ID 过滤"
// @Param        severity   query     string  false  "严重级别（critical/warning）"
// @Param        keyword    query     string  false  "关键字模糊搜索"
// @Success      200  {object}  handlers.PaginatedResponse{data=[]model.Alert}
// @Failure      401  {object}  handlers.Response
// @Router       /alerts [get]
func (h *AlertHandler) List(c *gin.Context) {
	query := h.db.Model(&model.Alert{})

	if nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db); err != nil {
		respondInternalError(c, err)
		return
	} else if needFilter {
		query = query.Where("node_id IN ?", nodeIDs)
	}

	status := strings.TrimSpace(c.Query("status"))
	if status != "" {
		if status == "unresolved" {
			query = query.Where("status != ?", "resolved")
		} else {
			query = query.Where("status = ?", status)
		}
	}
	if rawNodeID := strings.TrimSpace(c.Query("node_id")); rawNodeID != "" {
		nodeID, err := strconv.ParseUint(rawNodeID, 10, 64)
		if err == nil {
			query = query.Where("node_id = ?", uint(nodeID))
		}
	}
	if rawTaskID := strings.TrimSpace(c.Query("task_id")); rawTaskID != "" {
		taskID, err := strconv.ParseUint(rawTaskID, 10, 64)
		if err == nil {
			query = query.Where("task_id = ?", uint(taskID))
		}
	}
	if severity := strings.TrimSpace(c.Query("severity")); severity != "" {
		query = query.Where("severity = ?", severity)
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + strings.ToLower(keyword) + "%"
		query = query.Where("LOWER(node_name) LIKE ? OR LOWER(policy_name) LIKE ? OR LOWER(error_code) LIKE ? OR LOWER(message) LIKE ?", like, like, like, like)
	}

	pg := parsePagination(c, 200, "triggered_at", map[string]bool{
		"triggered_at": true, "severity": true, "status": true, "node_name": true,
	})

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var alerts []model.Alert
	if err := applyPagination(query, pg).Find(&alerts).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondPaginated(c, alerts, total, pg.Page, pg.PageSize)
}

// alertGroupInfo 表示告警在内存分组窗口内的计数信息。
type alertGroupInfo struct {
	// Count 是当前分组窗口内（含本次）累计出现的同类告警次数。
	Count int `json:"count"`
	// SiblingNodeIDs 刻意留空：渐进式内存分组只追踪计数，不保留单条告警标识。
	// SiblingNodeIDs is intentionally empty: progressive in-memory grouping
	// only tracks counts by key, not individual alert identity.
	SiblingNodeIDs []uint `json:"sibling_node_ids,omitempty"`
}

// alertWithGroupInfo 在 Alert 模型基础上附加分组信息。
type alertWithGroupInfo struct {
	model.Alert
	GroupInfo alertGroupInfo `json:"group_info"`
}

// Get godoc
// @Summary      获取告警详情
// @Description  返回单个告警的详细信息（含内存分组计数）
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "告警 ID"
// @Success      200  {object}  handlers.Response{data=alertWithGroupInfo}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /alerts/{id} [get]
func (h *AlertHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var a model.Alert
	if err := h.db.First(&a, id).Error; err != nil {
		respondNotFound(c, "告警不存在")
		return
	}
	if allowed, err := authorizeNodeOwnership(c, h.db, a.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权访问该告警")
		return
	}

	var node model.Node
	if res := h.db.First(&node, a.NodeID); res.Error != nil {
		logger.Module("api").Warn().
			Uint("alert_id", a.ID).
			Uint("node_id", a.NodeID).
			Err(res.Error).
			Msg("alert detail: 节点加载失败，group_count 将为 0")
	}
	tags := strings.Split(node.Tags, ",")
	cleanTags := make([]string, 0, len(tags))
	for _, t := range tags {
		if v := strings.TrimSpace(t); v != "" {
			cleanTags = append(cleanTags, v)
		}
	}
	key := alerting.GroupKey(a.ErrorCode, a.NodeID, cleanTags)

	respondOK(c, alertWithGroupInfo{
		Alert: a,
		GroupInfo: alertGroupInfo{
			Count:          alerting.GetSharedGrouping().Count(key),
			SiblingNodeIDs: nil,
		},
	})
}

// Ack godoc
// @Summary      确认告警
// @Description  将告警状态标记为已确认（acked）
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "告警 ID"
// @Success      200  {object}  handlers.Response{data=model.Alert}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /alerts/{id}/ack [post]
func (h *AlertHandler) Ack(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		respondNotFound(c, "告警不存在")
		return
	}
	if allowed, err := authorizeNodeOwnership(c, h.db, alert.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权操作该告警")
		return
	}
	if alert.Status == "resolved" {
		respondOK(c, alert)
		return
	}
	alert.Status = "acked"
	if err := h.db.Save(&alert).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, alert)
}

// Resolve godoc
// @Summary      解决告警
// @Description  将告警状态标记为已解决（resolved）
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "告警 ID"
// @Success      200  {object}  handlers.Response{data=model.Alert}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /alerts/{id}/resolve [post]
func (h *AlertHandler) Resolve(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		respondNotFound(c, "告警不存在")
		return
	}
	if allowed, err := authorizeNodeOwnership(c, h.db, alert.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权操作该告警")
		return
	}
	alert.Status = "resolved"
	alert.Retryable = false
	if err := h.db.Save(&alert).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, alert)
}

// Deliveries godoc
// @Summary      获取告警投递记录
// @Description  返回指定告警的所有通知投递记录
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "告警 ID"
// @Success      200  {object}  handlers.Response{data=[]model.AlertDelivery}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /alerts/{id}/deliveries [get]
func (h *AlertHandler) Deliveries(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var alert model.Alert
	if err := h.db.Select("id", "node_id").First(&alert, id).Error; err != nil {
		respondNotFound(c, "告警不存在")
		return
	}
	if allowed, err := authorizeNodeOwnership(c, h.db, alert.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权访问该告警投递记录")
		return
	}

	var deliveries []model.AlertDelivery
	if err := h.db.Where("alert_id = ?", id).Order("id desc").Find(&deliveries).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondOK(c, deliveries)
}

// RetryDelivery godoc
// @Summary      重发告警通知
// @Description  向指定通知通道重新发送告警
// @Tags         alerts
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                   true  "告警 ID"
// @Param        body  body      retryDeliveryRequest  true  "重发请求（integration_id）"
// @Success      200   {object}  handlers.Response{data=retryDeliveryResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /alerts/{id}/retry [post]
func (h *AlertHandler) RetryDelivery(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req retryDeliveryRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.IntegrationID == 0 {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		respondNotFound(c, "告警不存在")
		return
	}
	if allowed, err := authorizeNodeOwnership(c, h.db, alert.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权操作该告警")
		return
	}

	var integration model.Integration
	if err := h.db.First(&integration, req.IntegrationID).Error; err != nil {
		respondNotFound(c, "通知通道不存在")
		return
	}

	delivery := model.AlertDelivery{
		AlertID:       alert.ID,
		IntegrationID: integration.ID,
	}
	if err := alerting.SendAlert(integration, alert); err != nil {
		delivery.Status = "failed"
		delivery.Error = util.SanitizeDeliveryError(integration.Type, err)
		if saveErr := h.db.Create(&delivery).Error; saveErr != nil {
			respondInternalError(c, saveErr)
			return
		}
		respondOK(c, retryDeliveryResponse{
			OK:       false,
			Message:  "重发失败: " + delivery.Error,
			Delivery: delivery,
		})
		return
	}

	delivery.Status = "sent"
	if err := h.db.Create(&delivery).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondOK(c, retryDeliveryResponse{
		OK:       true,
		Message:  "重发成功",
		Delivery: delivery,
	})
}

// RetryFailedDeliveries godoc
// @Summary      批量重发失败的告警通知
// @Description  对指定告警的所有失败投递记录进行批量重发
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "告警 ID"
// @Success      200  {object}  handlers.Response{data=retryFailedDeliveriesResponse}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /alerts/{id}/retry-all [post]
func (h *AlertHandler) RetryFailedDeliveries(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		respondNotFound(c, "告警不存在")
		return
	}
	if allowed, err := authorizeNodeOwnership(c, h.db, alert.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权操作该告警")
		return
	}

	var failedRecords []model.AlertDelivery
	if err := h.db.Where("alert_id = ? AND status = ?", alert.ID, "failed").Order("id desc").Find(&failedRecords).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	if len(failedRecords) == 0 {
		respondOK(c, retryFailedDeliveriesResponse{
			OK:            true,
			Message:       "当前告警没有失败投递记录",
			TotalFailed:   0,
			SuccessCount:  0,
			FailedCount:   0,
			NewDeliveries: []model.AlertDelivery{},
		})
		return
	}

	seenIntegration := map[uint]struct{}{}
	uniqueIntegrationIDs := make([]uint, 0, len(failedRecords))
	for _, record := range failedRecords {
		if _, exists := seenIntegration[record.IntegrationID]; exists {
			continue
		}
		seenIntegration[record.IntegrationID] = struct{}{}
		uniqueIntegrationIDs = append(uniqueIntegrationIDs, record.IntegrationID)
	}

	newDeliveries := make([]model.AlertDelivery, 0, len(uniqueIntegrationIDs))
	successCount := 0
	failedCount := 0

	for _, integrationID := range uniqueIntegrationIDs {
		newRecord := model.AlertDelivery{
			AlertID:       alert.ID,
			IntegrationID: integrationID,
		}

		var integration model.Integration
		if err := h.db.First(&integration, integrationID).Error; err != nil {
			newRecord.Status = "failed"
			newRecord.Error = fmt.Sprintf("通知通道不存在: %d", integrationID)
			failedCount += 1
		} else if err := alerting.SendAlert(integration, alert); err != nil {
			newRecord.Status = "failed"
			newRecord.Error = util.SanitizeDeliveryError(integration.Type, err)
			failedCount += 1
		} else {
			newRecord.Status = "sent"
			successCount += 1
		}

		if err := h.db.Create(&newRecord).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		newDeliveries = append(newDeliveries, newRecord)
	}

	message := fmt.Sprintf("批量重发完成：成功 %d，失败 %d", successCount, failedCount)
	respondOK(c, retryFailedDeliveriesResponse{
		OK:            failedCount == 0,
		Message:       message,
		TotalFailed:   len(uniqueIntegrationIDs),
		SuccessCount:  successCount,
		FailedCount:   failedCount,
		NewDeliveries: newDeliveries,
	})
}

// DeliveryStats godoc
// @Summary      获取投递统计
// @Description  返回指定时间窗口内的通知投递统计数据
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Param        hours  query     int  false  "统计时间窗口（小时，默认 24，最大 720）"
// @Success      200  {object}  handlers.Response{data=deliveryStatsResponse}
// @Failure      401  {object}  handlers.Response
// @Router       /alerts/delivery-stats [get]
func (h *AlertHandler) DeliveryStats(c *gin.Context) {
	hours := parseDeliveryStatsHours(c.Query("hours"))
	from := time.Now().Add(-time.Duration(hours) * time.Hour)

	nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	totalQuery := h.db.Model(&model.AlertDelivery{}).
		Where("created_at >= ?", from)
	if needFilter {
		totalQuery = totalQuery.Where("alert_id IN (SELECT id FROM alerts WHERE node_id IN ?)", nodeIDs)
	}

	var totals struct {
		Sent   int64 `gorm:"column:sent"`
		Failed int64 `gorm:"column:failed"`
	}
	if err := totalQuery.
		Select("COALESCE(SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END), 0) AS sent, COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) AS failed").
		Scan(&totals).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	byIntQuery := h.db.Table("alert_deliveries AS ad").
		Select("ad.integration_id AS integration_id, COALESCE(i.name, '') AS name, COALESCE(i.type, '') AS type, COALESCE(SUM(CASE WHEN ad.status = 'sent' THEN 1 ELSE 0 END), 0) AS sent, COALESCE(SUM(CASE WHEN ad.status = 'failed' THEN 1 ELSE 0 END), 0) AS failed").
		Joins("LEFT JOIN integrations AS i ON i.id = ad.integration_id").
		Where("ad.created_at >= ?", from)
	if needFilter {
		byIntQuery = byIntQuery.Where("ad.alert_id IN (SELECT id FROM alerts WHERE node_id IN ?)", nodeIDs)
	}

	var byIntegration []deliveryStatsByIntegration
	if err := byIntQuery.
		Group("ad.integration_id, i.name, i.type").
		Order("failed DESC, sent DESC, ad.integration_id ASC").
		Scan(&byIntegration).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	for index := range byIntegration {
		if strings.TrimSpace(byIntegration[index].Name) == "" {
			byIntegration[index].Name = fmt.Sprintf("integration-%d", byIntegration[index].IntegrationID)
		}
	}

	total := totals.Sent + totals.Failed
	successRate := 0.0
	if total > 0 {
		successRate = math.Round((float64(totals.Sent)/float64(total))*1000) / 10
	}

	respondOK(c, deliveryStatsResponse{
		WindowHours:   hours,
		TotalSent:     totals.Sent,
		TotalFailed:   totals.Failed,
		SuccessRate:   successRate,
		ByIntegration: byIntegration,
	})
}

// UnreadCount godoc
// @Summary      获取未读告警数量
// @Description  返回当前未读（open 状态）告警的数量统计，按严重级别分组
// @Tags         alerts
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /alerts/unread-count [get]
func (h *AlertHandler) UnreadCount(c *gin.Context) {
	query := h.db.Model(&model.Alert{}).Where("status = ?", "open")

	if nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db); err != nil {
		respondInternalError(c, err)
		return
	} else if needFilter {
		query = query.Where("node_id IN ?", nodeIDs)
	}

	var counts struct {
		Total    int64 `gorm:"column:total"`
		Critical int64 `gorm:"column:critical"`
		Warning  int64 `gorm:"column:warning"`
	}
	if err := query.
		Select("COUNT(*) as total, COALESCE(SUM(CASE WHEN severity='critical' THEN 1 ELSE 0 END), 0) as critical, COALESCE(SUM(CASE WHEN severity='warning' THEN 1 ELSE 0 END), 0) as warning").
		Scan(&counts).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, gin.H{
		"total":    counts.Total,
		"critical": counts.Critical,
		"warning":  counts.Warning,
	})
}

// EscalationEvents returns the escalation timeline for one alert, ordered by level.
func (h *AlertHandler) EscalationEvents(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var evs []model.AlertEscalationEvent
	if err := h.db.WithContext(c.Request.Context()).
		Where("alert_id = ?", id).
		Order("level_index ASC").
		Find(&evs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, evs)
}

func parseDeliveryStatsHours(raw string) int {
	value := 24
	if strings.TrimSpace(raw) == "" {
		return value
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return value
	}
	if parsed <= 0 {
		return value
	}
	if parsed > 24*30 {
		return 24 * 30
	}
	return parsed
}
