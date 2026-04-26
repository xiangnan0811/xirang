package slo

import (
	"context"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// BreachAlertTriggerThreshold — burn rate above this triggers an alert.
const BreachAlertTriggerThreshold = 2.0

// AlertSink is the inbound interface SLO needs from alerting. Defined here
// so the evaluator does not import alerting; alerting.DefaultRaiser
// satisfies this interface because it implements RaiseSLOBreach with the
// same signature.
type AlertSink interface {
	RaiseSLOBreach(def *model.SLODefinition, c *Compliance) error
}

// Evaluator periodically evaluates enabled SLOs and raises breach alerts.
type Evaluator struct {
	db   *gorm.DB
	tick time.Duration
	sink AlertSink
	done chan struct{}
}

// NewEvaluator constructs the evaluator. sink MUST be non-nil; passing nil
// installs a stub that logs dropped breaches loudly so the deployment bug
// surfaces in logs rather than silently dropping alerts.
func NewEvaluator(db *gorm.DB, sink AlertSink) *Evaluator {
	if sink == nil {
		logger.Module("slo").Warn().Msg("NewEvaluator called with nil sink — breaches will be logged only")
		sink = stubSink{}
	}
	return &Evaluator{db: db, tick: time.Minute, sink: sink, done: make(chan struct{})}
}

type stubSink struct{}

func (stubSink) RaiseSLOBreach(def *model.SLODefinition, c *Compliance) error {
	logger.Module("slo").Warn().
		Uint("slo_id", def.ID).
		Str("slo_name", def.Name).
		Float64("burn_1h", c.BurnRate1h).
		Msg("stub sink installed — breach not dispatched (bootstrap bug)")
	return nil
}

// Run blocks until ctx is cancelled, evaluating every `tick`.
// Implements lifecycle.Worker.
func (e *Evaluator) Run(ctx context.Context) {
	defer close(e.done)
	t := time.NewTicker(e.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			e.evaluateAll(now)
		}
	}
}

// Shutdown blocks until Run returns or ctx expires. Implements lifecycle.Worker.
func (e *Evaluator) Shutdown(ctx context.Context) error {
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Evaluator) evaluateAll(now time.Time) {
	var defs []model.SLODefinition
	if err := e.db.Where("enabled = ?", true).Find(&defs).Error; err != nil {
		logger.Module("slo").Warn().Err(err).Msg("加载 SLO 定义失败")
		return
	}
	for i := range defs {
		c, err := Compute(e.db, &defs[i], now)
		if err != nil {
			logger.Module("slo").Warn().Uint("slo_id", defs[i].ID).Err(err).Msg("SLO 计算失败")
			continue
		}
		if c.Status == StatusInsufficient {
			continue
		}
		recordPromMetrics(&defs[i], c)
		if c.BurnRate1h > BreachAlertTriggerThreshold {
			if err := e.sink.RaiseSLOBreach(&defs[i], c); err != nil {
				logger.Module("slo").Warn().Uint("slo_id", defs[i].ID).Err(err).Msg("RaiseSLOBreach 失败")
			}
		}
	}
}
