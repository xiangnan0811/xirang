package recovery

import (
	"slices"
	"testing"

	"xirang/backend/internal/backupasset"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRecoveryMetricsExposeOnlyClosedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveState(backupasset.ProviderRsync, JobStateQueued)
	metrics.ObserveOutcome(backupasset.ProviderRsync, JobStateSucceeded, MetricOutcomeSuccess)
	metrics.ObserveCategory(AuthorizationReceiptCategoryWrite, MetricOutcomeSuccess)

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	actual := make(map[string][]string, len(families))
	for _, family := range families {
		if len(family.GetMetric()) == 0 {
			continue
		}
		for _, pair := range family.GetMetric()[0].GetLabel() {
			actual[family.GetName()] = append(actual[family.GetName()], pair.GetName())
		}
		slices.Sort(actual[family.GetName()])
	}
	want := map[string][]string{
		"xirang_backup_asset_recovery_states_total":     {"provider", "state"},
		"xirang_backup_asset_recovery_outcomes_total":   {"outcome", "provider", "state"},
		"xirang_backup_asset_recovery_categories_total": {"category", "outcome"},
	}
	if len(actual) != len(want) {
		t.Fatalf("metric families=%v, want %v", actual, want)
	}
	for name, labels := range want {
		if !slices.Equal(actual[name], labels) {
			t.Fatalf("metric %s labels=%v, want %v", name, actual[name], labels)
		}
	}
}

func TestRecoveryMetricsMapInvalidTypedValuesToUnknown(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveState(backupasset.ProviderKind("private-provider"), JobState("private-state"))
	metrics.ObserveOutcome(
		backupasset.ProviderKind("private-provider"), JobState("private-state"), MetricOutcome("private-outcome"),
	)
	metrics.ObserveCategory(AuthorizationReceiptCategory("private-category"), MetricOutcome("private-outcome"))

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetValue() != "unknown" {
					t.Fatalf("metric %s leaked invalid %s=%q", family.GetName(), pair.GetName(), pair.GetValue())
				}
			}
		}
	}
}

func TestRecoveryMetricsExcludeUnsupportedCommandProvider(t *testing.T) {
	if got := recoveryMetricProvider(backupasset.ProviderCommand); got != "unknown" {
		t.Fatalf("command provider metric label=%q, want unknown because Recovery does not support it", got)
	}
}

func TestRecoveryTerminalMetricOutcomeMapping(t *testing.T) {
	tests := map[JobState]MetricOutcome{
		JobStateSucceeded:      MetricOutcomeSuccess,
		JobStateCanceled:       MetricOutcomeBlocked,
		JobStateDegraded:       MetricOutcomeFailure,
		JobStateFailed:         MetricOutcomeFailure,
		JobStateNeedsAttention: MetricOutcomeFailure,
	}
	for state, want := range tests {
		if got := recoveryTerminalMetricOutcome(state); got != want {
			t.Fatalf("terminal state %q maps to %q, want %q", state, got, want)
		}
	}
	if got := recoveryTerminalMetricOutcome(JobState("private-state")); got != MetricOutcome("unknown") {
		t.Fatalf("invalid terminal state maps to %q, want unknown", got)
	}
}
