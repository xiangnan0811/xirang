package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
)

const MaxPreviewID uint64 = 1<<53 - 1

var ErrPreviewTime = errors.New("unsupported preview time")

type IntegrationMetadata struct {
	ID      uint
	Name    string
	Type    string
	Enabled bool
}

type DeliveryCounts struct {
	Pending  int64 `json:"pending"`
	Sending  int64 `json:"sending"`
	Retrying int64 `json:"retrying"`
	Sent     int64 `json:"sent"`
	Failed   int64 `json:"failed"`
	Unknown  int64 `json:"unknown"`
}

type PreviewInput struct {
	Mode             string
	GeneratedAt      time.Time
	EvaluationAt     time.Time
	Alert            model.Alert
	Policy           *model.EscalationPolicy
	Source           string
	SourceResourceID *uint
	Node             model.Node
	Silences         []model.Silence
	Integrations     []IntegrationMetadata
	DeliveryCounts   *DeliveryCounts
	Warnings         []string
}

type PreviewPolicy struct {
	ID          *uint      `json:"id"`
	Name        string     `json:"name"`
	MinSeverity string     `json:"min_severity"`
	Enabled     bool       `json:"enabled"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

type PreviewAlert struct {
	ID             *uint     `json:"id"`
	NodeID         uint      `json:"node_id"`
	Severity       string    `json:"severity"`
	Status         string    `json:"status"`
	TriggeredAt    time.Time `json:"triggered_at"`
	LastLevelFired int       `json:"last_level_fired"`
	Tags           []string  `json:"tags"`
}

type PreviewChannel struct {
	ID    uint    `json:"id"`
	Name  *string `json:"name"`
	Type  *string `json:"type"`
	State string  `json:"state"`
}

type PreviewStep struct {
	LevelIndex        int              `json:"level_index"`
	DelaySeconds      int              `json:"delay_seconds"`
	DueAt             time.Time        `json:"due_at"`
	ProjectedAt       *time.Time       `json:"projected_at"`
	Status            string           `json:"status"`
	SeverityBefore    string           `json:"severity_before"`
	SeverityAfter     string           `json:"severity_after"`
	TagsAfter         []string         `json:"tags_after"`
	MatchedSilenceIDs []uint           `json:"matched_silence_ids"`
	Channels          []PreviewChannel `json:"channels"`
}

type PreviewResult struct {
	Mode             string          `json:"mode"`
	Basis            string          `json:"basis"`
	GeneratedAt      time.Time       `json:"generated_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	EvaluationAt     time.Time       `json:"evaluation_at"`
	TickSeconds      int             `json:"tick_seconds"`
	RequestedAlertID *uint           `json:"requested_alert_id"`
	Status           string          `json:"status"`
	PolicySource     string          `json:"policy_source"`
	SourceResourceID *uint           `json:"source_resource_id"`
	Policy           *PreviewPolicy  `json:"policy"`
	Alert            *PreviewAlert   `json:"alert"`
	NextLevelIndex   *int            `json:"next_level_index"`
	NextDueAt        *time.Time      `json:"next_due_at"`
	NextDueNow       *bool           `json:"next_due_now"`
	DeliveryDecision *string         `json:"delivery_decision"`
	DeliveryCounts   *DeliveryCounts `json:"delivery_counts"`
	Warnings         []string        `json:"warnings"`
	Steps            []PreviewStep   `json:"steps"`
}

func previewSeverity(value string) bool {
	return value == "info" || value == "warning" || value == "critical"
}

// DecodePreviewTags validates persisted JSON without silently accepting partial
// decodes. Legacy blank/NULL representations retain their wildcard/empty meaning.
func DecodePreviewTags(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return []string{}, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		var tag string
		if len(value) == 0 || value[0] != '"' {
			return nil, errors.New("invalid tag")
		}
		if err := json.Unmarshal(value, &tag); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, nil
}

