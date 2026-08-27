package handlers

import (
	"fmt"
	"net/http"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
)

// Response is the unified API response envelope.
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// PaginatedResponse extends Response with pagination metadata.
type PaginatedResponse struct {
	Code     int         `json:"code"`
	Message  string      `json:"message"`
	Data     interface{} `json:"data"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func respondOK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{Code: http.StatusOK, Message: "ok", Data: data})
}

func respondOKWithMessage(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusOK, Response{Code: http.StatusOK, Message: msg, Data: data})
}

func respondCreated(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, Response{Code: http.StatusCreated, Message: "ok", Data: data})
}

func respondAccepted(c *gin.Context, data interface{}) {
	c.JSON(http.StatusAccepted, Response{Code: http.StatusAccepted, Message: "ok", Data: data})
}

func respondMessage(c *gin.Context, msg string) {
	c.JSON(http.StatusOK, Response{Code: http.StatusOK, Message: msg, Data: nil})
}

func respondPaginated(c *gin.Context, data interface{}, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, PaginatedResponse{
		Code:     http.StatusOK,
		Message:  "ok",
		Data:     data,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

func respondBadRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, Response{Code: http.StatusBadRequest, Message: msg, Data: nil})
}

func respondUnauthorized(c *gin.Context, msg string) {
	c.JSON(http.StatusUnauthorized, Response{Code: http.StatusUnauthorized, Message: msg, Data: nil})
}

func respondForbidden(c *gin.Context, msg string) {
	c.JSON(http.StatusForbidden, Response{Code: http.StatusForbidden, Message: msg, Data: nil})
}

func respondGone(c *gin.Context, msg string) {
	c.JSON(http.StatusGone, Response{Code: http.StatusGone, Message: msg, Data: nil})
}

func respondForbiddenData(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusForbidden, Response{Code: http.StatusForbidden, Message: msg, Data: data})
}

func respondLocked(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusLocked, Response{Code: http.StatusLocked, Message: msg, Data: data})
}

func respondNotFound(c *gin.Context, msg string) {
	c.JSON(http.StatusNotFound, Response{Code: http.StatusNotFound, Message: msg, Data: nil})
}

func respondConflict(c *gin.Context, msg string) {
	c.JSON(http.StatusConflict, Response{Code: http.StatusConflict, Message: msg, Data: nil})
}

func respondPayloadTooLarge(c *gin.Context, msg string) {
	c.JSON(http.StatusRequestEntityTooLarge, Response{Code: http.StatusRequestEntityTooLarge, Message: msg, Data: nil})
}

func respondUnprocessableEntityData(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusUnprocessableEntity, Response{Code: http.StatusUnprocessableEntity, Message: msg, Data: data})
}

func respondBadGateway(c *gin.Context, msg string) {
	c.JSON(http.StatusBadGateway, Response{Code: http.StatusBadGateway, Message: msg, Data: nil})
}

func respondServiceUnavailable(c *gin.Context, msg string) {
	c.JSON(http.StatusServiceUnavailable, Response{Code: http.StatusServiceUnavailable, Message: msg, Data: nil})
}

type backupContentTransportErrorData struct {
	Reason backupContentTransportErrorReason `json:"reason"`
}

type backupContentTransportErrorReason struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params"`
}

func respondBackupContentSecureTransportRequired(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, Response{
		Code:    http.StatusServiceUnavailable,
		Message: "需要安全传输",
		Data: backupContentTransportErrorData{Reason: backupContentTransportErrorReason{
			Code: "secure_transport_required", Params: map[string]string{},
		}},
	})
}

func respondNotImplemented(c *gin.Context, msg string) {
	c.JSON(http.StatusNotImplemented, Response{Code: http.StatusNotImplemented, Message: msg, Data: nil})
}

type backupCapabilityErrorData struct {
	Reason        backupCapabilityErrorReason `json:"reason"`
	CorrelationID string                      `json:"correlation_id,omitempty"`
}

type backupCapabilityErrorReason struct {
	Code   backupasset.CapabilityCode `json:"code"`
	Params map[string]string          `json:"params"`
}

func respondBackupCapabilityError(c *gin.Context, status int, reason backupasset.CapabilityReason, correlationID string) {
	if (status != http.StatusNotImplemented && status != http.StatusServiceUnavailable) || backupasset.ValidateCapabilityReason(reason) != nil {
		respondInternalError(c, fmt.Errorf("invalid backup capability response"))
		return
	}
	message := "当前备份 Provider 不支持此能力"
	if status == http.StatusServiceUnavailable {
		message = "备份 Provider 暂不可用"
		if reason.Code == backupasset.CapabilityFeatureDisabled {
			message = "备份资产功能未启用"
		}
	}
	params := reason.Params
	if params == nil {
		params = map[string]string{}
	}
	c.JSON(status, Response{Code: status, Message: message, Data: backupCapabilityErrorData{
		Reason: backupCapabilityErrorReason{
			Code:   reason.Code,
			Params: params,
		},
		CorrelationID: correlationID,
	}})
}

func respondInternalError(c *gin.Context, err error) {
	if err != nil {
		logger.Module("api").Error().Err(err).Str("path", c.FullPath()).Msg("服务器内部错误")
	}
	c.JSON(http.StatusInternalServerError, Response{
		Code:    http.StatusInternalServerError,
		Message: "服务器内部错误",
		Data:    nil,
	})
}
