package handlers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	credentialAuditExportDefaultLimit = 1000
	credentialAuditExportMaxLimit     = 5000
	credentialAuditMaxMetadataEntries = 16
	credentialAuditMaxMetadataList    = 16
	credentialAuditMaxMetadataKey     = 64
	credentialAuditMaxMetadataValue   = 500
)

var credentialAuditAllowedSorts = map[string]bool{
	"id":              true,
	"created_at":      true,
	"username":        true,
	"role":            true,
	"action":          true,
	"purpose":         true,
	"credential_kind": true,
	"outcome":         true,
}

var credentialAuditForbiddenKeyMarkers = []string{
	"private", "password", "token", "secret", "credential", "config", "output", "stream", "command", "content", "payload",
}

var credentialAuditForbiddenValueMarkers = []string{
	"private", "password", "token", "secret", "credential", "config", "output", "stream", "command", "content", "payload", "bearer", "authorization",
}

var credentialAuditErrorOutputMarkers = []string{
	"输出:",
	"output:",
	"stdout:",
	"stderr:",
	"command output:",
	"docker output:",
	"terminal stream:",
	"stream:",
	"file content:",
	"content:",
	"config:",
	"payload:",
	"diagnostic evidence:",
	"diagnostic:",
	"evidence:",
	"stack trace:",
	"traceback:",
	"panic:",
}

var credentialAuditStackLikePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bpanic\b`),
	regexp.MustCompile(`(?i)\btraceback\b`),
	regexp.MustCompile(`(?i)stack\s+trace`),
	regexp.MustCompile(`\b(?:[\w.-]+/)+[\w.-]+\.(?:go|ts|tsx|js|jsx|py|rb|java|php):\d+\b`),
}

var credentialAuditSensitiveErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*bearer\s+[^\s"',;)]+`),
	regexp.MustCompile(`(?i)bearer\s+[^\s"',;)]+`),
	regexp.MustCompile(`(?i)(private[_-]?key|token|api[_-]?key|secret|password)\s*[:=]\s*[^\s"',;)]+`),
}

