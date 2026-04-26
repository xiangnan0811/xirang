// Package retention defines the contract and scaffold for periodic
// data-pruning workers. Each retention loop in the project (anomaly
// events, node logs, expired silences, task runs) lives in its own
// package but shares the same Run/Prune/Shutdown shape declared here.
package retention

import (
	"context"
	"sync"
	"time"

	"xirang/backend/internal/lifecycle"
)

// Worker contracts a periodic data-pruning loop. Composes lifecycle.Worker
// for start/stop semantics and adds the synchronous Prune step that
// retention specifically requires.
type Worker interface {
	lifecycle.Worker
	// Prune runs one retention pass synchronously and returns the rows
	// affected. Idempotent; safe to call from tests or admin tooling.
	Prune(ctx context.Context) (rowsAffected int64, err error)
}

// Loop is the reusable scaffold most retention workers will embed rather
// than reimplement the ticker + ctx loop. Workers with non-uniform cadence
// (e.g. task.RetentionWorker which runs three distinct sub-jobs) skip
// Loop and implement Run themselves.
//
// Tick and Pruner are construction-time fields; they must not be modified
// after Run is called.
type Loop struct {
	Tick   time.Duration
	Pruner func(ctx context.Context) (int64, error)

	mu   sync.Mutex
	done chan struct{}
}

// Run executes the initial pass on startup, then ticks at the configured
// interval until ctx is done. close(done) fires last so Shutdown only
// returns once Run has fully exited.
func (l *Loop) Run(ctx context.Context) {
	l.mu.Lock()
	l.done = make(chan struct{})
	done := l.done
	l.mu.Unlock()

	t := time.NewTicker(l.Tick)
	defer close(done) // fires last - terminal signal
	defer t.Stop()
	// Initial pass on startup so stale data from a long-stopped deployment
	// is cleaned immediately rather than after the first tick.
	l.Pruner(ctx) //nolint:errcheck // logged inside Pruner per worker
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Pruner(ctx) //nolint:errcheck // logged inside Pruner per worker
		}
	}
}

// Shutdown blocks until Run returns or stopCtx expires. Returns nil if
// Run was never started (done channel is nil).
//
// lifecycle.Worker contract deviation: the upstream Worker contract says
// "calling Shutdown before Run is started yields undefined behaviour".
// Loop intentionally narrows that to a no-op (returns nil) so retention
// workers that embed Loop can be safely Shutdown even if Run was never
// scheduled - useful for tests and partial-bootstrap recovery.
func (l *Loop) Shutdown(ctx context.Context) error {
	l.mu.Lock()
	done := l.done
	l.mu.Unlock()
	if done == nil {
		return nil // never started
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
