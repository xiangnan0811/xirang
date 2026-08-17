package content

import "testing"

// Post-GREEN review selector closure for the immutable Task 11 publication ledger.
func TestRecoveryReviewF4ContentRevalidatesPublishBarrier(t *testing.T) {
	TestBrokerRecoveryResultHeartbeatStopsStreamAfterPublicationDrift(t)
}
