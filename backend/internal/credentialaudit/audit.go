package credentialaudit

import (
	"context"
	"encoding/json"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	OutcomeSuccess     = "success"
	OutcomeFailure     = "failure"
	OutcomeBlocked     = "blocked"
	maxMetadataEntries = 16
)

type Event struct {
	UserID           uint
	Username         string
	Role             string
	Action           string
	Purpose          string
	CredentialKind   string
	CredentialSource string
	SSHKeyID         *uint
	NodeID           *uint
	TaskID           *uint
	TaskRunID        *uint
	PolicyID         *uint
	Outcome          string
	ErrorMessage     string
	Metadata         map[string]any
	ClientIP         string
	UserAgent        string
}

type contextKey struct{}

type RuntimeContext struct {
	DB    *gorm.DB
	Event Event
}

func WithRuntimeContext(ctx context.Context, db *gorm.DB, event Event) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, RuntimeContext{DB: db, Event: event})
}

func RuntimeEvent(ctx context.Context) (RuntimeContext, bool) {
	if ctx == nil {
		return RuntimeContext{}, false
	}
	value, ok := ctx.Value(contextKey{}).(RuntimeContext)
	if !ok || value.DB == nil {
		return RuntimeContext{}, false
	}
	return value, true
}

func WriteRuntime(ctx context.Context, event Event) error {
	base, ok := RuntimeEvent(ctx)
	if !ok {
		return nil
	}
	merged := mergeEvents(base.Event, event)
	return Write(base.DB, merged)
}

func mergeEvents(base Event, override Event) Event {
	merged := base
	if override.UserID != 0 {
		merged.UserID = override.UserID
	}
	if override.Username != "" {
		merged.Username = override.Username
	}
	if override.Role != "" {
		merged.Role = override.Role
	}
	if override.Action != "" {
		merged.Action = override.Action
	}
	if override.Purpose != "" {
		merged.Purpose = override.Purpose
	}
	if override.CredentialKind != "" {
		merged.CredentialKind = override.CredentialKind
	}
	if override.CredentialSource != "" {
		merged.CredentialSource = override.CredentialSource
	}
	if override.SSHKeyID != nil {
		merged.SSHKeyID = override.SSHKeyID
	}
	if override.NodeID != nil {
		merged.NodeID = override.NodeID
	}
	if override.TaskID != nil {
		merged.TaskID = override.TaskID
	}
	if override.TaskRunID != nil {
		merged.TaskRunID = override.TaskRunID
	}
	if override.PolicyID != nil {
		merged.PolicyID = override.PolicyID
	}
	if override.Outcome != "" {
		merged.Outcome = override.Outcome
	}
	if override.ErrorMessage != "" {
		merged.ErrorMessage = override.ErrorMessage
	}
	merged.Metadata = mergeMetadata(base.Metadata, override.Metadata)
	if override.ClientIP != "" {
		merged.ClientIP = override.ClientIP
	}
	if override.UserAgent != "" {
		merged.UserAgent = override.UserAgent
	}
	return merged
}

func mergeMetadata(base, override map[string]any) map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func Write(db *gorm.DB, event Event) error {
	if db == nil {
		return nil
	}
	outcome := strings.TrimSpace(event.Outcome)
	if outcome == "" {
		outcome = OutcomeSuccess
	}
	metadata := sanitizeMetadata(event.Metadata)
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}
	record := model.CredentialAuditEvent{
		UserID:           event.UserID,
		Username:         bound(util.SanitizeMessage(event.Username), 64),
		Role:             bound(util.SanitizeMessage(event.Role), 32),
		Action:           bound(util.SanitizeMessage(event.Action), 64),
		Purpose:          bound(util.SanitizeMessage(event.Purpose), 64),
		CredentialKind:   bound(util.SanitizeMessage(event.CredentialKind), 32),
		CredentialSource: bound(util.SanitizeMessage(event.CredentialSource), 64),
		SSHKeyID:         event.SSHKeyID,
		NodeID:           event.NodeID,
		TaskID:           event.TaskID,
		TaskRunID:        event.TaskRunID,
		PolicyID:         event.PolicyID,
		Outcome:          bound(util.SanitizeMessage(outcome), 16),
		ErrorMessage:     sanitizeErrorMessage(event.ErrorMessage),
		Metadata:         string(metadataJSON),
		ClientIP:         bound(util.SanitizeMessage(event.ClientIP), 64),
		UserAgent:        bound(util.SanitizeMessage(event.UserAgent), 255),
	}
	return db.Create(&record).Error
}

