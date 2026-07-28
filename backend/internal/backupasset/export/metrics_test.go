package export

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestExportMetricsExposeOnlyClosedLowCardinalityLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.SetQueue(ExecutionQueued, 3, 2*time.Second)
	metrics.ObserveJob(ArchiveZIP, ExecutionReady, ResultComplete, MetricErrorNone, time.Second)
	metrics.AddBytes(MetricBytesLogical, 128)
	metrics.ObserveEvent(MetricEventTakeover)

	actual := exportMetricLabelNames(t, registry)
	expected := map[string][]string{
		"xirang_backup_asset_export_queue":                    {"state"},
		"xirang_backup_asset_export_queue_oldest_age_seconds": {"state"},
		"xirang_backup_asset_export_jobs_total":               {"error_category", "format", "result", "state"},
		"xirang_backup_asset_export_job_duration_seconds":     {"format", "state"},
		"xirang_backup_asset_export_bytes_total":              {"kind"},
		"xirang_backup_asset_export_events_total":             {"event"},
	}
	if len(actual) != len(expected) {
		t.Fatalf("metric family count=%d want=%d: %#v", len(actual), len(expected), actual)
	}
	for name, labels := range expected {
		if !slices.Equal(actual[name], labels) {
			t.Fatalf("metric %s labels=%v want=%v", name, actual[name], labels)
		}
	}
}

func TestExportMetricsMapUnknownValuesWithoutLeakingRawLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	secret := "export/SECRET/customer/path"
	metrics.SetQueue(ExecutionState(secret), -1, -time.Second)
	metrics.ObserveJob(ArchiveFormat(secret), ExecutionState(secret), ResultKind(secret), MetricErrorCategory(secret), -time.Second)
	metrics.AddBytes(MetricByteKind(secret), -1)
	metrics.ObserveEvent(MetricEvent(secret))

	for _, family := range exportMetricFamilies(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetValue(), "SECRET") || label.GetValue() != "unknown" {
					t.Fatalf("metric %s leaked label %s=%q", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
}

func TestExportNoopMetricsAreSafe(t *testing.T) {
	var metrics Metrics = NoopMetrics{}
	metrics.SetQueue(ExecutionQueued, 1, time.Second)
	metrics.ObserveJob(ArchiveTAR, ExecutionFailed, ResultPartial, MetricErrorInternal, time.Second)
	metrics.AddBytes(MetricBytesCiphertext, 1)
	metrics.ObserveEvent(MetricEventPurgeFailure)
}

func exportMetricLabelNames(t *testing.T, registry *prometheus.Registry) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, family := range exportMetricFamilies(t, registry) {
		if len(family.GetMetric()) == 0 {
			continue
		}
		labels := make([]string, 0, len(family.GetMetric()[0].GetLabel()))
		for _, label := range family.GetMetric()[0].GetLabel() {
			labels = append(labels, label.GetName())
		}
		slices.Sort(labels)
		result[family.GetName()] = labels
	}
	return result
}

func exportMetricFamilies(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}
