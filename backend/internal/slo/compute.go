package slo

import (
	"strings"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// Compute evaluates an SLO at time `now`.
func Compute(db *gorm.DB, def *model.SLODefinition, now time.Time) (*Compliance, error) {
	windowStart := now.AddDate(0, 0, -def.WindowDays)
	base := &Compliance{
		SLOID:       def.ID,
		Name:        def.Name,
		MetricType:  def.MetricType,
		WindowStart: windowStart,
		WindowEnd:   now,
		Threshold:   def.Threshold,
	}
	switch def.MetricType {
	case "availability":
		return computeAvailability(db, def, base, now)
	case "success_rate":
		return computeSuccessRate(db, def, base, now)
	default:
		base.Status = StatusInsufficient
		return base, nil
	}
}

// resolveNodeIDs returns node IDs matching def.MatchTags (any-of). Empty tags = all nodes.
// Projects only id and tags to avoid triggering GORM AfterFind hooks that decrypt
// sensitive credentials (password, private_key) — unnecessary for SLO computation.
func resolveNodeIDs(db *gorm.DB, def *model.SLODefinition) ([]uint, error) {
	type nodeTagRow struct {
		ID   uint   `gorm:"column:id"`
		Tags string `gorm:"column:tags"`
	}
	var rows []nodeTagRow
	if err := db.Table("nodes").Select("id, tags").Find(&rows).Error; err != nil {
		return nil, err
	}
	wanted := def.DecodedMatchTags()
	if len(wanted) == 0 {
		ids := make([]uint, len(rows))
		for i, n := range rows {
			ids[i] = n.ID
		}
		return ids, nil
	}
	var out []uint
	for _, n := range rows {
		if anyTagMatch(wanted, splitCSVTags(n.Tags)) {
			out = append(out, n.ID)
		}
	}
	return out, nil
}

func splitCSVTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func anyTagMatch(wanted, have []string) bool {
	idx := make(map[string]struct{}, len(have))
	for _, t := range have {
		idx[t] = struct{}{}
	}
	for _, t := range wanted {
		if _, ok := idx[t]; ok {
			return true
		}
	}
	return false
}

type aggRow struct {
	OK    int64
	Total int64
}

func computeAvailability(db *gorm.DB, def *model.SLODefinition, base *Compliance, now time.Time) (*Compliance, error) {
	nodeIDs, err := resolveNodeIDs(db, def)
	if err != nil {
		return nil, err
	}
	if len(nodeIDs) == 0 {
		base.Status = StatusInsufficient
		return base, nil
	}
	var win aggRow
	if err := db.Table("node_metric_samples_hourly").
		Select("COALESCE(SUM(probe_ok),0) AS ok, COALESCE(SUM(probe_ok + probe_fail),0) AS total").
		Where("node_id IN ? AND bucket_start >= ? AND bucket_start < ?", nodeIDs, base.WindowStart, now).
		Scan(&win).Error; err != nil {
		return nil, err
	}
	// 1h burn rate reads raw samples, not the hourly rollup. The hourly
	// aggregator runs with a 5-minute cushion (see metrics/aggregator.go
	// catchUpHourly: end = now - 5min), so at any moment the most recent
	// ~60 minutes live only in the raw table. Querying hourly for "last
	// hour" returns 0-1 buckets; safeRatio on such small totals amplifies
	// per-sample noise above BreachAlertTriggerThreshold=2.0.
	var hour aggRow
	if err := db.Table("node_metric_samples").
		Select("COALESCE(SUM(CASE WHEN probe_ok THEN 1 ELSE 0 END),0) AS ok, COUNT(*) AS total").
		Where("node_id IN ? AND sampled_at >= ? AND sampled_at < ?", nodeIDs, now.Add(-time.Hour), now).
		Scan(&hour).Error; err != nil {
		return nil, err
	}
	base.Observed = safeRatio(win.OK, win.Total)
	base.SampleCount = int(win.Total)
	// Require a minimum raw-sample count (~5 min at 30s cadence) before
	// computing BurnRate1h. Below that the denominator is too small to
	// distinguish real breach from a freshly-restarted node; returning 0
	// keeps the evaluator from raising bogus breach alerts.
	const minRawSamplesFor1h = 10
	if hour.Total >= minRawSamplesFor1h {
		base.BurnRate1h = burnRate(safeRatio(hour.OK, hour.Total), def.Threshold)
	}
	base.ErrorBudgetRemainingPct = budgetRemainingPct(base.Observed, def.Threshold)
	base.Status = classify(base.Observed, def.Threshold, int(win.Total))
	return base, nil
}

func computeSuccessRate(db *gorm.DB, def *model.SLODefinition, base *Compliance, now time.Time) (*Compliance, error) {
	nodeIDs, err := resolveNodeIDs(db, def)
	if err != nil {
		return nil, err
	}
	if len(nodeIDs) == 0 {
		base.Status = StatusInsufficient
		return base, nil
	}
	var win aggRow
	if err := db.Table("task_runs").
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Select("COALESCE(SUM(CASE WHEN task_runs.status = 'success' THEN 1 ELSE 0 END), 0) AS ok, COUNT(*) AS total").
		Where("tasks.node_id IN ? AND task_runs.created_at >= ? AND task_runs.created_at < ?", nodeIDs, base.WindowStart, now).
		Scan(&win).Error; err != nil {
		return nil, err
	}
	var hour aggRow
	if err := db.Table("task_runs").
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Select("COALESCE(SUM(CASE WHEN task_runs.status = 'success' THEN 1 ELSE 0 END), 0) AS ok, COUNT(*) AS total").
		Where("tasks.node_id IN ? AND task_runs.created_at >= ? AND task_runs.created_at < ?", nodeIDs, now.Add(-time.Hour), now).
		Scan(&hour).Error; err != nil {
		return nil, err
	}
	base.Observed = safeRatio(win.OK, win.Total)
	base.SampleCount = int(win.Total)
	base.BurnRate1h = burnRate(safeRatio(hour.OK, hour.Total), def.Threshold)
	base.ErrorBudgetRemainingPct = budgetRemainingPct(base.Observed, def.Threshold)
	base.Status = classify(base.Observed, def.Threshold, int(win.Total))
	return base, nil
}

func safeRatio(num, den int64) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func budgetRemainingPct(observed, threshold float64) float64 {
	if threshold >= 1.0 {
		return 0
	}
	budget := 1.0 - threshold
	consumed := (1.0 - observed)
	remaining := 1.0 - (consumed / budget)
	if remaining < 0 {
		return 0
	}
	return remaining * 100
}

func classify(observed, threshold float64, total int) Status {
	if total < insufficientSampleThreshold {
		return StatusInsufficient
	}
	if observed < threshold {
		return StatusBreached
	}
	// observed >= threshold → check budget consumption
	remaining := budgetRemainingPct(observed, threshold)
	if remaining < warningBudgetRemainingPct {
		return StatusWarning
	}
	return StatusHealthy
}
