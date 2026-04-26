package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fixtureWorker is a tiny in-test implementation that mimics the project's
// standard pattern: Run loops on a context, closes done on exit; Shutdown
// blocks until done is closed or stopCtx times out. This is the target
// post-migration shape; existing workers that currently lack a done
// channel will gain one as part of Tasks 9-12.
type fixtureWorker struct {
	done chan struct{}
}

func newFixtureWorker() *fixtureWorker {
	return &fixtureWorker{done: make(chan struct{})}
}

func (f *fixtureWorker) Run(ctx context.Context) {
	defer close(f.done)
	<-ctx.Done()
}

func (f *fixtureWorker) Shutdown(ctx context.Context) error {
	select {
	case <-f.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestWorker_RunReturnsOnCtxCancel(t *testing.T) {
	w := newFixtureWorker()
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	cancel()
	select {
	case <-w.done:
		// pass
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not return within 200ms after ctx cancel")
	}
}

func TestWorker_ShutdownBlocksUntilRunReturns(t *testing.T) {
	w := newFixtureWorker()
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	stopErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		stopErr <- w.Shutdown(context.Background())
	}()
	<-started // Shutdown goroutine has been scheduled
	// Shutdown should still be blocking - Run hasn't been canceled.
	select {
	case <-stopErr:
		t.Fatal("Shutdown returned before Run was canceled")
	default:
		// expected - still blocked
	}
	cancel()
	if err := <-stopErr; err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
}

func TestWorker_ShutdownReturnsCtxErrOnTimeout(t *testing.T) {
	w := newFixtureWorker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stopCancel()
	err := w.Shutdown(stopCtx)
	if err == nil {
		t.Fatal("expected non-nil error when stopCtx expires before Run returns")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestWorker_ShutdownIdempotentAfterRunReturned(t *testing.T) {
	w := newFixtureWorker()
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	cancel()
	// Wait for Run to actually finish.
	<-w.done
	// First Shutdown - Run already returned.
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown after Run returned: %v", err)
	}
	// Second Shutdown - must be a no-op.
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
