package search

import (
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"xirang/backend/internal/backupasset"
)

func TestSearchMetricsLabelsRemainClosedAndLowCardinality(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []BuildOutcome{BuildOutcomeSuccess, BuildOutcomeFailure, BuildOutcomeCanceled, BuildOutcomeFenced} {
		metrics.ObserveBuild(outcome)
	}
	for _, outcome := range []ScanOutcome{ScanOutcomeSuccess, ScanOutcomeFailure, ScanOutcomeDisabled} {
		metrics.ObserveScan(outcome)
	}
	metrics.SetActiveBuilds(2)
	metrics.AddReconciledAbandoned(3)
	metrics.AddReconciledOverlays(4)
	if validBuildOutcome(BuildOutcome("point-id")) || validScanOutcome(ScanOutcome("repository-id")) {
		t.Fatal("high-cardinality metric label was accepted")
	}

	actual := searchMetricLabelNames(t, registry)
	expected := map[string][]string{
		"xirang_backup_asset_search_builds_total":               {"outcome"},
		"xirang_backup_asset_search_scans_total":                {"outcome"},
		"xirang_backup_asset_search_active_builds":              {},
		"xirang_backup_asset_search_reconciled_abandoned_total": {},
		"xirang_backup_asset_search_reconciled_overlays_total":  {},
	}
	if len(actual) != len(expected) {
		t.Fatalf("metric family count=%d want=%d: %#v", len(actual), len(expected), actual)
	}
	for name, labels := range expected {
		if !slices.Equal(actual[name], labels) {
			t.Fatalf("metric %s labels=%v want=%v", name, actual[name], labels)
		}
	}
	for _, family := range searchMetricFamilies(t, registry) {
		if strings.Contains(family.GetName(), "duration") || strings.Contains(family.GetName(), "latency") {
			t.Fatalf("search metrics must not expose latency: %s", family.GetName())
		}
		if family.GetName() == "xirang_backup_asset_search_builds_total" || family.GetName() == "xirang_backup_asset_search_scans_total" {
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() != "outcome" {
						t.Fatalf("search metric %s has forbidden label %s", family.GetName(), label.GetName())
					}
				}
			}
		}
	}
}

func TestSearchMetricsMapUnknownOutcomesWithoutLeakingRawValues(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveBuild(BuildOutcome("point/SECRET/path"))
	metrics.ObserveScan(ScanOutcome("cursor/SECRET"))
	for _, family := range searchMetricFamilies(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() != "unknown" {
					t.Fatalf("metric %s leaked label %s=%q", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
}

func TestSearchMetricsNilRegistererFailsClosed(t *testing.T) {
	_, err := NewPrometheusMetrics(nil)
	if err == nil || !strings.Contains(err.Error(), backupasset.ErrInvalidState.Error()) {
		t.Fatalf("nil registerer err=%v", err)
	}
}

func TestSearchNoopMetricsAreSafe(t *testing.T) {
	var metrics Metrics = NoopMetrics{}
	metrics.ObserveBuild(BuildOutcomeSuccess)
	metrics.ObserveScan(ScanOutcomeDisabled)
	metrics.SetActiveBuilds(1)
	metrics.AddReconciledAbandoned(1)
	metrics.AddReconciledOverlays(1)
}

func searchMetricLabelNames(t *testing.T, registry *prometheus.Registry) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, family := range searchMetricFamilies(t, registry) {
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

func searchMetricFamilies(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}
