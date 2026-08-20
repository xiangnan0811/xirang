package retention

import (
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestRetentionWorkerMetricsAreAggregatesWithoutPathOrEntryLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []MetricOutcome{
		MetricSelected, MetricRetired, MetricExpired, MetricBlocked, MetricRetried,
	} {
		metrics.Observe(outcome)
	}

	actual := retentionMetricLabelNames(t, registry)
	expected := map[string][]string{
		"xirang_backup_asset_retention_outcomes_total": {"outcome"},
	}
	if len(actual) != len(expected) {
		t.Fatalf("metric family count=%d want=%d: %#v", len(actual), len(expected), actual)
	}
	for name, labels := range expected {
		if !slices.Equal(actual[name], labels) {
			t.Fatalf("metric %s labels=%v want=%v", name, actual[name], labels)
		}
	}
	for _, family := range retentionMetricFamilies(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetName(), "path") || strings.Contains(label.GetName(), "entry") ||
					strings.Contains(label.GetValue(), "/") || strings.Contains(label.GetValue(), "\\") {
					t.Fatalf("metric %s leaked path/entry label %s=%q", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
}

func TestRetentionWorkerMetricsMapUnknownOutcomesWithoutLeakingRawValues(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.Observe(MetricOutcome("repository/SECRET/path"))
	metrics.Observe(MetricOutcome("entry:report.txt"))
	for _, family := range retentionMetricFamilies(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() != "unknown" {
					t.Fatalf("metric %s leaked label %s=%q", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
}

func retentionMetricLabelNames(t *testing.T, registry *prometheus.Registry) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, family := range retentionMetricFamilies(t, registry) {
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

func retentionMetricFamilies(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}
