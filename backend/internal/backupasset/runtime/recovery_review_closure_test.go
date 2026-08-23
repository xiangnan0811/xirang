package runtime

import "testing"

// This selector exercises both directions of the shared same-node writer
// exclusion and the simultaneous Task/Recovery single-winner transaction.
func TestRecoveryReviewF5OrdinaryWriterExclusion(t *testing.T) {
	TestNodeWriteCoordinatorUnexpiredRecoveryLeaseBlocksTaskAndRecovery(t)
	TestNodeWriteCoordinatorActiveLeaseRejectsManagerTriggersWithoutResidualRuns(t)
	TestNodeWriteCoordinatorRecoveryAdmissionBlocksActiveTaskRuns(t)
	TestNodeWriteCoordinatorSameNodeConcurrentTaskAndRecoveryHaveOneDurableWinner(t)
}

// These selectors bind Task 11 downgrade review names to the runtime tests
// that exercise the disabled cleanup graph and sticky downgrade fence.
func TestRecoveryReviewF7FeatureDisableKeepsCleanup(t *testing.T) {
	TestManagedRecoveryRuntimeDisableRetainsResultAndReconciliationFacades(t)
}

func TestRecoveryReviewF7DowngradeReadiness(t *testing.T) {
	TestManagedRecoveryRuntimeDowngradeReadinessMatrixIsDisabledStickyAndNeverRunsDown(t)
}

func TestRecoveryReviewF7ForwardFixOnlyAfterUse(t *testing.T) {
	TestManagedRecoveryDowngradeDBInspectorMatchesPairedDownGuard(t)
}
