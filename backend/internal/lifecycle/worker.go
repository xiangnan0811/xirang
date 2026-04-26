// Package lifecycle defines the standard long-running goroutine contract
// used by every background subsystem in the project (probers, schedulers,
// retention loops, alerting retry, etc.). Workers implement Worker so
// main.go can start and drain them through one uniform call shape.
//
// The caller owns the run context (cancels it to stop workers) and
// supplies a separate stop context to Shutdown to cap drain time.
package lifecycle

import "context"

// Worker is the standard background-goroutine contract.
//
//	go w.Run(ctx)              // controller-supplied ctx; Run returns when ctx is done.
//	if err := w.Shutdown(stopCtx); err != nil { ... }
//
// Run MUST honor ctx cancellation. Run MUST be called (in its own
// goroutine) before Shutdown is called - calling Shutdown before Run
// is started yields undefined behaviour. Shutdown SHOULD be safe to
// call after Run has already returned (no-op). Shutdown MAY perform
// a final drain pass; stopCtx caps how long that takes.
type Worker interface {
	Run(ctx context.Context)
	Shutdown(ctx context.Context) error
}
