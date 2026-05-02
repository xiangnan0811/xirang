package anomaly

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors.
var (
	ErrInvalidInput = errors.New("invalid input")
)

// Finding describes one anomaly produced by a detector. The Raise function
// always turns it into an AnomalyEvent row, and may also promote it to an Alert.
type Finding struct {
	NodeID        uint
	Detector      string // "ewma" | "disk_forecast"
	Metric        string // "cpu_pct" | "mem_pct" | "load_1m" | "disk_pct"
	Severity      string // "warning" | "critical"
	ObservedValue float64
	BaselineValue float64
	Sigma         *float64 // populated by EWMA
	ForecastDays  *float64 // populated by disk_forecast
	ErrorCode     string   // e.g. "XR-ANOMALY-CPU-5"
	Message       string
	Details       map[string]any // JSON-encoded into events.details
}

// Detector is a tickable anomaly detection strategy. Implementations must be
// safe to call from a long-running goroutine.
type Detector interface {
	Name() string
	TickInterval() time.Duration
	Evaluate(ctx context.Context) ([]Finding, error)
}

// RaiseFn persists a finding via anomaly_events and optional alert promotion.
// Injected by main.go to avoid import cycle with alerting + model.
type RaiseFn func(ctx context.Context, f Finding) error
