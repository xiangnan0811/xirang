package search

import "testing"

func TestSearchMetricsLabelsRemainClosedAndLowCardinality(t *testing.T) {
	metrics := NoopMetrics{}
	for _, outcome := range []BuildOutcome{BuildOutcomeSuccess, BuildOutcomeFailure, BuildOutcomeCanceled, BuildOutcomeFenced} {
		metrics.ObserveBuild(outcome)
	}
	for _, outcome := range []ScanOutcome{ScanOutcomeSuccess, ScanOutcomeFailure, ScanOutcomeDisabled} {
		metrics.ObserveScan(outcome)
	}
	if validBuildOutcome(BuildOutcome("point-id")) || validScanOutcome(ScanOutcome("repository-id")) {
		t.Fatal("high-cardinality metric label was accepted")
	}
}
