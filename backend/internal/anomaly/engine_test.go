package anomaly

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDetector struct {
	name     string
	interval time.Duration
	mu       sync.Mutex
	calls    int
	returns  []Finding
	err      error
	panicMsg string
}

func (f *fakeDetector) Name() string                { return f.name }
func (f *fakeDetector) TickInterval() time.Duration { return f.interval }
func (f *fakeDetector) Evaluate(_ context.Context) ([]Finding, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	return f.returns, f.err
}
func (f *fakeDetector) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestEngine_DispatchesFindingsToRaise(t *testing.T) {
	det := &fakeDetector{
		name:     "fake",
		interval: 10 * time.Millisecond,
		returns:  []Finding{{NodeID: 1, Metric: "cpu_pct"}, {NodeID: 2, Metric: "cpu_pct"}},
	}
	var raised int64
	e := NewEngine(nil, nil, det)
	e.SetRaiseFn(func(_ context.Context, _ Finding) error {
		atomic.AddInt64(&raised, 1)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	if atomic.LoadInt64(&raised) < 2 {
		t.Fatalf("expected ≥2 raises, got %d", atomic.LoadInt64(&raised))
	}
}

func TestEngine_NilRaiseFn_NoPanic(t *testing.T) {
	det := &fakeDetector{
		name: "fake", interval: 10 * time.Millisecond,
		returns: []Finding{{NodeID: 1}},
	}
	e := NewEngine(nil, nil, det) // no raiseFn set
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	// Not panicking is the assertion
}

func TestEngine_TickPanic_Recovered_TickerContinues(t *testing.T) {
	det := &fakeDetector{
		name: "fake", interval: 10 * time.Millisecond,
		panicMsg: "boom",
	}
	e := NewEngine(nil, nil, det)
	e.SetRaiseFn(func(_ context.Context, _ Finding) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	// If panic wasn't recovered, the goroutine would die and calls would stop
	if det.Calls() < 2 {
		t.Fatalf("ticker should continue after panic; calls=%d", det.Calls())
	}
}

func TestEngine_EvaluateError_LoggedNotFatal(t *testing.T) {
	det := &fakeDetector{
		name: "fake", interval: 10 * time.Millisecond,
		err: ErrInvalidInput,
	}
	var raised int64
	e := NewEngine(nil, nil, det)
	e.SetRaiseFn(func(_ context.Context, _ Finding) error {
		atomic.AddInt64(&raised, 1)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	if atomic.LoadInt64(&raised) != 0 {
		t.Fatalf("evaluate error should short-circuit raise, got %d", atomic.LoadInt64(&raised))
	}
}
