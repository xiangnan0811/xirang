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

// Evaluator periodically evaluates enabled SLOs and raises breach alerts.
type Evaluator struct {
	db      *gorm.DB
	tick    time.Duration
	raiseFn func(db any, slo *model.SLODefinition, c *Compliance) error
}

func NewEvaluator(db *gorm.DB) *Evaluator {
	return &Evaluator{
		db:   db,
		tick: time.Minute,
		raiseFn: func(_ any, _ *model.SLODefinition, _ *Compliance) error {
			// Real raise wiring injected in main.go (Task 8) to avoid import cycle
			// between slo and alerting. Default is no-op.
			return nil
		},
	}
}

// SetRaiseFn replaces the breach callback.
// MUST be called before Run() starts; mutation during Run is racy.
// In production this is set once in main.go during bootstrap.
func (e *Evaluator) SetRaiseFn(fn func(db any, slo *model.SLODefinition, c *Compliance) error) {
	e.raiseFn = fn
}

// Run blocks until ctx is cancelled, evaluating every `tick`.
func (e *Evaluator) Run(ctx context.Context) {
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
		recordPromMetrics(&defs[i], c) // real impl lands in Task 8 (prom.go)
		if c.BurnRate1h > BreachAlertTriggerThreshold {
			if err := e.raiseFn(e.db, &defs[i], c); err != nil {
				logger.Module("slo").Warn().Uint("slo_id", defs[i].ID).Err(err).Msg("RaiseSLOBreach 失败")
			}
		}
	}
}

