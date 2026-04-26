package anomaly

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// AlertSink is anomaly's inbound interface for raising detection events as
// alerts. Defined here so the engine does not import alerting.
// alerting.DefaultRaiser does NOT directly satisfy this interface — see
// NewSink in raise.go for the adapter that bridges alerting's
// (db, AnomalyAlertInput)→(uint, bool, error) shape onto Sink.Raise.
type AlertSink interface {
	Raise(ctx context.Context, f Finding) error
}

// Engine drives a fixed set of Detectors on independent goroutines.
type Engine struct {
	db        *gorm.DB
	settings  *settings.Service
	detectors []Detector
	sink      AlertSink
	done      chan struct{}
}

// NewEngine constructs an Engine. Passing nil for sink installs a stub that
// logs dropped findings loudly so misconfigurations surface in production
// logs instead of panicking the detector goroutines.
func NewEngine(db *gorm.DB, s *settings.Service, sink AlertSink, detectors ...Detector) *Engine {
	if sink == nil {
		logger.Module("anomaly").Warn().Msg("NewEngine called with nil sink — findings will be logged only")
		sink = stubSink{}
	}
	return &Engine{db: db, settings: s, detectors: detectors, sink: sink, done: make(chan struct{})}
}

type stubSink struct{}

func (stubSink) Raise(_ context.Context, f Finding) error {
	logger.Module("anomaly").Warn().
		Uint("node_id", f.NodeID).
		Str("detector", f.Detector).
		Str("metric", f.Metric).
		Str("severity", f.Severity).
		Msg("stub sink active — finding dropped; wire a real AlertSink via NewEngine")
	return nil
}

// Run spawns one goroutine per detector and blocks until ctx is done.
// Implements lifecycle.Worker. close(e.done) only fires after every detector
// goroutine has returned, so Shutdown's nil return guarantees full quiescence
// (no detector still touching e.db, e.sink, or e.settings).
func (e *Engine) Run(ctx context.Context) {
	defer close(e.done)
	var wg sync.WaitGroup
	for _, det := range e.detectors {
		det := det
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.runDetector(ctx, det)
		}()
	}
	<-ctx.Done()
	wg.Wait() // drain detectors before closing done
}

// Shutdown blocks until Run returns or ctx expires. Implements lifecycle.Worker.
func (e *Engine) Shutdown(ctx context.Context) error {
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	for _, f := range findings {
		if err := e.sink.Raise(ctx, f); err != nil {
			logger.Module("anomaly").Warn().
				Err(err).Str("detector", det.Name()).Uint("node_id", f.NodeID).
				Msg("raise failed")
		}
	}
}
