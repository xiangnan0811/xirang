package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/escalation"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type EscalationPreviewHandler struct {
	db  *gorm.DB
	now func() time.Time
}

func NewEscalationPreviewHandler(db *gorm.DB) *EscalationPreviewHandler {
	return &EscalationPreviewHandler{db: db, now: time.Now}
}

var (
	errEscalationPreviewInput    = errors.New("invalid preview input")
	errEscalationPreviewCapacity = errors.New("preview snapshot limit exceeded")
)

type previewDraftBody struct {
	Policy *struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		MinSeverity *string `json:"min_severity"`
		Enabled     *bool   `json:"enabled"`
		Levels      *[]struct {
			DelaySeconds     *int       `json:"delay_seconds"`
			IntegrationIDs   *[]*uint   `json:"integration_ids"`
			SeverityOverride *string    `json:"severity_override"`
			Tags             *[]*string `json:"tags"`
		} `json:"levels"`
	} `json:"policy"`
	Scenario *struct {
		NodeID       *uint      `json:"node_id"`
		ErrorCode    *string    `json:"error_code"`
		Severity     *string    `json:"severity"`
		Tags         *[]*string `json:"tags"`
		TriggeredAt  *string    `json:"triggered_at"`
		EvaluationAt *string    `json:"evaluation_at"`
	} `json:"scenario"`
}

func previewRequestTags(values *[]*string) ([]string, error) {
	if values == nil || len(*values) > 10 {
		return nil, errEscalationPreviewInput
	}
	out := make([]string, 0, len(*values))
	for _, value := range *values {
		if value == nil || !utf8.ValidString(*value) || len(*value) > 32 {
			return nil, errEscalationPreviewInput
		}
		out = append(out, *value)
	}
	return out, nil
}

func decodePreviewDraft(body []byte, generated time.Time) (escalation.PreviewInput, error) {
	input := escalation.PreviewInput{Mode: "draft", GeneratedAt: generated, Source: "draft"}
	var wire previewDraftBody
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return input, errEscalationPreviewInput
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return input, errEscalationPreviewInput
	}
	p, s := wire.Policy, wire.Scenario
	if p == nil || s == nil || p.Name == nil || p.Description == nil || p.MinSeverity == nil || p.Enabled == nil || p.Levels == nil || s.NodeID == nil || s.ErrorCode == nil || s.Severity == nil || s.TriggeredAt == nil || s.EvaluationAt == nil {
		return input, errEscalationPreviewInput
	}
	if uint64(*s.NodeID) > escalation.MaxPreviewID || len(*s.ErrorCode) < 1 || len(*s.ErrorCode) > 128 || !utf8.ValidString(*s.ErrorCode) || strings.IndexFunc(*s.ErrorCode, unicode.IsControl) >= 0 {
		return input, errEscalationPreviewInput
	}
	policy := escalation.PolicyInput{Name: *p.Name, Description: *p.Description, MinSeverity: *p.MinSeverity, Enabled: *p.Enabled, Levels: make([]model.EscalationLevel, 0, len(*p.Levels))}
	for _, level := range *p.Levels {
		if level.DelaySeconds == nil || level.IntegrationIDs == nil || level.SeverityOverride == nil || len(*level.IntegrationIDs) > 100 {
			return input, errEscalationPreviewInput
		}
		tags, err := previewRequestTags(level.Tags)
		if err != nil {
			return input, err
		}
		ids := make([]uint, 0, len(*level.IntegrationIDs))
		for _, id := range *level.IntegrationIDs {
			if id == nil || *id == 0 || uint64(*id) > escalation.MaxPreviewID {
				return input, errEscalationPreviewInput
			}
			ids = append(ids, *id)
		}
		policy.Levels = append(policy.Levels, model.EscalationLevel{DelaySeconds: *level.DelaySeconds, IntegrationIDs: ids, SeverityOverride: *level.SeverityOverride, Tags: tags})
	}
	if err := escalation.ValidatePolicyInput(policy); err != nil {
		return input, errEscalationPreviewInput
	}
	tags, err := previewRequestTags(s.Tags)
	if err != nil {
		return input, err
	}
	triggered, err := time.Parse(time.RFC3339, *s.TriggeredAt)
	if err != nil {
		return input, errEscalationPreviewInput
	}
	evaluation, err := time.Parse(time.RFC3339, *s.EvaluationAt)
	if err != nil {
		return input, errEscalationPreviewInput
	}
	encodedTags, _ := json.Marshal(tags)
	encodedLevels, _ := json.Marshal(policy.Levels)
	input.EvaluationAt = evaluation.UTC()
	input.Alert = model.Alert{NodeID: *s.NodeID, ErrorCode: *s.ErrorCode, Severity: *s.Severity, Status: "open", Tags: string(encodedTags), TriggeredAt: triggered.UTC(), LastLevelFired: -1}
	input.Policy = &model.EscalationPolicy{Name: policy.Name, MinSeverity: policy.MinSeverity, Enabled: policy.Enabled, Levels: string(encodedLevels)}
	if _, warning := escalation.PreviewWindow(input); warning != "" {
		return input, errEscalationPreviewInput
	}
	return input, nil
}

