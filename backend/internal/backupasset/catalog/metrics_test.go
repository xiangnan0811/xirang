package catalog

import (
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestCatalogMetricsExposeOnlyFrozenLowCardinalityLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveBuild(MetricBuildComplete, 2*time.Second)
	metrics.ObserveScan(MetricScanSuccess)
	metrics.SetActiveBuilds(2)
	metrics.AddReconciledAbandoned(3)

	actual := catalogMetricLabelNames(t, registry)
	expected := map[string][]string{
		"xirang_backup_asset_catalog_builds_total":               {"outcome"},
		"xirang_backup_asset_catalog_build_duration_seconds":     {"outcome"},
		"xirang_backup_asset_catalog_scans_total":                {"outcome"},
		"xirang_backup_asset_catalog_active_builds":              {},
		"xirang_backup_asset_catalog_reconciled_abandoned_total": {},
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

func TestCatalogMetricsMapUnknownOutcomesWithoutLeakingRawValues(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveBuild(MetricBuildOutcome("repository/SECRET/path"), -time.Second)
	metrics.ObserveScan(MetricScanOutcome("cursor/SECRET"))
	for _, family := range catalogMetricFamilies(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() != "unknown" {
					t.Fatalf("metric %s leaked label %s=%q", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
}

func catalogMetricLabelNames(t *testing.T, registry *prometheus.Registry) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, family := range catalogMetricFamilies(t, registry) {
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

func catalogMetricFamilies(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}
