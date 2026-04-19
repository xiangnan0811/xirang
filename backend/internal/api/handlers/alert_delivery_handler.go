package handlers

import (
	"xirang/backend/internal/alerting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AlertDeliveryHandler 处理告警投递相关请求。
type AlertDeliveryHandler struct {
	DB     *gorm.DB
	Worker *alerting.RetryWorker
}

// NewAlertDeliveryHandler 创建 AlertDeliveryHandler。
func NewAlertDeliveryHandler(db *gorm.DB, worker *alerting.RetryWorker) *AlertDeliveryHandler {
	return &AlertDeliveryHandler{DB: db, Worker: worker}
}

// Retry 立即强制重试指定投递记录（管理员操作）。
//
//	POST /alert-deliveries/:id/retry
func (h *AlertDeliveryHandler) Retry(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.Worker.ManualRetry(id); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	respondOK(c, gin.H{"status": "ok"})
}
