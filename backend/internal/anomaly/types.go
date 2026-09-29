package anomaly

import "context"

// Finding describes one anomaly produced by the retained snapshot-diff detector.
// The Raise function always turns it into an AnomalyEvent row, and may also
// promote it to an Alert.
type Finding struct {
	NodeID        uint
	Detector      string // "snapshot_diff"
	Metric        string // "snapshot_churn" | "ransomware_pattern"
	Severity      string // "warning" | "critical"
	ObservedValue float64
	BaselineValue float64
	Sigma         *float64 // populated by snapshot_diff
	ErrorCode     string   // e.g. "XR-SNAPSHOT-CHURN-5"
	Message       string
	Details       map[string]any // JSON-encoded into events.details
}

// AlertSink persists findings and optionally promotes them as alerts.
// It is retained for task-run snapshot-diff detection.
type AlertSink interface {
	Raise(ctx context.Context, f Finding) error
}

// RaiseFn persists a finding via anomaly_events and optional alert promotion.
// Injected by main.go to avoid import cycle with alerting + model.
type RaiseFn func(ctx context.Context, f Finding) error
