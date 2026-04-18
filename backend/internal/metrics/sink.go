package metrics

import (
	"context"

	"xirang/backend/internal/logger"
)

// Sink accepts samples for further processing (DB write, remote push, etc.).
// A Sink implementation must be safe for concurrent Write calls.
type Sink interface {
	Name() string
	Write(ctx context.Context, s Sample) error
}

// FanSink dispatches a single Sample to every configured sink. Failures in
// one sink are logged but never block delivery to the others, so a flaky
// remote push never breaks core DB persistence.
//
// FanSink itself satisfies Sink so it can be composed (e.g. nested fan-outs
// or used behind any Sink-typed handle). Its Write always returns nil — child
// errors are logged, never propagated.
type FanSink struct {
	sinks []Sink
}

// NewFanSink builds a FanSink from the given sinks. Order is preserved but
// not semantically meaningful; each sink receives an independent copy of the
// sample.
func NewFanSink(sinks ...Sink) *FanSink {
	return &FanSink{sinks: sinks}
}

// Name identifies the fan aggregator for logging purposes.
func (f *FanSink) Name() string { return "fan" }

// Write delivers the sample to every child sink. Errors are logged with
// sink name + node id context and do not interrupt other sinks. Always nil.
func (f *FanSink) Write(ctx context.Context, s Sample) error {
	for _, sink := range f.sinks {
		if err := sink.Write(ctx, s); err != nil {
			logger.Module("metrics").Warn().
				Str("sink", sink.Name()).
				Uint("node_id", s.NodeID).
				Err(err).
				Msg("metric sink write failed")
		}
	}
	return nil
}
