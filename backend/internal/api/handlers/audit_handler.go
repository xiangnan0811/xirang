package handlers

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AuditHandler struct {
	db *gorm.DB
}

func NewAuditHandler(db *gorm.DB) *AuditHandler {
	return &AuditHandler{db: db}
}

func (h *AuditHandler) List(c *gin.Context) {
	query := h.buildQuery(c)

	pg := parsePagination(c, 50, "id", map[string]bool{
		"id": true, "created_at": true, "username": true, "method": true, "status_code": true,
	})

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var items []model.AuditLog
	if err := applyPagination(query, pg).Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	paginatedResponse(c, items, total, pg)
}

func (h *AuditHandler) ExportCSV(c *gin.Context) {
	query := h.buildQuery(c)

	limit := 1000
	rawLimit := strings.TrimSpace(c.Query("page_size"))
	if rawLimit == "" {
		rawLimit = strings.TrimSpace(c.Query("limit"))
	}
	if rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			if parsed > 5000 {
				limit = 5000
			} else {
				limit = parsed
			}
		}
	}

	var items []model.AuditLog
	if err := query.Order("id desc").Limit(limit).Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	fileName := fmt.Sprintf("audit-logs-%s.csv", time.Now().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	c.Status(http.StatusOK)

	// 写入 UTF-8 BOM 以便 Excel 正确识别编码
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	header := []string{"id", "created_at", "username", "role", "method", "path", "status_code", "client_ip", "user_agent"}
	if err := writer.Write(header); err != nil {
		return
	}

	for _, row := range items {
		record := []string{
			strconv.FormatUint(uint64(row.ID), 10),
			row.CreatedAt.Local().Format(config.DisplayTimeFormat),
			row.Username,
			row.Role,
			row.Method,
			row.Path,
			strconv.Itoa(row.StatusCode),
			row.ClientIP,
			row.UserAgent,
		}
		if err := writer.Write(record); err != nil {
			return
		}
	}
}

func (h *AuditHandler) buildQuery(c *gin.Context) *gorm.DB {
	query := h.db.Model(&model.AuditLog{})

	if username := strings.TrimSpace(c.Query("username")); username != "" {
		query = query.Where("username = ?", username)
	}
	if role := strings.TrimSpace(c.Query("role")); role != "" {
		query = query.Where("role = ?", role)
	}
	if method := strings.TrimSpace(c.Query("method")); method != "" {
		query = query.Where("UPPER(method) = ?", strings.ToUpper(method))
	}
	if pathKeyword := strings.TrimSpace(c.Query("path")); pathKeyword != "" {
		escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(pathKeyword)
		query = query.Where("path LIKE ? ESCAPE '\\'", "%"+escaped+"%")
	}

	if rawStatusCode := strings.TrimSpace(c.Query("status_code")); rawStatusCode != "" {
		if statusCode, err := strconv.Atoi(rawStatusCode); err == nil {
			query = query.Where("status_code = ?", statusCode)
		}
	}
	if rawUserID := strings.TrimSpace(c.Query("user_id")); rawUserID != "" {
		if userID, err := strconv.ParseUint(rawUserID, 10, 64); err == nil {
			query = query.Where("user_id = ?", uint(userID))
		}
	}

	if from := parseRFC3339(c.Query("from")); !from.IsZero() {
		query = query.Where("created_at >= ?", from)
	}
	if to := parseRFC3339(c.Query("to")); !to.IsZero() {
		query = query.Where("created_at <= ?", to)
	}

	return query
}

func parseRFC3339(raw string) time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