func previewLevels(policy *model.EscalationPolicy) ([]model.EscalationLevel, string) {
	if policy == nil {
		return nil, ""
	}
	var levels []model.EscalationLevel
	if err := json.Unmarshal([]byte(policy.Levels), &levels); err != nil {
		return nil, "unsupported_configuration"
	}
	if err := ValidatePolicyInput(PolicyInput{Name: policy.Name, MinSeverity: policy.MinSeverity, Levels: levels}); err != nil {
		return nil, "unsupported_configuration"
	}
	// Validate nested tags separately: encoding/json accepts null string elements.
	var rawLevels []struct {
		Tags json.RawMessage `json:"tags"`
	}
	if err := json.Unmarshal([]byte(policy.Levels), &rawLevels); err != nil {
		return nil, "unsupported_configuration"
	}
	for i, level := range levels {
		if level.DelaySeconds < 0 || int64(level.DelaySeconds) > math.MaxInt64/int64(time.Second) {
			return nil, "unsupported_time"
		}
		if len(level.IntegrationIDs) > 100 {
			return nil, "unsupported_configuration"
		}
		for _, id := range level.IntegrationIDs {
			if id == 0 || uint64(id) > MaxPreviewID {
				return nil, "unsupported_configuration"
			}
		}
		if _, err := DecodePreviewTags(string(rawLevels[i].Tags)); err != nil {
			return nil, "unsupported_configuration"
		}
	}
	return levels, ""
}

func previewAdd(base time.Time, duration time.Duration) (time.Time, error) {
	result := base.Add(duration).UTC()
	if base.Year() < 1 || base.Year() > 9999 || result.Year() < 1 || result.Year() > 9999 || result.Sub(base) != duration {
		return time.Time{}, ErrPreviewTime
	}
	return result, nil
}

func previewDifference(a, b time.Time) (time.Duration, error) {
	delta := a.Sub(b)
	if !b.Add(delta).Equal(a) {
		return 0, ErrPreviewTime
	}
	return delta, nil
}

// PreviewWindow validates the snapshot and bounds the rule query before any
// projection. It never consults a clock or a database.
func PreviewWindow(input PreviewInput) (time.Time, string) {
	a := input.Alert
	if !previewSeverity(a.Severity) || (a.Status != "open" && a.Status != "acked" && a.Status != "resolved") || a.LastLevelFired < -1 || (a.LastLevelFired >= 0 && uint64(a.LastLevelFired) > MaxPreviewID) || a.DeliveryReason == "feature_retired" || uint64(a.NodeID) > MaxPreviewID {
		return time.Time{}, "unsupported_alert_state"
	}
	if _, err := DecodePreviewTags(a.Tags); err != nil {
		return time.Time{}, "unsupported_alert_state"
	}
	if a.TriggeredAt.Year() < 1 || a.TriggeredAt.Year() > 9999 || input.EvaluationAt.Year() < 1 || input.EvaluationAt.Year() > 9999 {
		return time.Time{}, "unsupported_time"
	}
	if _, err := previewDifference(input.EvaluationAt, a.TriggeredAt); err != nil {
		return time.Time{}, "unsupported_time"
	}
	levels, warning := previewLevels(input.Policy)
	if warning != "" {
		return time.Time{}, warning
	}
	if input.Policy != nil && (uint64(input.Policy.ID) > MaxPreviewID || (input.Mode == "alert" && (input.Policy.UpdatedAt.Year() < 1 || input.Policy.UpdatedAt.Year() > 9999))) {
		return time.Time{}, "unsupported_configuration"
	}
	if input.SourceResourceID != nil && (*input.SourceResourceID == 0 || uint64(*input.SourceResourceID) > MaxPreviewID) {
		return time.Time{}, "unsupported_configuration"
	}
	end := input.EvaluationAt.UTC()
	for _, level := range levels {
		due, err := previewAdd(a.TriggeredAt, time.Duration(level.DelaySeconds)*time.Second)
		if err != nil {
			return time.Time{}, "unsupported_time"
		}
		if _, err := previewDifference(due, input.EvaluationAt); err != nil {
			return time.Time{}, "unsupported_time"
		}
		if due.After(end) {
			end = due
		}
	}
	end, err := previewAdd(end, 5*DefaultTickInterval)
	if err != nil {
		return time.Time{}, "unsupported_time"
	}
	if _, err := previewDifference(end, input.EvaluationAt); err != nil {
		return time.Time{}, "unsupported_time"
	}
	return end, ""
}

