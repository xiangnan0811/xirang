package nodelogs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"xirang/backend/internal/logger"
)

func TestSchedulerSaturationEmitsOneAggregateWarning(t *testing.T) {
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&output)
	t.Cleanup(func() { logger.Log = previous })
	s := NewScheduler(openCursorTestDB(t), &fakeRunner{})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	s.jobs = make(chan CollectJob, 1)
	for id := uint(1); id <= 4; id++ {
		seedCollector(t, s, id)
	}
	before := counterValue(t, queueRejected.WithLabelValues("full"))
	s.enqueue(context.Background())
	if got := counterValue(t, queueRejected.WithLabelValues("full")) - before; got != 3 {
		t.Fatalf("rejected=%v, want 3", got)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("warnings=%d, want 1", len(lines))
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"node_id", "host", "path", "username", "nodes"} {
		if _, exists := record[key]; exists {
			t.Fatalf("sensitive/high-cardinality field %s", key)
		}
	}
	if record["rejected"] != float64(3) || record["queue_capacity"] != float64(1) || record["queue_depth"] != float64(1) {
		t.Fatal("missing aggregate queue counts")
	}
	if strings.Contains(output.String(), "FAKE_NODE") {
		t.Fatal("node name leaked")
	}
}

func TestSchedulerMetricsHaveOnlyBoundedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(jobsDeduplicated, queueRejected, inFlight, shutdownTimeouts)
	queueRejected.WithLabelValues("full")
	queueRejected.WithLabelValues("shutdown")
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if family.GetName() != "xirang_node_logs_queue_rejected_total" || label.GetName() != "reason" || (label.GetValue() != "full" && label.GetValue() != "shutdown") {
					t.Fatalf("unbounded metric label: %s %s", family.GetName(), label.GetName())
				}
			}
		}
	}
}
