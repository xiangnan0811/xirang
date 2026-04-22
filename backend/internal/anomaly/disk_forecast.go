package anomaly

import (
	"context"
	"fmt"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// DiskForecastDetector runs hourly: linear-regression disk_pct_avg over the
// last 14 days and alerts when projected days-until-full ≤ threshold.
type DiskForecastDetector struct {
	db       *gorm.DB
	settings *settings.Service
	nowFn    func() time.Time
}

func NewDiskForecastDetector(db *gorm.DB, s *settings.Service) *DiskForecastDetector {
	return &DiskForecastDetector{db: db, settings: s, nowFn: time.Now}
}

func (d *DiskForecastDetector) SetNowFn(fn func() time.Time) { d.nowFn = fn }

func (*DiskForecastDetector) Name() string                { return "disk_forecast" }
func (*DiskForecastDetector) TickInterval() time.Duration { return 1 * time.Hour }

// minR2 is the goodness-of-fit floor below which the prediction is considered
// too noisy to act on.
const diskForecastMinR2 = 0.5

// historyDaysCap bounds the regression input window to 14 days of hourly buckets.
const diskForecastHistoryDays = 14

func (d *DiskForecastDetector) Evaluate(ctx context.Context) ([]Finding, error) {
	ewma := EWMADetector{settings: d.settings}
	if !ewma.settingsBool("anomaly.enabled", true) {
		return nil, nil
	}

	threshold := ewma.settingsInt("anomaly.disk_forecast_days", 7)
	minHistoryHours := ewma.settingsInt("anomaly.disk_forecast_min_history_hours", 72)
	if threshold <= 0 || minHistoryHours <= 0 {
		return nil, fmt.Errorf("%w: invalid disk forecast settings", ErrInvalidInput)
	}

	var nodes []model.Node
	if err := d.db.WithContext(ctx).
		Select("id, name").
		Where("archived = ?", false).
		Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("load nodes: %w", err)
	}

	now := d.nowFn()
	since := now.Add(-time.Duration(diskForecastHistoryDays) * 24 * time.Hour)

	var findings []Finding
	for _, n := range nodes {
		f := d.evaluateNode(ctx, n, since, now, threshold, minHistoryHours)
		if f != nil {
			findings = append(findings, *f)
		}
	}
	return findings, nil
}

func (d *DiskForecastDetector) evaluateNode(ctx context.Context, node model.Node,
	since, now time.Time, thresholdDays, minHistoryHours int) *Finding {

	var rows []model.NodeMetricSampleHourly
	if err := d.db.WithContext(ctx).
		Where("node_id = ? AND bucket_start >= ?", node.ID, since).
		Order("bucket_start ASC").
		Find(&rows).Error; err != nil {
		return nil
	}

	xs := make([]float64, 0, len(rows))
	ys := make([]float64, 0, len(rows))
	for _, r := range rows {
		if r.DiskPctAvg == nil {
			continue
		}
		xs = append(xs, float64(r.BucketStart.Unix()))
		ys = append(ys, *r.DiskPctAvg)
	}
	if len(xs) < minHistoryHours {
		return nil
	}

	slope, intercept, r2 := LinearRegression(xs, ys)
	if slope <= 0 {
		return nil
	}
	if r2 < diskForecastMinR2 {
		return nil
	}

	currentY := intercept + slope*float64(now.Unix())
	if currentY >= 100 {
		return nil // already full; threshold alert will cover
	}

	secondsToFull := (100 - currentY) / slope
	if secondsToFull <= 0 {
		return nil
	}
	daysToFull := secondsToFull / 86400
	if daysToFull > float64(thresholdDays) {
		return nil
	}

	severity := "warning"
	if daysToFull <= 3 {
		severity = "critical"
	}

	errorCode := fmt.Sprintf("XR-DISKFORECAST-%d", node.ID)
	msg := fmt.Sprintf("节点 %s 磁盘预计 %.1f 天后爆满（当前 %.1f%%，斜率 %.3f/天）",
		node.Name, daysToFull, currentY, slope*86400)

	forecast := daysToFull
	return &Finding{
		NodeID:        node.ID,
		Detector:      "disk_forecast",
		Metric:        "disk_pct",
		Severity:      severity,
		ObservedValue: currentY,
		BaselineValue: ys[0], // earliest observation in the window
		ForecastDays:  &forecast,
		ErrorCode:     errorCode,
		Message:       msg,
		Details: map[string]any{
			"samples_used":   len(xs),
			"history_span_h": len(xs),
			"slope_per_day":  slope * 86400,
			"r2":             r2,
			"threshold_days": thresholdDays,
		},
	}
}
