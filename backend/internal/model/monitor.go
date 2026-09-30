package model

import (
	"encoding/json"
	"strings"
	"time"
)

// SLODefinition is a service-level objective target, matched by node tags.
type SLODefinition struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	Name               string    `gorm:"size:128;not null" json:"name"`
	MetricType         string    `gorm:"size:32;not null" json:"metric_type"` // success_rate
	MatchTags          string    `gorm:"type:text" json:"match_tags"`         // JSON-encoded []string (nil = all)
	Threshold          float64   `gorm:"not null" json:"threshold"`           // 0–1 range
	WindowDays         int       `gorm:"not null;default:28" json:"window_days"`
	Enabled            bool      `gorm:"not null;default:true;index" json:"enabled"`
	EscalationPolicyID *uint     `gorm:"index" json:"escalation_policy_id"`
	CreatedBy          uint      `gorm:"not null" json:"created_by"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// DecodedMatchTags returns the parsed tag list. Returns nil on NULL/empty/invalid JSON.
func (s *SLODefinition) DecodedMatchTags() []string {
	if strings.TrimSpace(s.MatchTags) == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(s.MatchTags), &tags); err != nil {
		return nil
	}
	return tags
}

// AnomalyEvent records one detector finding; written whether or not a new
// alert was raised (dedup hits still persist an event with RaisedAlert=false).
type AnomalyEvent struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	NodeID        uint      `gorm:"not null;index:idx_anomaly_events_node_fired,priority:1" json:"node_id"`
	Detector      string    `gorm:"size:32;not null;index:idx_anomaly_events_detector_fired,priority:1" json:"detector"`
	Metric        string    `gorm:"size:32;not null" json:"metric"`
	Severity      string    `gorm:"size:16;not null" json:"severity"`
	ObservedValue float64   `gorm:"not null" json:"observed_value"`
	BaselineValue float64   `gorm:"not null" json:"baseline_value"`
	Sigma         *float64  `json:"sigma,omitempty"`
	AlertID       *uint     `json:"alert_id,omitempty"`
	RaisedAlert   bool      `gorm:"not null;default:false" json:"raised_alert"`
	Details       string    `gorm:"type:text;not null;default:'{}'" json:"details"`
	FiredAt       time.Time `gorm:"not null;index:idx_anomaly_events_node_fired,priority:2,sort:desc;index:idx_anomaly_events_detector_fired,priority:2,sort:desc" json:"fired_at"`
}

// DecodedDetails returns the parsed details map; empty on invalid JSON.
func (e *AnomalyEvent) DecodedDetails() map[string]any {
	out := map[string]any{}
	s := strings.TrimSpace(e.Details)
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
