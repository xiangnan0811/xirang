package ga

import (
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"xirang/backend/internal/backupasset"
)

func TestGAMetricsExposeOnlyFrozenLowCardinalityLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.SetInstallationClass(InstallationFresh)
	metrics.SetReadinessState(ReadinessReady)
	metrics.SetLastInventoryResult(InventoryResultComplete)
	metrics.SetConflictCount(ConflictSharedResticIdentity, 2)
	metrics.ObserveEnablementReject(RejectAckRequired)
	metrics.SetExportRootProbe(true)
	metrics.SetFeatureGates(true, false)

	actual := gaMetricLabelNames(t, registry)
	expected := map[string][]string{
		"xirang_backup_asset_ga_installation_class":       {"class"},
		"xirang_backup_asset_ga_readiness_state":          {"state"},
		"xirang_backup_asset_ga_inventory_result":         {"result"},
		"xirang_backup_asset_ga_conflicts":                {"kind"},
		"xirang_backup_asset_ga_enablement_rejects_total": {"reason"},
		"xirang_backup_asset_ga_export_root_probe":        {"result"},
		FeatureRequestedMetric:                            {},
		FeatureLiveMetric:                                 {},
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

func TestGAMetricsMapUnknownValuesWithoutLeakingRawLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	secret := "repository/SECRET/locator"
	metrics.SetInstallationClass(InstallationClass(secret))
	metrics.SetReadinessState(ReadinessStatus(secret))
	metrics.SetLastInventoryResult(InventoryResult(secret))
	metrics.SetConflictCount(ConflictKind(secret), 3)
	metrics.ObserveEnablementReject(EnablementRejectReason(secret))

	sawUnknown := map[string]bool{}
	for _, family := range gaMetricFamilies(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetValue(), "SECRET") {
					t.Fatalf("metric %s leaked label %s=%q", family.GetName(), label.GetName(), label.GetValue())
				}
				if label.GetValue() == "unknown" {
					sawUnknown[family.GetName()] = true
				}
			}
		}
	}
	for _, name := range []string{
		"xirang_backup_asset_ga_installation_class",
		"xirang_backup_asset_ga_readiness_state",
		"xirang_backup_asset_ga_inventory_result",
		"xirang_backup_asset_ga_conflicts",
		"xirang_backup_asset_ga_enablement_rejects_total",
	} {
		if !sawUnknown[name] {
			t.Fatalf("metric %s did not collapse unknown input to unknown", name)
		}
	}
}

func TestGAMetricsNilRegistererFailsClosed(t *testing.T) {
	_, err := NewPrometheusMetrics(nil)
	if err == nil || !strings.Contains(err.Error(), backupasset.ErrInvalidState.Error()) {
		t.Fatalf("nil registerer err=%v", err)
	}
}

func TestGANoopMetricsAreSafe(t *testing.T) {
	var metrics Metrics = NoopMetrics{}
	metrics.SetInstallationClass(InstallationExisting)
	metrics.SetReadinessState(ReadinessBlocked)
	metrics.SetLastInventoryResult(InventoryResultFailed)
	metrics.SetConflictCount(ConflictCapabilityGap, 1)
	metrics.ObserveEnablementReject(RejectInventoryIncomplete)
	metrics.SetExportRootProbe(false)
	metrics.SetFeatureGates(false, false)
}

func gaMetricLabelNames(t *testing.T, registry *prometheus.Registry) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, family := range gaMetricFamilies(t, registry) {
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

func gaMetricFamilies(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}
