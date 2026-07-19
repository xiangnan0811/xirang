package content

import (
	"slices"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestContentMetricsExposeOnlyFrozenLowCardinalityLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveTicket(DeliveryPreview, MetricOutcomeSuccess)
	metrics.ObserveRead(DeliveryDownload, MetricOutcomeBlocked)
	metrics.SetInFlight(backupasset.ProviderRestic, 2)
	metrics.AddBytes(MetricBytesReserved, 128)
	metrics.ObserveReason(MetricReasonSourceChanged)
	metrics.ObserveCache(MetricCacheTamper)
	metrics.SetAuditBacklog(3)
	metrics.ObserveAuditRetry()
	metrics.SetReconciliationAge(2 * time.Second)

	actual := contentMetricLabelNames(t, registry)
	expected := map[string][]string{
		"xirang_backup_asset_content_tickets_total":              {"action", "outcome"},
		"xirang_backup_asset_content_reads_total":                {"action", "outcome"},
		"xirang_backup_asset_content_in_flight":                  {"provider"},
		"xirang_backup_asset_content_bytes_total":                {"kind"},
		"xirang_backup_asset_content_reasons_total":              {"reason"},
		"xirang_backup_asset_content_cache_total":                {"outcome"},
		"xirang_backup_asset_content_audit_backlog":              {},
		"xirang_backup_asset_content_audit_retries_total":        {},
		"xirang_backup_asset_content_reconciliation_age_seconds": {},
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

func TestContentMetricsMapUnknownValuesWithoutLeakingRawLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	secret := "repository/SECRET/path"
	metrics.ObserveTicket(DeliveryAction(secret), MetricOutcome(secret))
	metrics.ObserveRead(DeliveryAction(secret), MetricOutcome(secret))
	metrics.SetInFlight(backupasset.ProviderKind(secret), -1)
	metrics.AddBytes(MetricByteKind(secret), -1)
	metrics.ObserveReason(MetricReason(secret))
	metrics.ObserveCache(MetricCacheOutcome(secret))

	for _, family := range contentMetricFamilies(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetValue(), "SECRET") || label.GetValue() != "unknown" {
					t.Fatalf("metric %s leaked label %s=%q", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
}

func contentMetricLabelNames(t *testing.T, registry *prometheus.Registry) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, family := range contentMetricFamilies(t, registry) {
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

func contentMetricFamilies(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}