func FromGin(c *gin.Context, event Event) Event {
	if c == nil {
		return event
	}
	if event.UserID == 0 {
		event.UserID = c.GetUint("userID")
	}
	if event.Username == "" {
		event.Username = c.GetString("username")
	}
	if event.Role == "" {
		event.Role = c.GetString("role")
	}
	if event.ClientIP == "" {
		event.ClientIP = c.ClientIP()
	}
	if event.UserAgent == "" && c.Request != nil {
		event.UserAgent = c.Request.UserAgent()
	}
	return event
}

func PtrUint(v uint) *uint {
	if v == 0 {
		return nil
	}
	return &v
}

func sanitizeMetadata(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		cleanKey := bound(util.SanitizeMessage(strings.TrimSpace(key)), 64)
		if cleanKey == "" || metadataKeyDenied(cleanKey) {
			continue
		}
		switch v := value.(type) {
		case string:
			cleanValue := strings.TrimSpace(util.SanitizeMessage(v))
			if cleanValue == "" || metadataValueDenied(cleanValue) {
				continue
			}
			out[cleanKey] = cleanValue
		case []string:
			items := make([]string, 0, len(v))
			for _, item := range v {
				clean := strings.TrimSpace(util.SanitizeMessage(item))
				if clean != "" && !metadataValueDenied(clean) {
					items = append(items, clean)
				}
			}
			if len(items) == 0 {
				continue
			}
			out[cleanKey] = items
		case bool:
			out[cleanKey] = v
		case int:
			out[cleanKey] = v
		case int64:
			out[cleanKey] = v
		case uint:
			out[cleanKey] = v
		case uint64:
			out[cleanKey] = v
		case float64:
			out[cleanKey] = v
		default:
			cleanValue := strings.TrimSpace(util.SanitizeMessage(toString(v)))
			if cleanValue == "" || metadataValueDenied(cleanValue) {
				continue
			}
			out[cleanKey] = cleanValue
		}
		if len(out) >= maxMetadataEntries {
			break
		}
	}
	return out
}

func metadataKeyDenied(key string) bool {
	lower := strings.ToLower(key)
	for _, forbidden := range []string{"private", "password", "token", "secret", "credential", "config", "output", "stream", "command", "content", "payload"} {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	return false
}

func metadataValueDenied(value string) bool {
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"private", "password", "token", "secret", "credential", "config", "output", "stream", "command", "content", "payload", "bearer", "authorization:"} {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	return false
}

func toString(value any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(bytes)
}

func sanitizeErrorMessage(value string) string {
	clean := util.SanitizeMessage(value)
	if strings.TrimSpace(clean) == "" {
		return ""
	}
	clean = redactAfterMarker(clean, "输出:")
	clean = redactAfterMarker(clean, "output:")
	clean = redactAfterMarker(clean, "stdout:")
	clean = redactAfterMarker(clean, "stderr:")
	return clean
}

func redactAfterMarker(value, marker string) string {
	lower := strings.ToLower(value)
	idx := strings.Index(lower, strings.ToLower(marker))
	if idx < 0 {
		return value
	}
	return strings.TrimSpace(value[:idx+len(marker)]) + " [REDACTED_OUTPUT]"
}

func bound(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if max <= 0 || len([]rune(trimmed)) <= max {
		return trimmed
	}
	runes := []rune(trimmed)
	return string(runes[:max])
}
