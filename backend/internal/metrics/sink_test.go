package metrics

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recorderSink struct {
	name   string
	fail   bool
	called int
}

func (r *recorderSink) Name() string { return r.name }
func (r *recorderSink) Write(_ context.Context, _ Sample) error {
	r.called++
	if r.fail {
		return errors.New("boom")
	}
	return nil
}

func TestFanSink_DispatchesToAll(t *testing.T) {
	a := &recorderSink{name: "a"}
	b := &recorderSink{name: "b"}
	fan := NewFanSink(a, b)
	if err := fan.Write(context.Background(), Sample{NodeID: 1, SampledAt: time.Now()}); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if a.called != 1 || b.called != 1 {
		t.Fatalf("expected both sinks called once, got a=%d b=%d", a.called, b.called)
	}
}

func TestFanSink_OneFailsDoesNotBlockOthers(t *testing.T) {
	a := &recorderSink{name: "a", fail: true}
	b := &recorderSink{name: "b"}
	fan := NewFanSink(a, b)
	// First sink returns an error; FanSink should still dispatch to b. Error is expected.
	_ = fan.Write(context.Background(), Sample{NodeID: 1, SampledAt: time.Now()})
	if b.called != 1 {
		t.Fatalf("expected b to be called despite a failing, got %d", b.called)
	}
}
