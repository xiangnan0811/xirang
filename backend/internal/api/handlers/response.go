package handlers

import (
	"net/http"

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
	c.JSON(http.StatusOK, Response{Code: 0, Message: "ok", Data: data})
}

func respondCreated(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, Response{Code: 0, Message: "ok", Data: data})
}

func respondMessage(c *gin.Context, msg string) {
	c.JSON(http.StatusOK, Response{Code: 0, Message: msg, Data: nil})
}

func respondPaginated(c *gin.Context, data interface{}, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, PaginatedResponse{
		Code:     0,
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

func respondNotFound(c *gin.Context, msg string) {
	c.JSON(http.StatusNotFound, Response{Code: http.StatusNotFound, Message: msg, Data: nil})
}

func respondConflict(c *gin.Context, msg string) {
	c.JSON(http.StatusConflict, Response{Code: http.StatusConflict, Message: msg, Data: nil})
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
