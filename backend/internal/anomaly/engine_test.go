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

// TestEngine_NilSink_StubAbsorbs replaces the prior NilRaiseFn_NoPanic test.
// Constructor now installs a stub when sink is nil; Run must not panic and
// findings must be silently absorbed by the stub.
func TestEngine_NilSink_StubAbsorbs(t *testing.T) {
	det := &fakeDetector{
		name: "fake", interval: 10 * time.Millisecond,
		returns: []Finding{{NodeID: 1}},
	}
	e := NewEngine(nil, nil, nil, det) // nil sink → stubSink installed
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	e.Run(ctx)
	// Not panicking is the assertion. The stub logs but never panics.
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
