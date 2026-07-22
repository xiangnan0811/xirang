package processing

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestProcessingMetricsKeepLabelsClosedAndClampValues(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveJob(PriorityClass("priority/secret"), ProcessingState("state/secret"), ProcessingErrorCategory("error/secret"))
	metrics.ObserveJobDuration(PriorityClass("priority/secret"), ProcessingState("state/secret"), -time.Second)
	metrics.SetWorkers(WorkerTrustClass("trust/secret"), WorkerHealthClass("health/secret"), -4)
	metrics.SetSlots(SlotClass("slot/secret"), SlotMetricUsed, -4)
	metrics.SetQueue(PriorityClass("priority/secret"), ProcessingState("state/secret"), -4, -time.Second)
	metrics.AddSinkBytes(-4)
	metrics.SetDerived(DerivedMetricKind("derived/secret"), -4)
	metrics.ObserveDerived(DerivedMetricEvent("event/secret"))
	metrics.SetCoverage("capability/secret", "profile/secret", CoverageMetricState("coverage/secret"), -4)
	metrics.ObserveUpdaterActivation(UpdaterActivationOutcome("activation/secret"))

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetValue(), "secret") {
					t.Fatalf("raw metric label leaked: family=%s label=%s", family.GetName(), label.GetValue())
				}
			}
		}
	}
	names := make(map[string]bool, len(families))
	for _, family := range families {
		names[family.GetName()] = true
	}
	for _, required := range []string{
		"xirang_backup_asset_processing_coverage",
		"xirang_backup_asset_processing_updater_activation_total",
	} {
		if !names[required] {
			t.Fatalf("missing Child 11 metric %q", required)
		}
	}
}

func TestProcessingMetricsNoopIsSafe(t *testing.T) {
	var metrics Metrics = NoopMetrics{}
	metrics.ObserveJob(PriorityInteractive, ProcessingQueued, TransientError)
	metrics.ObserveLeaseLoss()
	metrics.ObserveJobDuration(PriorityInteractive, ProcessingSucceeded, time.Second)
	metrics.SetWorkers(WorkerTrustActive, WorkerHealthReady, 1)
	metrics.SetSlots(SlotInteractive, SlotMetricUsed, 1)
	metrics.SetQueue(PriorityInteractive, ProcessingQueued, 1, time.Second)
	metrics.AddSinkBytes(1)
	metrics.SetDerived(DerivedMetricLogicalBytes, 1)
	metrics.ObserveDerived(DerivedEventRewrapped)
	metrics.SetCoverage("image.thumbnail", "raster_thumbnail_v1", CoverageMetricComplete, 1)
	metrics.ObserveUpdaterActivation(UpdaterActivationCommit)
}
