package search

type BuildOutcome string

const (
	BuildOutcomeSuccess  BuildOutcome = "success"
	BuildOutcomeFailure  BuildOutcome = "failure"
	BuildOutcomeCanceled BuildOutcome = "canceled"
	BuildOutcomeFenced   BuildOutcome = "fenced"
)

type ScanOutcome string

const (
	ScanOutcomeSuccess  ScanOutcome = "success"
	ScanOutcomeFailure  ScanOutcome = "failure"
	ScanOutcomeDisabled ScanOutcome = "disabled"
)

type Metrics interface {
	ObserveBuild(BuildOutcome)
	ObserveScan(ScanOutcome)
	SetActiveBuilds(int)
	AddReconciledAbandoned(int64)
	AddReconciledOverlays(int64)
}

type NoopMetrics struct{}

func (NoopMetrics) ObserveBuild(BuildOutcome)    {}
func (NoopMetrics) ObserveScan(ScanOutcome)      {}
func (NoopMetrics) SetActiveBuilds(int)          {}
func (NoopMetrics) AddReconciledAbandoned(int64) {}
func (NoopMetrics) AddReconciledOverlays(int64)  {}

func validBuildOutcome(value BuildOutcome) bool {
	return value == BuildOutcomeSuccess || value == BuildOutcomeFailure || value == BuildOutcomeCanceled || value == BuildOutcomeFenced
}

func validScanOutcome(value ScanOutcome) bool {
	return value == ScanOutcomeSuccess || value == ScanOutcomeFailure || value == ScanOutcomeDisabled
}