// readEscalationPreviewBody joins its reader on cancellation so a slow/chunked
// body cannot outlive the request's ten-second budget or touch a reused writer.
func readEscalationPreviewBody(ctx context.Context, c *gin.Context) ([]byte, error) {
	body := c.Request.Body
	if body == nil {
		body = http.NoBody
	}
	limited := http.MaxBytesReader(nil, body, 64<<10)
	c.Request.Body = limited
	controller := http.NewResponseController(c.Writer)
	deadline, _ := ctx.Deadline()
	setDeadline := controller.SetReadDeadline(deadline) == nil
	if setDeadline {
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	}
	type bodyResult struct {
		data []byte
		err  error
	}
	results := make(chan bodyResult, 1)
	go func() { data, err := io.ReadAll(limited); results <- bodyResult{data, err} }()
	select {
	case result := <-results:
		return result.data, result.err
	case <-ctx.Done():
		if setDeadline {
			_ = controller.SetReadDeadline(time.Now())
		}
		_ = limited.Close()
		<-results
		return nil, ctx.Err()
	}
}

func (h *EscalationPreviewHandler) PreviewDraft(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	generated := h.now().UTC()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	body, err := readEscalationPreviewBody(ctx, c)
	if err != nil {
		respondEscalationPreviewError(c, ctx, err)
		return
	}
	input, err := decodePreviewDraft(body, generated)
	if err != nil {
		respondEscalationPreviewError(c, ctx, err)
		return
	}
	err = withReadonlySnapshot(ctx, h.db, func(db *gorm.DB) error { return loadEscalationPreviewSnapshot(ctx, db, &input) })
	if err != nil {
		respondEscalationPreviewError(c, ctx, err)
		return
	}
	result, err := escalation.Preview(ctx, input)
	if err != nil {
		respondEscalationPreviewError(c, ctx, err)
		return
	}
	respondOK(c, result)
}

func (h *EscalationPreviewHandler) PreviewAlert(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	generated := h.now().UTC()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 || id > escalation.MaxPreviewID || strings.Trim(raw, "0123456789") != "" || c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
		respondBadRequest(c, "无效的预演请求")
		return
	}
	body, err := readEscalationPreviewBody(ctx, c)
	if err != nil {
		respondEscalationPreviewError(c, ctx, err)
		return
	}
	if len(body) != 0 {
		respondBadRequest(c, "诊断不接受请求正文")
		return
	}
	input := escalation.PreviewInput{Mode: "alert", GeneratedAt: generated, EvaluationAt: generated}
	err = withReadonlySnapshot(ctx, h.db, func(db *gorm.DB) error {
		if err := db.WithContext(ctx).Select("id,node_id,task_id,slo_id,severity,status,error_code,triggered_at,tags,last_level_fired,delivery_decision,delivery_reason").First(&input.Alert, uint(id)).Error; err != nil {
			return err
		}
		policy, reference, err := escalation.NewService(db).ResolvePolicyForPreview(ctx, input.Alert)
		if err != nil {
			return err
		}
		input.Policy, input.Source, input.SourceResourceID = policy, reference.Source, reference.ResourceID
		return loadEscalationPreviewSnapshot(ctx, db, &input)
	})
	if err != nil {
		respondEscalationPreviewError(c, ctx, err)
		return
	}
	result, err := escalation.Preview(ctx, input)
	if err != nil {
		respondEscalationPreviewError(c, ctx, err)
		return
	}
	respondOK(c, result)
}

