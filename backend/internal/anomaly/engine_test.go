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

// recordingSink captures every Raise call so tests can assert dispatch
// behavior without touching the engine internals.
type recordingSink struct {
	count int64
}

func (r *recordingSink) Raise(_ context.Context, _ Finding) error {
	atomic.AddInt64(&r.count, 1)
	return nil
}

func (r *recordingSink) Count() int64 { return atomic.LoadInt64(&r.count) }

func TestEngine_DispatchesFindingsToSink(t *testing.T) {
	det := &fakeDetector{
		name:     "fake",
		interval: 10 * time.Millisecond,
		returns:  []Finding{{NodeID: 1, Metric: "cpu_pct"}, {NodeID: 2, Metric: "cpu_pct"}},
	}
	sink := &recordingSink{}
	e := NewEngine(nil, nil, sink, det)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	if sink.Count() < 2 {
		t.Fatalf("expected ≥2 raises, got %d", sink.Count())
	}
}

// TestEngine_NilSink_PanicsAtConstruction asserts the bootstrap contract:
// a nil sink is a wiring bug and must crash loudly at NewEngine, not be
// silently swallowed by a stub. Findings have no other dispatch path.
func TestEngine_NilSink_PanicsAtConstruction(t *testing.T) {
	det := &fakeDetector{
		name: "fake", interval: 10 * time.Millisecond,
		returns: []Finding{{NodeID: 1}},
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected NewEngine to panic on nil sink, got no panic")
		}
	}()
	_ = NewEngine(nil, nil, nil, det)
	t.Fatalf("unreachable: NewEngine should have panicked")
}

func TestEngine_TickPanic_Recovered_TickerContinues(t *testing.T) {
	det := &fakeDetector{
		name: "fake", interval: 10 * time.Millisecond,
		panicMsg: "boom",
	}
	sink := &recordingSink{}
	e := NewEngine(nil, nil, sink, det)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	// If panic wasn't recovered, the goroutine would die and calls would stop.
	if det.Calls() < 2 {
		t.Fatalf("ticker should continue after panic; calls=%d", det.Calls())
	}
}

func TestEngine_EvaluateError_LoggedNotFatal(t *testing.T) {
	det := &fakeDetector{
		name: "fake", interval: 10 * time.Millisecond,
		err: ErrInvalidInput,
	}
	sink := &recordingSink{}
	e := NewEngine(nil, nil, sink, det)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	if sink.Count() != 0 {
		t.Fatalf("evaluate error should short-circuit raise, got %d", sink.Count())
	}
}