var credentialAuditEndpointPattern = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>]+`)

type CredentialAuditHandler struct {
	db *gorm.DB
}

type credentialAuditEventResponse struct {
	ID               uint           `json:"id"`
	UserID           uint           `json:"user_id"`
	Username         string         `json:"username"`
	Role             string         `json:"role"`
	Action           string         `json:"action"`
	Purpose          string         `json:"purpose"`
	CredentialKind   string         `json:"credential_kind"`
	CredentialSource string         `json:"credential_source"`
	SSHKeyID         *uint          `json:"ssh_key_id,omitempty"`
	NodeID           *uint          `json:"node_id,omitempty"`
	TaskID           *uint          `json:"task_id,omitempty"`
	TaskRunID        *uint          `json:"task_run_id,omitempty"`
	PolicyID         *uint          `json:"policy_id,omitempty"`
	Outcome          string         `json:"outcome"`
	ErrorMessage     string         `json:"error_message,omitempty"`
	Metadata         map[string]any `json:"metadata"`
	ClientIP         string         `json:"client_ip"`
	UserAgent        string         `json:"user_agent"`
	CreatedAt        time.Time      `json:"created_at"`
}

func NewCredentialAuditHandler(db *gorm.DB) *CredentialAuditHandler {
	return &CredentialAuditHandler{db: db}
}

// List godoc
// @Summary      列出凭据使用审计事件
// @Description  返回分页的凭据使用审计事件，响应会重新清洗 metadata 和 legacy error_message
// @Tags         credential-audit
// @Security     Bearer
// @Produce      json
// @Param        page              query     int     false  "页码（默认 1）"
// @Param        page_size         query     int     false  "每页条数（默认 50）"
// @Param        username          query     string  false  "按用户名过滤"
// @Param        role              query     string  false  "按角色过滤"
// @Param        user_id           query     int     false  "按用户 ID 过滤"
// @Param        action            query     string  false  "按动作过滤"
// @Param        purpose           query     string  false  "按用途过滤"
// @Param        credential_kind   query     string  false  "按凭据类型过滤"
// @Param        credential_source query     string  false  "按凭据来源过滤"
// @Param        outcome           query     string  false  "按结果过滤"
// @Param        from              query     string  false  "开始时间（RFC3339）"
// @Param        to                query     string  false  "结束时间（RFC3339）"
// @Success      200  {object}  handlers.PaginatedResponse{data=[]handlers.credentialAuditEventResponse}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /credential-audit-events [get]
func (h *CredentialAuditHandler) List(c *gin.Context) {
	query := h.buildQuery(c)

	pg := parsePagination(c, 50, "id", credentialAuditAllowedSorts)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var items []model.CredentialAuditEvent
	if err := applyPagination(query, pg).Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondPaginated(c, mapCredentialAuditEvents(items), total, pg.Page, pg.PageSize)
}

// ExportCSV godoc
// @Summary      导出凭据使用审计事件 CSV
// @Description  使用与列表一致的过滤语义导出凭据使用审计事件，导出内容会重新清洗敏感字段
// @Tags         credential-audit
// @Security     Bearer
// @Produce      text/csv
// @Param        page_size         query     int     false  "最大条数（默认 1000，最大 5000）"
// @Param        username          query     string  false  "按用户名过滤"
// @Param        role              query     string  false  "按角色过滤"
// @Param        user_id           query     int     false  "按用户 ID 过滤"
// @Param        action            query     string  false  "按动作过滤"
// @Param        purpose           query     string  false  "按用途过滤"
// @Param        credential_kind   query     string  false  "按凭据类型过滤"
// @Param        credential_source query     string  false  "按凭据来源过滤"
// @Param        outcome           query     string  false  "按结果过滤"
// @Param        from              query     string  false  "开始时间（RFC3339）"
// @Param        to                query     string  false  "结束时间（RFC3339）"
// @Success      200
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /credential-audit-events/export [get]
func (h *CredentialAuditHandler) ExportCSV(c *gin.Context) {
	query := h.buildQuery(c)
	limit := parseCredentialAuditExportLimit(c)
	sortBy, sortOrder := parseCredentialAuditSort(c)

	var items []model.CredentialAuditEvent
	if err := query.Order(sortBy + " " + sortOrder).Limit(limit).Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	fileName := fmt.Sprintf("credential-audit-events-%s.csv", time.Now().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	c.Status(http.StatusOK)

	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	header := []string{
		"id", "created_at", "username", "role", "action", "purpose", "credential_kind", "credential_source", "outcome",
		"user_id", "ssh_key_id", "node_id", "task_id", "task_run_id", "policy_id", "client_ip", "user_agent", "error_message", "metadata",
	}
	if err := writer.Write(header); err != nil {
		return
	}

	for _, row := range mapCredentialAuditEvents(items) {
		metadataJSON, err := json.Marshal(row.Metadata)
		if err != nil {
			metadataJSON = []byte("{}")
		}
		record := []string{
			strconv.FormatUint(uint64(row.ID), 10),
			row.CreatedAt.Local().Format(config.DisplayTimeFormat),
			row.Username,
			row.Role,
			row.Action,
			row.Purpose,
			row.CredentialKind,
			row.CredentialSource,
			row.Outcome,
			strconv.FormatUint(uint64(row.UserID), 10),
			credentialAuditUintPtrString(row.SSHKeyID),
			credentialAuditUintPtrString(row.NodeID),
			credentialAuditUintPtrString(row.TaskID),
			credentialAuditUintPtrString(row.TaskRunID),
			credentialAuditUintPtrString(row.PolicyID),
			row.ClientIP,
			row.UserAgent,
			row.ErrorMessage,
			string(metadataJSON),
		}
		if err := writer.Write(record); err != nil {
			return
		}
	}
}

func (h *CredentialAuditHandler) buildQuery(c *gin.Context) *gorm.DB {
	query := h.db.Model(&model.CredentialAuditEvent{})

	for _, item := range []struct {
		param  string
		column string
	}{
		{param: "username", column: "username"},
		{param: "role", column: "role"},
		{param: "action", column: "action"},
		{param: "purpose", column: "purpose"},
		{param: "credential_kind", column: "credential_kind"},
		{param: "credential_source", column: "credential_source"},
		{param: "outcome", column: "outcome"},
	} {
		if value := strings.TrimSpace(c.Query(item.param)); value != "" {
			query = query.Where(item.column+" = ?", value)
		}
	}

	for _, item := range []struct {
		param  string
		column string
	}{
		{param: "user_id", column: "user_id"},
		{param: "ssh_key_id", column: "ssh_key_id"},
		{param: "node_id", column: "node_id"},
		{param: "task_id", column: "task_id"},
		{param: "task_run_id", column: "task_run_id"},
		{param: "policy_id", column: "policy_id"},
	} {
		if value, ok := parseCredentialAuditUintQuery(c, item.param); ok {
			query = query.Where(item.column+" = ?", value)
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

func mapCredentialAuditEvents(items []model.CredentialAuditEvent) []credentialAuditEventResponse {
	out := make([]credentialAuditEventResponse, 0, len(items))
	for _, item := range items {
		out = append(out, mapCredentialAuditEvent(item))
	}
	return out
}

func mapCredentialAuditEvent(row model.CredentialAuditEvent) credentialAuditEventResponse {
	return credentialAuditEventResponse{
		ID:               row.ID,
		UserID:           row.UserID,
		Username:         sanitizeCredentialAuditScalar(row.Username, 64),
		Role:             sanitizeCredentialAuditScalar(row.Role, 32),
		Action:           sanitizeCredentialAuditScalar(row.Action, 64),
		Purpose:          sanitizeCredentialAuditScalar(row.Purpose, 64),
		CredentialKind:   sanitizeCredentialAuditScalar(row.CredentialKind, 32),
		CredentialSource: sanitizeCredentialAuditScalar(row.CredentialSource, 64),
		SSHKeyID:         row.SSHKeyID,
		NodeID:           row.NodeID,
		TaskID:           row.TaskID,
		TaskRunID:        row.TaskRunID,
		PolicyID:         row.PolicyID,
		Outcome:          sanitizeCredentialAuditScalar(row.Outcome, 16),
		ErrorMessage:     sanitizeCredentialAuditErrorMessage(row.ErrorMessage),
		Metadata:         sanitizeCredentialAuditMetadataJSON(row.Metadata),
		ClientIP:         sanitizeCredentialAuditScalar(row.ClientIP, 64),
		UserAgent:        sanitizeCredentialAuditScalar(row.UserAgent, 255),
		CreatedAt:        row.CreatedAt,
	}
}

func parseCredentialAuditExportLimit(c *gin.Context) int {
	limit := credentialAuditExportDefaultLimit
	rawLimit := strings.TrimSpace(c.Query("page_size"))
	if rawLimit == "" {
		rawLimit = strings.TrimSpace(c.Query("limit"))
	}
	if rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			if parsed > credentialAuditExportMaxLimit {
				return credentialAuditExportMaxLimit
			}
			return parsed
		}
	}
	return limit
}

func parseCredentialAuditSort(c *gin.Context) (string, string) {
	sortBy := "id"
	if raw := strings.TrimSpace(c.Query("sort_by")); credentialAuditAllowedSorts[raw] {
		sortBy = raw
	}
	sortOrder := "desc"
	if strings.TrimSpace(c.Query("sort_order")) == "asc" {
		sortOrder = "asc"
	}
	return sortBy, sortOrder
}

func parseCredentialAuditUintQuery(c *gin.Context, key string) (uint, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, false
	}
	return uint(value), true
}

func sanitizeCredentialAuditMetadataJSON(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}

	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return map[string]any{}
	}
	metadata, ok := decoded.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return sanitizeCredentialAuditMetadataMap(metadata)
}

func sanitizeCredentialAuditMetadataMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, min(len(input), credentialAuditMaxMetadataEntries))
	for key, value := range input {
		cleanKey := sanitizeCredentialAuditScalar(strings.TrimSpace(key), credentialAuditMaxMetadataKey)
		if cleanKey == "" || credentialAuditMetadataKeyDenied(cleanKey) {
			continue
		}
		cleanValue, ok := sanitizeCredentialAuditMetadataValue(value)
		if !ok {
			continue
		}
		out[cleanKey] = cleanValue
		if len(out) >= credentialAuditMaxMetadataEntries {
			break
		}
	}
	return out
}

func sanitizeCredentialAuditMetadataValue(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		clean := sanitizeCredentialAuditScalar(v, credentialAuditMaxMetadataValue)
		if clean == "" || credentialAuditMetadataValueDenied(clean) {
			return nil, false
		}
		return clean, true
	case []string:
		items := make([]string, 0, min(len(v), credentialAuditMaxMetadataList))
		for _, item := range v {
			clean := sanitizeCredentialAuditScalar(item, credentialAuditMaxMetadataValue)
			if clean != "" && !credentialAuditMetadataValueDenied(clean) {
				items = append(items, clean)
			}
			if len(items) >= credentialAuditMaxMetadataList {
				break
			}
		}
		if len(items) == 0 {
			return nil, false
		}
		return items, true
	case []any:
		items := make([]string, 0, min(len(v), credentialAuditMaxMetadataList))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				continue
			}
			clean := sanitizeCredentialAuditScalar(text, credentialAuditMaxMetadataValue)
			if clean != "" && !credentialAuditMetadataValueDenied(clean) {
				items = append(items, clean)
			}
			if len(items) >= credentialAuditMaxMetadataList {
				break
			}
		}
		if len(items) == 0 {
			return nil, false
		}
		return items, true
	case bool:
		return v, true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		return v, true
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, false
		}
		return v, true
	case int:
		return v, true
	case int64:
		return v, true
	case uint:
		return v, true
	case uint64:
		return v, true
	default:
		return nil, false
	}
}

func sanitizeCredentialAuditErrorMessage(value string) string {
	clean := sanitizeCredentialAuditScalar(value, credentialAuditMaxMetadataValue)
	if clean == "" {
		return ""
	}
	for _, marker := range credentialAuditErrorOutputMarkers {
		clean = redactCredentialAuditAfterMarker(clean, marker)
	}
	for _, pattern := range credentialAuditStackLikePatterns {
		if pattern.MatchString(clean) {
			return "[REDACTED_ERROR]"
		}
	}
	return sanitizeCredentialAuditLegacySensitiveText(clean)
}

func redactCredentialAuditAfterMarker(value string, marker string) string {
	lower := strings.ToLower(value)
	idx := strings.Index(lower, strings.ToLower(marker))
	if idx < 0 {
		return value
	}
	return strings.TrimSpace(value[:idx+len(marker)]) + " [REDACTED_OUTPUT]"
}

func credentialAuditMetadataKeyDenied(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range credentialAuditForbiddenKeyMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func credentialAuditMetadataValueDenied(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range credentialAuditForbiddenValueMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sanitizeCredentialAuditScalar(value string, maxRunes int) string {
	clean := strings.TrimSpace(value)
	clean = sanitizeCredentialAuditLegacySensitiveText(clean)
	clean = strings.TrimSpace(util.SanitizeMessage(clean))
	clean = sanitizeCredentialAuditLegacySensitiveText(clean)
	return boundCredentialAuditRunes(clean, maxRunes)
}

func sanitizeCredentialAuditLegacySensitiveText(value string) string {
	clean := credentialAuditEndpointPattern.ReplaceAllString(value, "[REDACTED_ENDPOINT]")
	for _, pattern := range credentialAuditSensitiveErrorPatterns {
		clean = pattern.ReplaceAllString(clean, "[REDACTED_SECRET]")
	}
	return clean
}

func boundCredentialAuditRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func credentialAuditUintPtrString(value *uint) string {
	if value == nil || *value == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(*value), 10)
}
