package anomaly

import (
	"context"
	"runtime/debug"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// Engine drives a fixed set of Detectors on independent goroutines.
type Engine struct {
	db        *gorm.DB
	settings  *settings.Service
	detectors []Detector
	raise     RaiseFn
}

// NewEngine constructs an Engine with the given detectors. Call SetRaiseFn
// before Run.
func NewEngine(db *gorm.DB, s *settings.Service, detectors ...Detector) *Engine {
	return &Engine{db: db, settings: s, detectors: detectors}
}

// SetRaiseFn wires the post-detection callback (anomaly events + alert).
func (e *Engine) SetRaiseFn(fn RaiseFn) { e.raise = fn }

// Run spawns one goroutine per detector and blocks until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	for _, det := range e.detectors {
		det := det
		go e.runDetector(ctx, det)
	}
	<-ctx.Done()
}

func (e *Engine) runDetector(ctx context.Context, det Detector) {
	defer func() {
		if r := recover(); r != nil {
			logger.Module("anomaly").Error().
				Str("detector", det.Name()).
				Interface("panic", r).
				Str("stack", string(debug.Stack())).
				Msg("detector goroutine recovered")
		}
	}()

	t := time.NewTicker(det.TickInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.safeTick(ctx, det)
		}
	}
}

func (e *Engine) safeTick(ctx context.Context, det Detector) {
	defer func() {
		if r := recover(); r != nil {
			logger.Module("anomaly").Error().
				Str("detector", det.Name()).
				Interface("panic", r).
				Msg("detector tick recovered")
		}
	}()

	findings, err := det.Evaluate(ctx)
	if err != nil {
		logger.Module("anomaly").Warn().
			Err(err).Str("detector", det.Name()).Msg("evaluate failed")
		return
	}
	if e.raise == nil {
		return
	}
	for _, f := range findings {
		if err := e.raise(ctx, f); err != nil {
			logger.Module("anomaly").Warn().
				Err(err).Str("detector", det.Name()).Uint("node_id", f.NodeID).
				Msg("raise failed")
		}
	}
}
