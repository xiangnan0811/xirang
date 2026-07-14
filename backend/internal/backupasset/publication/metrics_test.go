package publication

import (
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"xirang/backend/internal/backupasset"
)

func TestPublicationMetricsExposeOnlyFrozenBoundedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveAttempt(backupasset.ProviderRestic, StageExecution)
	metrics.ObserveOutcome(backupasset.ProviderRestic, StageExecution, backupasset.PublicationOutcomeSuccess)
	metrics.SetBacklog(backupasset.RecoveryPointPreparing, 3, 2*time.Second)
	metrics.ObserveReconcileMatch(ReconcileMatchExact)
	metrics.ObserveFenceLoss(StageManifest)
	metrics.ObserveManifest(20*time.Millisecond, 12, 128, backupasset.ManifestComplete, ManifestLimitNone)
	metrics.ObserveLegacyBlocked(OperationLegacySnapshotList)
	metrics.ObserveAuditFailure(StageReconciliation)

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	actual := metricLabelNames(families)
	expected := map[string][]string{
		"xirang_backup_asset_publication_attempts_total":             {"provider", "stage"},
		"xirang_backup_asset_publication_outcomes_total":             {"provider", "stage", "code"},
		"xirang_backup_asset_publication_backlog_count":              {"state"},
		"xirang_backup_asset_publication_backlog_oldest_age_seconds": {"state"},
		"xirang_backup_asset_publication_reconcile_matches_total":    {"class"},
		"xirang_backup_asset_publication_fence_lost_total":           {"stage"},
		"xirang_backup_asset_publication_manifest_duration_seconds":  {"completeness", "limit_class"},
		"xirang_backup_asset_publication_manifest_entries":           {"completeness"},
		"xirang_backup_asset_publication_manifest_bytes":             {"completeness"},
		"xirang_backup_asset_legacy_operation_blocked_total":         {"operation"},
		"xirang_backup_asset_publication_audit_failures_total":       {"stage"},
	}
	if len(actual) != len(expected) {
		t.Fatalf("metric family count=%d, want %d: %#v", len(actual), len(expected), actual)
	}
	for name, labels := range expected {
		if !sameStrings(actual[name], labels) {
			t.Fatalf("metric %s labels=%v, want %v", name, actual[name], labels)
		}
	}
}

func TestPublicationMetricsEncodeSuccessAsSuccessNeverUnknown(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveOutcome(backupasset.ProviderRestic, StageExecution, backupasset.PublicationOutcomeSuccess)
	if got := metricLabelValue(t, registry, "xirang_backup_asset_publication_outcomes_total", "code"); got != "success" {
		t.Fatalf("success outcome label=%q, want success", got)
	}
}

func TestPublicationMetricsMapUnknownTypedValuesToUnknownWithoutRawLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveAttempt(backupasset.ProviderKind("repository.example/secret"), PublicationStage("unsafe-stage"))
	metrics.ObserveOutcome(backupasset.ProviderKind("repository.example/secret"), PublicationStage("unsafe-stage"), backupasset.PublicationOutcomeCode("unsafe-code"))
	metrics.SetBacklog(backupasset.RecoveryPointState("unsafe-state"), -1, -time.Second)
	metrics.ObserveReconcileMatch(ReconcileMatchClass("unsafe-match"))
	metrics.ObserveFenceLoss(PublicationStage("unsafe-stage"))
	metrics.ObserveManifest(-time.Second, -1, -1, backupasset.ManifestCompleteness("unsafe-completeness"), ManifestLimitClass("unsafe-limit"))
	metrics.ObserveLegacyBlocked(ResticOperation("unsafe-operation"))
	metrics.ObserveAuditFailure(PublicationStage("unsafe-stage"))

	for _, family := range mustGather(t, registry) {
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetValue() != "unknown" {
					t.Fatalf("metric %s leaked unvalidated label %s=%q", family.GetName(), pair.GetName(), pair.GetValue())
				}
			}
		}
	}
}

func mustGather(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}

func metricLabelNames(families []*dto.MetricFamily) map[string][]string {
	result := make(map[string][]string, len(families))
	for _, family := range families {
		if len(family.GetMetric()) == 0 {
			continue
		}
		labels := make([]string, 0, len(family.GetMetric()[0].GetLabel()))
		for _, pair := range family.GetMetric()[0].GetLabel() {
			labels = append(labels, pair.GetName())
		}
		result[family.GetName()] = labels
	}
	return result
}

func metricLabelValue(t *testing.T, registry *prometheus.Registry, name, label string) string {
	t.Helper()
	for _, family := range mustGather(t, registry) {
		if family.GetName() != name || len(family.GetMetric()) != 1 {
			continue
		}
		for _, pair := range family.GetMetric()[0].GetLabel() {
			if pair.GetName() == label {
				return pair.GetValue()
			}
		}
	}
	t.Fatalf("label %q missing from metric %q", label, name)
	return ""
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
