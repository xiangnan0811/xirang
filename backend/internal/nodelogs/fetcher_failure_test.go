package nodelogs

import (
	"context"
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"xirang/backend/internal/model"
)

func counterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

func TestFetchFailureReasonsDiscardOutput(t *testing.T) {
	for _, tc := range []struct {
		reason string
		err    error
	}{
		{"timeout", context.DeadlineExceeded},
		{"canceled", context.Canceled},
		{"output_limit", ErrOutputLimit},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			node := model.Node{ID: 818, LogJournalctlEnabled: true}
			metric := fetchErrors.WithLabelValues(nodeIDLabel(node.ID), tc.reason)
			before := counterValue(t, metric)
			f := NewFetcher(&fakeRunner{out: `{"__CURSOR":"unsafe","MESSAGE":"partial"}`, err: fmt.Errorf("execution: %w", tc.err)})
			entries, cursors, err := f.Fetch(context.Background(), node, nil)
			if err == nil || len(entries) != 0 || len(cursors) != 0 {
				t.Fatalf("failed execution must discard output: entries=%d cursors=%d err=%v", len(entries), len(cursors), err)
			}
			if got := counterValue(t, metric) - before; got != 1 {
				t.Fatalf("reason %s increment=%v, want 1", tc.reason, got)
			}
		})
	}
}
