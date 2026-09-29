package model

import (
	"encoding/json"
	"strings"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
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

// ServiceMonitor is an HTTP/TCP uptime probe target. Probes run from the Xirang
// server itself (no SSH), collecting uptime samples into service_uptime_samples.
//
// HTTPHeaders is deliberately excluded from JSON. API handlers expose only the
// header names and whether any headers are configured; the value is decrypted
// only inside the probe worker.
type ServiceMonitor struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Name               string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Description        string     `gorm:"size:255" json:"description"`
	Type               string     `gorm:"size:16;not null" json:"type"`    // "http" | "tcp"
	Target             string     `gorm:"size:512;not null" json:"target"` // URL or host:port
	IntervalSeconds    int        `gorm:"not null;default:60" json:"interval_seconds"`
	TimeoutSeconds     int        `gorm:"not null;default:10" json:"timeout_seconds"`
	HTTPMethod         string     `gorm:"size:8;not null;default:'GET'" json:"http_method"`
	HTTPExpectedStatus int        `gorm:"not null;default:200" json:"http_expected_status"`
	HTTPHeaders        string     `gorm:"type:text;not null;default:'{}'" json:"-"` // encrypted JSON
	Enabled            bool       `gorm:"not null;default:true" json:"enabled"`
	LastStatus         string     `gorm:"size:8;not null;default:'unknown'" json:"last_status"` // "up"|"down"|"unknown"
	UptimePct          float64    `gorm:"not null;default:0" json:"uptime_pct"`                 // trailing 24h
	LastCheckedAt      *time.Time `json:"last_checked_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (m *ServiceMonitor) BeforeSave(tx *gorm.DB) error {
	if !serviceMonitorHeadersSelected(tx) {
		return nil
	}
	if m.HTTPHeaders == "" {
		m.HTTPHeaders = "{}"
	}
	if secure.IsEncrypted(m.HTTPHeaders) {
		return nil
	}
	encrypted, err := secure.EncryptString(m.HTTPHeaders)
	if err != nil {
		return err
	}
	m.HTTPHeaders = encrypted
	return nil
}

func serviceMonitorHeadersSelected(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return true
	}
	columns, restricted := tx.Statement.SelectAndOmitColumns(false, true)
	if selected, ok := columns["http_headers"]; ok {
		return selected
	}
	return !restricted
}

func (m *ServiceMonitor) AfterFind(_ *gorm.DB) error {
	if m.HTTPHeaders == "" {
		return nil
	}
	decrypted, err := secure.DecryptIfNeeded(m.HTTPHeaders)
	if err != nil {
		return err
	}
	m.HTTPHeaders = decrypted
	return nil
}

// ServiceUptimeSample records hourly probe aggregation for a ServiceMonitor.
type ServiceUptimeSample struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	MonitorID  uint      `gorm:"not null;index:idx_sus_monitor_hour,unique" json:"monitor_id"`
	Hour       time.Time `gorm:"not null;index:idx_sus_monitor_hour,unique" json:"hour"` // truncated to hour
	ProbeCount int       `gorm:"not null;default:0" json:"probe_count"`
	ProbeOK    int       `gorm:"not null;default:0" json:"probe_ok"`
}
