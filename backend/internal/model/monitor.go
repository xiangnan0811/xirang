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
	MetricType         string    `gorm:"size:32;not null" json:"metric_type"` // success_rate | availability
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

// Dashboard is a user-owned collection of panels.
type Dashboard struct {
	ID                 uint             `gorm:"primaryKey" json:"id"`
	OwnerID            uint             `gorm:"not null;uniqueIndex:uk_dashboards_owner_name,priority:1" json:"owner_id"`
	Name               string           `gorm:"size:100;not null;uniqueIndex:uk_dashboards_owner_name,priority:2" json:"name"`
	Description        string           `gorm:"type:text;not null;default:''" json:"description"`
	TimeRange          string           `gorm:"size:16;not null;default:'1h'" json:"time_range"`
	CustomStart        *time.Time       `json:"custom_start,omitempty"`
	CustomEnd          *time.Time       `json:"custom_end,omitempty"`
	AutoRefreshSeconds int              `gorm:"not null;default:30" json:"auto_refresh_seconds"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
	Panels             []DashboardPanel `gorm:"foreignKey:DashboardID;constraint:OnDelete:CASCADE" json:"panels,omitempty"`
}

// DashboardPanel is a single chart configuration inside a dashboard.
// Filters is stored as a JSON string in the DB but serialized as a structured
// object on the wire (see MarshalJSON). This keeps the TS contract clean and
// lets the frontend round-trip filters through `panel-query` without re-encoding.
type DashboardPanel struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	DashboardID uint      `gorm:"not null;index:idx_dashboard_panels_dashboard" json:"dashboard_id"`
	Title       string    `gorm:"size:100;not null" json:"title"`
	ChartType   string    `gorm:"size:16;not null" json:"chart_type"`
	Metric      string    `gorm:"size:32;not null" json:"metric"`
	Filters     string    `gorm:"type:text;not null;default:'{}'" json:"-"`
	Aggregation string    `gorm:"size:16;not null" json:"aggregation"`
	LayoutX     int       `gorm:"not null;default:0" json:"layout_x"`
	LayoutY     int       `gorm:"not null;default:0" json:"layout_y"`
	LayoutW     int       `gorm:"not null;default:6" json:"layout_w"`
	LayoutH     int       `gorm:"not null;default:4" json:"layout_h"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PanelFilters is the decoded shape of DashboardPanel.Filters.
type PanelFilters struct {
	NodeIDs []uint `json:"node_ids,omitempty"`
	TaskIDs []uint `json:"task_ids,omitempty"`
}

// DecodedFilters returns the parsed filters; zero-value PanelFilters on empty/invalid JSON.
func (p *DashboardPanel) DecodedFilters() PanelFilters {
	var f PanelFilters
	s := strings.TrimSpace(p.Filters)
	if s == "" {
		return f
	}
	_ = json.Unmarshal([]byte(s), &f)
	return f
}

// MarshalJSON emits `filters` as the decoded object so clients don't need to
// re-parse a JSON string when round-tripping through /dashboards/panel-query.
func (p DashboardPanel) MarshalJSON() ([]byte, error) {
	type alias DashboardPanel
	return json.Marshal(&struct {
		alias
		Filters PanelFilters `json:"filters"`
	}{
		alias:   alias(p),
		Filters: p.DecodedFilters(),
	})
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
	ForecastDays  *float64  `json:"forecast_days,omitempty"`
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
	HTTPHeaders        string     `gorm:"type:text;not null;default:'{}'" json:"http_headers"` // JSON
	Enabled            bool       `gorm:"not null;default:true" json:"enabled"`
	LastStatus         string     `gorm:"size:8;not null;default:'unknown'" json:"last_status"` // "up"|"down"|"unknown"
	UptimePct          float64    `gorm:"not null;default:0" json:"uptime_pct"`                 // trailing 24h
	LastCheckedAt      *time.Time `json:"last_checked_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// ServiceUptimeSample records hourly probe aggregation for a ServiceMonitor.
type ServiceUptimeSample struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	MonitorID  uint      `gorm:"not null;index:idx_sus_monitor_hour,unique" json:"monitor_id"`
	Hour       time.Time `gorm:"not null;index:idx_sus_monitor_hour,unique" json:"hour"` // truncated to hour
	ProbeCount int       `gorm:"not null;default:0" json:"probe_count"`
	ProbeOK    int       `gorm:"not null;default:0" json:"probe_ok"`
}
