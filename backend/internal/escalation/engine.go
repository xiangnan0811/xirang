package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// DefaultTickInterval is the engine's poll cadence.
const DefaultTickInterval = 30 * time.Second

// Engine polls open alerts and fires the next escalation level when due.
type Engine struct {
	db      *gorm.DB
	svc     *Service
	silence SilenceCheckerFn
	sender  SenderFn
	tick    time.Duration
	nowFn   func() time.Time // for tests
}

// NewEngine constructs an Engine. silence and sender are injected dependencies.
func NewEngine(db *gorm.DB, svc *Service, silence SilenceCheckerFn, sender SenderFn) *Engine {
	return &Engine{
		db: db, svc: svc, silence: silence, sender: sender,
		tick:  DefaultTickInterval,
		nowFn: time.Now,
	}
}

// SetTickInterval overrides the default tick for tests/smoke.
func (e *Engine) SetTickInterval(d time.Duration) { e.tick = d }

// SetNowFn overrides time.Now for deterministic tests.
func (e *Engine) SetNowFn(fn func() time.Time) { e.nowFn = fn }

// Run drives the engine until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Tick(ctx)
		}
	}
}

// tickBatchSize caps one Tick's open-alert fetch so a runaway queue can't
// stall the escalation loop for minutes per pass. At 1000 alerts/30s the
// engine still drains ~30/s which is far above any realistic fire rate.
//
// Pagination relies on alert.ID being monotonically increasing and never
// reused — both hold with GORM's AUTOINCREMENT PK + our soft-delete (status
// flip) retention strategy. If a future change ever reuses IDs, replace the
// cursor scheme with offset-by-id-set or LIMIT/OFFSET.
const tickBatchSize = 1000

// Tick performs one scan+fire pass. Exposed for tests. Processes in batches
// keyed by ascending id to bound memory + per-tick latency at scale.
func (e *Engine) Tick(ctx context.Context) {
	start := time.Now()
	defer func() { TickDuration.Observe(time.Since(start).Seconds()) }()
	now := e.nowFn()
	var cursor uint
	for {
		if ctx.Err() != nil {
			return
		}
		var alerts []model.Alert
		if err := e.db.WithContext(ctx).
			Where("status = ? AND id > ?", "open", cursor).
			Order("id ASC").
			Limit(tickBatchSize).
			Find(&alerts).Error; err != nil {
			logger.Module("escalation").Warn().Err(err).Msg("tick: load alerts failed")
			return
		}
		if len(alerts) == 0 {
			return
		}
		for i := range alerts {
			if ctx.Err() != nil {
				return
			}
			OpenAlertsScanned.Inc()
			e.evaluate(ctx, &alerts[i], now)
		}
		if len(alerts) < tickBatchSize {
			return
		}
		cursor = alerts[len(alerts)-1].ID
	}
}

// evaluate decides whether the next level for this alert is due and fires it.
func (e *Engine) evaluate(ctx context.Context, alert *model.Alert, now time.Time) {
	policy, err := e.svc.ResolvePolicyForAlert(ctx, *alert)
	if err != nil {
		logger.Module("escalation").Warn().Err(err).Uint("alert_id", alert.ID).Msg("resolve policy failed")
		return
	}
	if policy == nil || !policy.Enabled {
		return
	}
	if !SeverityAtLeast(alert.Severity, policy.MinSeverity) {
		return
	}
	levels := policy.DecodedLevels()
	nextIdx := alert.LastLevelFired + 1
	if nextIdx < 0 || nextIdx >= len(levels) {
		return
	}
	level := levels[nextIdx]
	if now.Sub(alert.TriggeredAt) < time.Duration(level.DelaySeconds)*time.Second {
		return
	}
	e.fire(ctx, alert, policy, nextIdx, level, now)
}

// fire atomically advances last_level_fired, records the event, then dispatches.
// Steps:
//  1. compute severityAfter and tagsAfter
//  2. check silence against the projected alert state
//  3. UPDATE alerts with optimistic lock on last_level_fired
//  4. INSERT event row (UNIQUE protects against double fire)
//  5. after tx commit, call sender (unless silenced-skip)
func (e *Engine) fire(ctx context.Context, alert *model.Alert, policy *model.EscalationPolicy,
	idx int, level model.EscalationLevel, now time.Time) {

	severityBefore := alert.Severity
	severityAfter := severityBefore
	if level.SeverityOverride != "" {
		severityAfter = level.SeverityOverride
	}

	existingTags := alert.DecodedTags()
	tagsAfter := appendUniqueTags(existingTags, level.Tags)
	tagsJSON, _ := json.Marshal(tagsAfter)
	tagsAddedJSON, _ := json.Marshal(level.Tags)

	// Check silence against the projected state
	proj := *alert
	proj.Severity = severityAfter
	proj.Tags = string(tagsJSON)
	silenced := false
	if e.silence != nil {
		if sil := e.silence(proj); sil != nil {
			silenced = true
		}
	}

	integrationIDs := level.IntegrationIDs
	integrationSnapshot := append([]uint(nil), integrationIDs...)
	if silenced {
		integrationSnapshot = nil
	}
	integrationSnapshotJSON, _ := json.Marshal(integrationSnapshot)

	pid := policy.ID

	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Optimistic lock on last_level_fired AND status='open' — covers
		// two races: concurrent fire (another tick advanced last_level_fired)
		// and state flip (API acked/resolved the alert between evaluate and
		// fire). Either outcome triggers a clean idempotent skip.
		//
		// NOTE: Updates(map) does NOT skip zero values. If a future caller
		// adds a field whose legitimate zero value differs from "unchanged"
		// (e.g. numeric severity scale), switch to a struct + Select().
		res := tx.Model(&model.Alert{}).
			Where("id = ? AND last_level_fired = ? AND status = ?",
				alert.ID, alert.LastLevelFired, "open").
			Updates(map[string]any{
				"severity":         severityAfter,
				"tags":             string(tagsJSON),
				"last_level_fired": idx,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errConcurrentFire
		}
		evt := model.AlertEscalationEvent{
			AlertID:            alert.ID,
			EscalationPolicyID: &pid,
			LevelIndex:         idx,
			IntegrationIDs:     string(integrationSnapshotJSON),
			SeverityBefore:     severityBefore,
			SeverityAfter:      severityAfter,
			TagsAdded:          string(tagsAddedJSON),
			FiredAt:            now,
		}
		return tx.Create(&evt).Error
	})
	if err != nil {
		if errors.Is(err, errConcurrentFire) {
			return // another tick already advanced this alert; idempotent skip
		}
		logger.Module("escalation").Warn().Err(err).Uint("alert_id", alert.ID).Int("level", idx).Msg("fire failed")
		return
	}

	// Update in-memory snapshot so subsequent evaluates in the same tick see new state
	alert.Severity = severityAfter
	alert.Tags = string(tagsJSON)
	alert.LastLevelFired = idx

	silencedLabel := "false"
	if silenced {
		silencedLabel = "true"
	}
	FiresTotal.WithLabelValues(severityAfter, silencedLabel).Inc()

	if !silenced && e.sender != nil && len(integrationIDs) > 0 {
		e.sender(*alert, integrationIDs)
	}
}

var errConcurrentFire = errors.New("concurrent fire detected")

// appendUniqueTags returns existing ∪ add, preserving order of first appearance.
func appendUniqueTags(existing, add []string) []string {
	seen := make(map[string]bool, len(existing))
	out := make([]string, 0, len(existing)+len(add))
	for _, t := range existing {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range add {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
