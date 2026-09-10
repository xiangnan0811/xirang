package model

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// Alert delivery decisions are persisted separately from the alert status.
	// They describe why an alert has (or has not) acquired channel intents and
	// therefore make replay safe after a process restart.
	AlertDeliveryDecisionPending    = "pending"
	AlertDeliveryDecisionDirect     = "direct"
	AlertDeliveryDecisionSuppressed = "suppressed"
	AlertDeliveryDecisionEscalated  = "escalated"
	AlertDeliveryDecisionNoChannel  = "no_channel"

	AlertDeliveryReasonSilence             = "silence"
	AlertDeliveryReasonGrouping            = "grouping"
	AlertDeliveryReasonThresholdOrCooldown = "threshold_or_cooldown"
	AlertDeliveryReasonNoEnabledChannel    = "no_enabled_channel"
	AlertDeliveryReasonEscalation          = "escalation"
)

const (
	AlertDeliveryStatusPending  = "pending"
	AlertDeliveryStatusSending  = "sending"
	AlertDeliveryStatusSent     = "sent"
	AlertDeliveryStatusRetrying = "retrying"
	AlertDeliveryStatusFailed   = "failed"
)

type Alert struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	NodeID         uint       `gorm:"not null;index:idx_alerts_dedup" json:"node_id"`
	NodeName       string     `gorm:"size:128;not null" json:"node_name"`
	TaskID         *uint      `gorm:"index" json:"task_id"`
	TaskRunID      *uint      `gorm:"index" json:"task_run_id,omitempty"`
	SLOID          *uint      `gorm:"index" json:"slo_id,omitempty"`
	PolicyName     string     `gorm:"size:128" json:"policy_name"`
	Severity       string     `gorm:"size:16;not null;index" json:"severity"`
	Status         string     `gorm:"size:16;not null;index" json:"status"`
	ErrorCode      string     `gorm:"size:64;not null;index:idx_alerts_dedup" json:"error_code"`
	Message        string     `gorm:"type:text;not null" json:"message"`
	Retryable      bool       `gorm:"not null;default:false" json:"retryable"`
	TriggeredAt    time.Time  `gorm:"index" json:"triggered_at"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`
	Tags           string     `gorm:"type:text;not null;default:'[]'" json:"tags"`
	LastLevelFired int        `gorm:"not null;default:-1" json:"last_level_fired"`

	// DeliveryDecision is intentionally independent of Alert.Status. A resolved
	// alert may still have an in-flight delivery intent, while a silenced or
	// escalated alert must not be rediscovered as a direct delivery on replay.
	DeliveryDecision  string     `gorm:"size:32" json:"delivery_decision,omitempty"`
	DeliveryReason    string     `gorm:"size:64" json:"delivery_reason,omitempty"`
	DeliveryDecidedAt *time.Time `json:"delivery_decided_at,omitempty"`

	CreatedAt time.Time `gorm:"index:idx_alerts_dedup" json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DecodedTags returns the parsed tags; empty on invalid.
func (a *Alert) DecodedTags() []string {
	var tags []string
	if strings.TrimSpace(a.Tags) == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(a.Tags), &tags)
	return tags
}

type AlertDelivery struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	AlertID       uint   `gorm:"index;not null" json:"alert_id"`
	IntegrationID uint   `gorm:"index;not null" json:"integration_id"`
	Status        string `gorm:"size:16;not null" json:"status"` // pending|sending|sent|retrying|failed
	// Decision is normally "deliver". It is kept on the row so a future
	// non-delivery intent can be represented without making an absent row
	// ambiguous. Legacy rows with an empty value are treated as deliverable.
	Decision       string     `gorm:"size:16;not null;default:'deliver'" json:"decision"`
	DeliveryKey    string     `gorm:"size:96" json:"-"`
	AttemptCount   int        `gorm:"not null;default:0" json:"attempt_count"`
	AttemptID      string     `gorm:"size:64" json:"-"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at"`
	LastError      string     `gorm:"type:text" json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Silence 告警静默规则：在指定时间窗口内抑制匹配的告警
type Silence struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Name          string    `gorm:"size:128;not null" json:"name"`
	MatchNodeID   *uint     `gorm:"index" json:"match_node_id"`
	MatchCategory string    `gorm:"size:64;index" json:"match_category"`
	MatchTags     string    `gorm:"type:text" json:"match_tags"` // JSON-encoded []string
	StartsAt      time.Time `gorm:"not null;index:idx_silences_active" json:"starts_at"`
	EndsAt        time.Time `gorm:"not null;index:idx_silences_active;index:idx_silences_cleanup" json:"ends_at"`
	CreatedBy     uint      `gorm:"not null" json:"created_by"`
	Note          string    `gorm:"type:text" json:"note"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DecodedMatchTags 返回解析后的标签列表（JSON 为空或无效时返回 nil）。
func (s *Silence) DecodedMatchTags() []string {
	if strings.TrimSpace(s.MatchTags) == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(s.MatchTags), &tags); err != nil {
		return nil
	}
	return tags
}

// EscalationPolicy defines a chain of 1-5 escalation levels.
type EscalationPolicy struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:100;not null;uniqueIndex:uk_escalation_policies_name" json:"name"`
	Description string    `gorm:"type:text;not null;default:''" json:"description"`
	MinSeverity string    `gorm:"size:16;not null" json:"min_severity"`
	Enabled     bool      `gorm:"not null;default:true" json:"enabled"`
	Levels      string    `gorm:"type:text;not null" json:"levels"` // JSON-encoded []EscalationLevel
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// EscalationLevel is a single step in a policy's chain.
type EscalationLevel struct {
	DelaySeconds     int      `json:"delay_seconds"`
	IntegrationIDs   []uint   `json:"integration_ids"`
	SeverityOverride string   `json:"severity_override"`
	Tags             []string `json:"tags"`
}

// DecodedLevels returns the parsed levels; empty slice on invalid JSON.
func (p *EscalationPolicy) DecodedLevels() []EscalationLevel {
	var levels []EscalationLevel
	s := strings.TrimSpace(p.Levels)
	if s == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(s), &levels)
	return levels
}

// AlertEscalationEvent records one level firing (real or silenced-skip).
type AlertEscalationEvent struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	AlertID            uint      `gorm:"not null;uniqueIndex:uk_escalation_events_alert_level,priority:1" json:"alert_id"`
	EscalationPolicyID *uint     `json:"escalation_policy_id"`
	LevelIndex         int       `gorm:"not null;uniqueIndex:uk_escalation_events_alert_level,priority:2" json:"level_index"`
	IntegrationIDs     string    `gorm:"type:text;not null;default:'[]'" json:"integration_ids"`
	SeverityBefore     string    `gorm:"size:16;not null" json:"severity_before"`
	SeverityAfter      string    `gorm:"size:16;not null" json:"severity_after"`
	TagsAdded          string    `gorm:"type:text;not null;default:'[]'" json:"tags_added"`
	FiredAt            time.Time `gorm:"not null" json:"fired_at"`
}

// DecodedIntegrationIDs returns the parsed snapshot; empty slice on invalid JSON.
func (e *AlertEscalationEvent) DecodedIntegrationIDs() []uint {
	var ids []uint
	if strings.TrimSpace(e.IntegrationIDs) == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(e.IntegrationIDs), &ids)
	return ids
}

// DecodedTagsAdded returns the parsed tags snapshot.
func (e *AlertEscalationEvent) DecodedTagsAdded() []string {
	var tags []string
	if strings.TrimSpace(e.TagsAdded) == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(e.TagsAdded), &tags)
	return tags
}