func unsupportedPreview(result PreviewResult, warning string) PreviewResult {
	result.Status = "unsupported_state"
	result.PolicySource = "none"
	result.SourceResourceID = nil
	result.Policy = nil
	result.Alert = nil
	result.NextLevelIndex = nil
	result.NextDueAt = nil
	result.NextDueNow = nil
	result.DeliveryDecision = nil
	result.DeliveryCounts = nil
	result.Steps = []PreviewStep{}
	if warning != "" {
		found := false
		for _, existing := range result.Warnings {
			if existing == warning {
				found = true
			}
		}
		if !found {
			result.Warnings = append(result.Warnings, warning)
		}
	}
	return result
}

// Preview describes possible committed level advancement, never sending or
// delivery. Input is a read snapshot, not an instruction for the real engine.
func Preview(ctx context.Context, input PreviewInput) (PreviewResult, error) {
	result := PreviewResult{Mode: input.Mode, Basis: "current_snapshot", GeneratedAt: input.GeneratedAt.UTC(), EvaluationAt: input.EvaluationAt.UTC(), TickSeconds: int(DefaultTickInterval / time.Second), PolicySource: input.Source, SourceResourceID: input.SourceResourceID, Warnings: append([]string{}, input.Warnings...), Steps: []PreviewStep{}}
	var err error
	result.ExpiresAt, err = previewAdd(result.GeneratedAt, time.Minute)
	if err != nil {
		return result, err
	}
	if input.Mode == "alert" {
		id := input.Alert.ID
		result.RequestedAlertID = &id
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if _, warning := PreviewWindow(input); warning != "" {
		return unsupportedPreview(result, warning), nil
	}
	for _, warning := range input.Warnings {
		if warning != "node_missing" && warning != "policy_changed_since_history" {
			return unsupportedPreview(result, ""), nil
		}
	}
	for _, silence := range input.Silences {
		if _, err := DecodePreviewTags(silence.MatchTags); err != nil {
			return unsupportedPreview(result, "unsupported_configuration"), nil
		}
		if silence.ID == 0 || uint64(silence.ID) > MaxPreviewID || (silence.MatchNodeID != nil && uint64(*silence.MatchNodeID) > MaxPreviewID) {
			return unsupportedPreview(result, "unsupported_configuration"), nil
		}
	}
	a := input.Alert
	tags, _ := DecodePreviewTags(a.Tags)
	result.Alert = &PreviewAlert{NodeID: a.NodeID, Severity: a.Severity, Status: a.Status, TriggeredAt: a.TriggeredAt.UTC(), LastLevelFired: a.LastLevelFired, Tags: tags}
	if input.Mode == "alert" {
		result.Alert.ID = result.RequestedAlertID
		decision := a.DeliveryDecision
		switch decision {
		case "pending", "direct", "suppressed", "escalated", "no_channel", "unknown":
		default:
			decision = "unknown"
		}
		result.DeliveryDecision = &decision
		result.DeliveryCounts = input.DeliveryCounts
	}
	if input.Policy != nil {
		p := input.Policy
		result.Policy = &PreviewPolicy{Name: p.Name, MinSeverity: p.MinSeverity, Enabled: p.Enabled}
		if input.Mode == "alert" {
			id := p.ID
			updated := p.UpdatedAt.UTC()
			result.Policy.ID = &id
			result.Policy.UpdatedAt = &updated
		}
	}
	if a.Status != "open" {
		result.Status = "not_open"
		return result, nil
	}
	evaluation := evaluateNextLevel(a, input.Policy, input.EvaluationAt)
	if evaluation.Reason != "ready" && evaluation.Reason != "waiting" {
		result.Status = evaluation.Reason
		return result, nil
	}
	result.Status = "projected"
	idx := evaluation.Index
	result.NextLevelIndex = &idx
	due, _ := previewAdd(a.TriggeredAt, time.Duration(evaluation.Level.DelaySeconds)*time.Second)
	result.NextDueAt = &due
	dueNow := evaluation.Reason == "ready"
	result.NextDueNow = &dueNow
	levels := input.Policy.DecodedLevels()
	metadata := make(map[uint]IntegrationMetadata, len(input.Integrations))
	for _, channel := range input.Integrations {
		metadata[channel.ID] = channel
	}
	scan := input.EvaluationAt.UTC()
	blocked := false
	for i := idx; i < len(levels); i++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		level := levels[i]
		due, _ := previewAdd(a.TriggeredAt, time.Duration(level.DelaySeconds)*time.Second)
		step := PreviewStep{LevelIndex: i, DelaySeconds: level.DelaySeconds, DueAt: due, SeverityBefore: a.Severity, SeverityAfter: a.Severity, TagsAfter: tags, MatchedSilenceIDs: []uint{}, Channels: []PreviewChannel{}}
		seen := make(map[uint]bool, len(level.IntegrationIDs))
		enabled := false
		for _, id := range level.IntegrationIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			channel := PreviewChannel{ID: id, State: "missing"}
			if meta, ok := metadata[id]; ok {
				channel.Name = &meta.Name
				channel.Type = &meta.Type
				channel.State = "disabled"
				if meta.Enabled {
					channel.State = "enabled"
					enabled = true
				}
			}
			step.Channels = append(step.Channels, channel)
		}
		if blocked {
			step.Status = "blocked_prior"
		} else if !SeverityAtLeast(a.Severity, input.Policy.MinSeverity) {
			step.Status = "blocked_minimum"
			blocked = true
		} else {
			if due.After(scan) {
				delta, err := previewDifference(due, input.EvaluationAt)
				if err != nil {
					return unsupportedPreview(result, "unsupported_time"), nil
				}
				grid := delta / DefaultTickInterval
				if delta%DefaultTickInterval != 0 {
					grid++
				}
				if grid > time.Duration(math.MaxInt64)/DefaultTickInterval {
					return unsupportedPreview(result, "unsupported_time"), nil
				}
				scan, err = previewAdd(input.EvaluationAt, grid*DefaultTickInterval)
				if err != nil {
					return unsupportedPreview(result, "unsupported_time"), nil
				}
			}
			if _, err := previewDifference(scan, a.TriggeredAt); err != nil {
				return unsupportedPreview(result, "unsupported_time"), nil
			}
			projected := scan
			step.ProjectedAt = &projected
			projection := projectLevel(a, level)
			step.SeverityAfter = projection.Severity
			step.TagsAfter = projection.Tags
			a.Severity = projection.Severity
			tags = projection.Tags
			encoded, _ := json.Marshal(tags)
			a.Tags = string(encoded)
			a.LastLevelFired = i
			for j := range input.Silences {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				if alerting.MatchSilence(a, input.Node, input.Silences[j:j+1], scan) != nil {
					step.MatchedSilenceIDs = append(step.MatchedSilenceIDs, input.Silences[j].ID)
				}
			}
			sort.Slice(step.MatchedSilenceIDs, func(i, j int) bool { return step.MatchedSilenceIDs[i] < step.MatchedSilenceIDs[j] })
			step.Status = "no_enabled_channels"
			if enabled {
				step.Status = "would_enqueue"
			}
			if len(step.MatchedSilenceIDs) > 0 {
				step.Status = "silenced"
			}
			if i+1 < len(levels) {
				scan, err = previewAdd(scan, DefaultTickInterval)
				if err != nil {
					return unsupportedPreview(result, "unsupported_time"), nil
				}
			}
		}
		result.Steps = append(result.Steps, step)
	}
	return result, nil
}