func loadEscalationPreviewSnapshot(ctx context.Context, db *gorm.DB, input *escalation.PreviewInput) error {
	if input.Alert.NodeID > 0 {
		err := db.WithContext(ctx).Select("id,tags").First(&input.Node, input.Alert.NodeID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if input.Mode == "draft" {
				return err
			}
			input.Warnings = append(input.Warnings, "node_missing")
		} else if err != nil {
			return err
		}
	}
	if input.Mode == "alert" {
		var events []struct {
			LevelIndex         int
			EscalationPolicyID *uint
		}
		if err := db.WithContext(ctx).Table("alert_escalation_events").Select("level_index,escalation_policy_id").Where("alert_id = ?", input.Alert.ID).Limit(1001).Scan(&events).Error; err != nil {
			return err
		}
		if len(events) > 1000 {
			return errEscalationPreviewCapacity
		}
		changed, conflict := false, false
		hasNext := input.Policy != nil && input.Alert.LastLevelFired >= -1 && input.Alert.LastLevelFired < len(input.Policy.DecodedLevels())-1
		for _, event := range events {
			if hasNext && event.LevelIndex >= input.Alert.LastLevelFired+1 {
				conflict = true
			}
			if (input.Policy == nil && event.EscalationPolicyID != nil) || (input.Policy != nil && (event.EscalationPolicyID == nil || *event.EscalationPolicyID != input.Policy.ID)) {
				changed = true
			}
		}
		if changed {
			input.Warnings = append(input.Warnings, "policy_changed_since_history")
		}
		if conflict {
			input.Warnings = append(input.Warnings, "event_cursor_conflict")
		}
		var counts []struct {
			Status string
			Count  int64
		}
		if err := db.WithContext(ctx).Table("alert_deliveries").Select("status, COUNT(*) AS count").Where("alert_id = ?", input.Alert.ID).Group("status").Scan(&counts).Error; err != nil {
			return err
		}
		input.DeliveryCounts = &escalation.DeliveryCounts{}
		for _, count := range counts {
			switch count.Status {
			case "pending":
				input.DeliveryCounts.Pending += count.Count
			case "sending":
				input.DeliveryCounts.Sending += count.Count
			case "retrying":
				input.DeliveryCounts.Retrying += count.Count
			case "sent":
				input.DeliveryCounts.Sent += count.Count
			case "failed":
				input.DeliveryCounts.Failed += count.Count
			default:
				input.DeliveryCounts.Unknown += count.Count
			}
		}
	}
	end, warning := escalation.PreviewWindow(*input)
	if warning != "" {
		input.Warnings = append(input.Warnings, warning)
		return nil
	}
	ids := make([]uint, 0)
	if input.Policy != nil {
		for _, level := range input.Policy.DecodedLevels() {
			ids = append(ids, level.IntegrationIDs...)
		}
	}
	if len(ids) > 0 {
		if err := db.WithContext(ctx).Table("integrations").Select("id,name,type,enabled").Where("id IN ?", ids).Scan(&input.Integrations).Error; err != nil {
			return err
		}
	}
	if err := db.WithContext(ctx).Select("id,match_node_id,match_category,match_tags,starts_at,ends_at").Where("starts_at <= ? AND ends_at > ?", end, input.EvaluationAt).Limit(1001).Find(&input.Silences).Error; err != nil {
		return err
	}
	if len(input.Silences) > 1000 {
		return errEscalationPreviewCapacity
	}
	return ctx.Err()
}

func respondEscalationPreviewError(c *gin.Context, ctx context.Context, err error) {
	var size *http.MaxBytesError
	var network net.Error
	switch {
	case ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) || errors.Is(err, errEscalationPreviewCapacity):
		respondServiceUnavailable(c, "预演暂不可用或已超时")
	case errors.As(err, &size):
		respondPayloadTooLarge(c, "预演请求超过 64KB 限制")
	case errors.Is(err, errEscalationPreviewInput):
		respondBadRequest(c, "无效的预演输入")
	case errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, escalation.ErrNotFound):
		respondNotFound(c, "预演资源不存在")
	default:
		respondInternalError(c, errors.New("escalation preview snapshot failed"))
	}
}
