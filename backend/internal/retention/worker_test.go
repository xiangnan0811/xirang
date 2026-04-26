package retention

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoop_PrunerCalledOnStart(t *testing.T) {
	var calls int32
	called := make(chan struct{}, 1)
	loop := &Loop{
		Tick: time.Hour, // long - rely on initial pass
		Pruner: func(_ context.Context) (int64, error) {
			atomic.AddInt32(&calls, 1)
			select {
			case called <- struct{}{}:
			default:
			}
			return 0, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("initial Pruner call did not happen within 2s")
	}
	cancel()
	_ = loop.Shutdown(context.Background())
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 initial Pruner call, got %d", got)
	}
}

func TestLoop_PrunerCalledOnTick(t *testing.T) {
	var calls int32
	threeReached := make(chan struct{})
	loop := &Loop{
		Tick: 100 * time.Millisecond,
		Pruner: func(_ context.Context) (int64, error) {
			n := atomic.AddInt32(&calls, 1)
			if n == 3 {
				close(threeReached) // signal initial + 2 ticks have all fired
			}
			return 0, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)
	select {
	case <-threeReached:
	case <-time.After(2 * time.Second):
		t.Fatalf("3 Pruner calls did not happen within 2s, got %d", atomic.LoadInt32(&calls))
	}
	cancel()
	_ = loop.Shutdown(context.Background())
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected >=3 Pruner calls, got %d", got)
	}
}

func TestLoop_RunReturnsOnCtxCancel(t *testing.T) {
	loop := &Loop{
		Tick:   time.Hour,
		Pruner: func(_ context.Context) (int64, error) { return 0, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)
	cancel()
	if err := loop.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after cancel: %v", err)
	}
}

func TestLoop_ShutdownBeforeRunReturnsNil(t *testing.T) {
	loop := &Loop{
		Tick:   time.Hour,
		Pruner: func(_ context.Context) (int64, error) { return 0, nil },
	}
	if err := loop.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on never-started loop: %v", err)
	}
}

func TestLoop_ShutdownIdempotent(t *testing.T) {
	loop := &Loop{
		Tick:   time.Hour,
		Pruner: func(_ context.Context) (int64, error) { return 0, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)
	cancel()
	if err := loop.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := loop.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
