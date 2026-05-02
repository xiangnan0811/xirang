package anomaly

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"gorm.io/gorm"
)

// ewmaEvaluateErrors counts per-metric evaluate failures so a rising error
// rate shows up in dashboards even though individual errors log at Debug.
var ewmaEvaluateErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "xirang_anomaly_ewma_evaluate_errors_total",
	Help: "Count of EWMADetector.evaluateNodeMetric failures, labeled by metric key.",
}, []string{"metric"})

// EWMADetector evaluates CPU/mem/load EWMA anomaly for each node every tick.
type EWMADetector struct {
	db       *gorm.DB
	settings *settings.Service
	nowFn    func() time.Time // test hook
}

// NewEWMADetector constructs a detector bound to db + settings.
func NewEWMADetector(db *gorm.DB, s *settings.Service) *EWMADetector {
	return &EWMADetector{db: db, settings: s, nowFn: time.Now}
}

// SetNowFn overrides time for deterministic tests.
func (d *EWMADetector) SetNowFn(fn func() time.Time) { d.nowFn = fn }

func (*EWMADetector) Name() string                { return "ewma" }
func (*EWMADetector) TickInterval() time.Duration { return 5 * time.Minute }

// metrics EWMA evaluates — disk_pct intentionally excluded (handled by DiskForecastDetector).
var ewmaMetrics = []struct {
	Key        string
	Column     string
	UpperLabel string
}{
	{"cpu_pct", "cpu_pct", "CPU"},
	{"mem_pct", "mem_pct", "MEM"},
	{"load_1m", "load_1m", "LOAD"},
}

// Evaluate scans all non-archived nodes' probe_ok samples in the window and
// emits Findings for metrics that exceed baseline + k·σ.
func (d *EWMADetector) Evaluate(ctx context.Context) ([]Finding, error) {
	if !d.settingsBool("anomaly.enabled", true) {
		return nil, nil
	}

	alpha := d.settingsFloat("anomaly.ewma_alpha", 0.3)
	sigmaK := d.settingsFloat("anomaly.ewma_sigma", 5.0)
	windowHours := d.settingsInt("anomaly.ewma_window_hours", 6)
	minSamples := d.settingsInt("anomaly.ewma_min_samples", 24)

	if alpha <= 0 || alpha >= 1 || sigmaK <= 0 || windowHours <= 0 || minSamples <= 1 {
		return nil, fmt.Errorf("%w: invalid anomaly settings", ErrInvalidInput)
	}

	var nodes []model.Node
	if err := d.db.WithContext(ctx).
		Select("id, name").
		Where("archived = ?", false).
		Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("load nodes: %w", err)
	}

	now := d.nowFn()
	since := now.Add(-time.Duration(windowHours) * time.Hour)

	var findings []Finding
	for _, n := range nodes {
		for _, m := range ewmaMetrics {
			f, err := d.evaluateNodeMetric(ctx, n, m.Key, m.Column, m.UpperLabel, alpha, sigmaK, since, minSamples)
			if err != nil {
				// One flaky node must not flood the log. Log at Debug for
				// forensic traces and let the counter drive alerts.
				ewmaEvaluateErrors.WithLabelValues(m.Key).Inc()
				logger.Module("anomaly").Debug().
					Uint("node_id", n.ID).
					Str("metric", m.Key).
					Err(err).
					Msg("ewma: evaluate failed")
				continue
			}
			if f != nil {
				findings = append(findings, *f)
			}
		}
	}
	return findings, nil
}

type rawSample struct {
	Value     float64   `gorm:"column:value"`
	SampledAt time.Time `gorm:"column:sampled_at"`
}

func (d *EWMADetector) evaluateNodeMetric(ctx context.Context, node model.Node,
	metricKey, column, upperLabel string, alpha, sigmaK float64, since time.Time, minSamples int) (*Finding, error) {

	var rows []rawSample
	query := fmt.Sprintf("SELECT %s AS value, sampled_at FROM node_metric_samples "+
		"WHERE node_id = ? AND sampled_at >= ? AND probe_ok = ? ORDER BY sampled_at ASC", column)
	if err := d.db.WithContext(ctx).Raw(query, node.ID, since, true).Scan(&rows).Error; err != nil {
		return nil, err
	}
	// Filter NaN/Inf defensively. The prober should already sanitize, but a
	// single polluted row here would propagate through EWMA mean → detector
	// stays silent (NaN comparisons are always false) or false-fires forever.
	cleaned := rows[:0]
	for _, r := range rows {
		if !math.IsNaN(r.Value) && !math.IsInf(r.Value, 0) {
			cleaned = append(cleaned, r)
		}
	}
	rows = cleaned
	if len(rows) < minSamples {
		return nil, nil
	}

	// Last sample = observed; prior samples = baseline history
	observed := rows[len(rows)-1].Value
	hist := make([]float64, 0, len(rows)-1)
	for i := 0; i < len(rows)-1; i++ {
		hist = append(hist, rows[i].Value)
	}
	mean, stddev := EWMAMeanStddev(hist, alpha)
	if stddev <= 0 {
		return nil, nil // constant series — no useful baseline
	}

	deviation := observed - mean
	if deviation < 0 {
		deviation = -deviation
	}
	threshold := sigmaK * stddev
	if deviation <= threshold {
		return nil, nil
	}

	severity := "warning"
	if deviation > 2*threshold {
		severity = "critical"
	}

	sigmaRatio := deviation / stddev
	errorCode := fmt.Sprintf("XR-ANOMALY-%s-%d", upperLabel, node.ID)
	msg := fmt.Sprintf("节点 %s 的 %s 指标偏离基线 %.2fσ（当前 %.2f，基线 %.2f）",
		node.Name, metricKey, sigmaRatio, observed, mean)

	f := Finding{
		NodeID:        node.ID,
		Detector:      "ewma",
		Metric:        metricKey,
		Severity:      severity,
		ObservedValue: observed,
		BaselineValue: mean,
		Sigma:         &sigmaRatio,
		ErrorCode:     errorCode,
		Message:       msg,
		Details: map[string]any{
			"samples":      len(rows),
			"window_hours": settingsWindowHint(since, d.nowFn()),
			"stddev":       stddev,
			"threshold_k":  sigmaK,
			"alpha":        alpha,
		},
	}
	return &f, nil
}

// ---- settings helpers (fall back to defaults on parse errors) ----

func (d *EWMADetector) settingsBool(key string, fallback bool) bool {
	v := strings.TrimSpace(d.settings.GetEffective(key))
	if v == "" {
		return fallback
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return fallback
}

func (d *EWMADetector) settingsInt(key string, fallback int) int {
	v := strings.TrimSpace(d.settings.GetEffective(key))
	if v == "" {
		return fallback
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return fallback
}

func (d *EWMADetector) settingsFloat(key string, fallback float64) float64 {
	v := strings.TrimSpace(d.settings.GetEffective(key))
	if v == "" {
		return fallback
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return fallback
}

// settingsWindowHint returns the window width in hours (diagnostic only).
func settingsWindowHint(since, now time.Time) float64 {
	return now.Sub(since).Hours()
}
