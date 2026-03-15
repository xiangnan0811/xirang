package handlers

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
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
		query = query.Where("status = ?", status)
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

	limit := 200
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	offset := 0
	if rawOffset := strings.TrimSpace(c.Query("offset")); rawOffset != "" {
		if parsed, err := strconv.Atoi(rawOffset); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var alerts []model.Alert
	if err := query.Order("triggered_at desc").Limit(limit).Offset(offset).Find(&alerts).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":   alerts,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *AlertHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "告警不存在"})
		return
	}
	if !checkOwnershipByNodeID(c, h.db, alert.NodeID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该告警"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": alert})
}

func (h *AlertHandler) Ack(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "告警不存在"})
		return
	}
	if !checkOwnershipByNodeID(c, h.db, alert.NodeID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作该告警"})
		return
	}
	if alert.Status == "resolved" {
		c.JSON(http.StatusOK, gin.H{"data": alert})
		return
	}
	alert.Status = "acked"
	if err := h.db.Save(&alert).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": alert})
}

func (h *AlertHandler) Resolve(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "告警不存在"})
		return
	}
	if !checkOwnershipByNodeID(c, h.db, alert.NodeID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作该告警"})
		return
	}
	alert.Status = "resolved"
	alert.Retryable = false
	if err := h.db.Save(&alert).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": alert})
}

func (h *AlertHandler) Deliveries(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var alert model.Alert
	if err := h.db.Select("id").First(&alert, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "告警不存在"})
		return
	}

	var deliveries []model.AlertDelivery
	if err := h.db.Where("alert_id = ?", id).Order("id desc").Find(&deliveries).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": deliveries})
}

func (h *AlertHandler) RetryDelivery(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req retryDeliveryRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.IntegrationID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "告警不存在"})
		return
	}

	var integration model.Integration
	if err := h.db.First(&integration, req.IntegrationID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
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
		c.JSON(http.StatusOK, gin.H{"data": retryDeliveryResponse{
			OK:       false,
			Message:  "重发失败: " + delivery.Error,
			Delivery: delivery,
		}})
		return
	}

	delivery.Status = "sent"
	if err := h.db.Create(&delivery).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": retryDeliveryResponse{
		OK:       true,
		Message:  "重发成功",
		Delivery: delivery,
	}})
}

func (h *AlertHandler) RetryFailedDeliveries(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "告警不存在"})
		return
	}

	var failedRecords []model.AlertDelivery
	if err := h.db.Where("alert_id = ? AND status = ?", alert.ID, "failed").Order("id desc").Find(&failedRecords).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	if len(failedRecords) == 0 {
		c.JSON(http.StatusOK, gin.H{"data": retryFailedDeliveriesResponse{
			OK:            true,
			Message:       "当前告警没有失败投递记录",
			TotalFailed:   0,
			SuccessCount:  0,
			FailedCount:   0,
			NewDeliveries: []model.AlertDelivery{},
		}})
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
	c.JSON(http.StatusOK, gin.H{"data": retryFailedDeliveriesResponse{
		OK:            failedCount == 0,
		Message:       message,
		TotalFailed:   len(uniqueIntegrationIDs),
		SuccessCount:  successCount,
		FailedCount:   failedCount,
		NewDeliveries: newDeliveries,
	}})
}

func (h *AlertHandler) DeliveryStats(c *gin.Context) {
	hours := parseDeliveryStatsHours(c.Query("hours"))
	from := time.Now().Add(-time.Duration(hours) * time.Hour)

	var totals struct {
		Sent   int64 `gorm:"column:sent"`
		Failed int64 `gorm:"column:failed"`
	}
	if err := h.db.Model(&model.AlertDelivery{}).
		Select("COALESCE(SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END), 0) AS sent, COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) AS failed").
		Where("created_at >= ?", from).
		Scan(&totals).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var byIntegration []deliveryStatsByIntegration
	if err := h.db.Table("alert_deliveries AS ad").
		Select("ad.integration_id AS integration_id, COALESCE(i.name, '') AS name, COALESCE(i.type, '') AS type, COALESCE(SUM(CASE WHEN ad.status = 'sent' THEN 1 ELSE 0 END), 0) AS sent, COALESCE(SUM(CASE WHEN ad.status = 'failed' THEN 1 ELSE 0 END), 0) AS failed").
		Joins("LEFT JOIN integrations AS i ON i.id = ad.integration_id").
		Where("ad.created_at >= ?", from).
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

	c.JSON(http.StatusOK, gin.H{"data": deliveryStatsResponse{
		WindowHours:   hours,
		TotalSent:     totals.Sent,
		TotalFailed:   totals.Failed,
		SuccessRate:   successRate,
		ByIntegration: byIntegration,
	}})
}

func (h *AlertHandler) UnreadCount(c *gin.Context) {
	var counts struct {
		Total    int64 `gorm:"column:total"`
		Critical int64 `gorm:"column:critical"`
		Warning  int64 `gorm:"column:warning"`
	}
	if err := h.db.Model(&model.Alert{}).
		Select("COUNT(*) as total, COALESCE(SUM(CASE WHEN severity='critical' THEN 1 ELSE 0 END), 0) as critical, COALESCE(SUM(CASE WHEN severity='warning' THEN 1 ELSE 0 END), 0) as warning").
		Where("status = ?", "open").
		Scan(&counts).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"total":    counts.Total,
		"critical": counts.Critical,
		"warning":  counts.Warning,
	}})
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
