package backupasset

import "testing"

func TestRsyncVersioningRequestsAcceptOnlyApprovedModesAndChoices(t *testing.T) {
	preflight := RsyncVersioningPreflightRequest{
		TaskID: 7, ExpectedTaskRevision: 9, RequestedMode: PublicationVersionedHardlink,
	}
	if err := preflight.Validate(); err != nil {
		t.Fatalf("valid preflight request rejected: %v", err)
	}

	preflight.RequestedMode = PublicationLegacyMutable
	if err := preflight.Validate(); err == nil {
		t.Fatal("legacy mode accepted as a versioning preflight request")
	}

	activation := RsyncVersioningActivationRequest{
		TaskID: 7, ExpectedTaskRevision: 9, PreflightID: "0123456789abcdef0123456789abcdef",
		MigrationChoice: RsyncVersioningFirstNewPoint,
	}
	if err := activation.Validate(); err != nil {
		t.Fatalf("valid activation request rejected: %v", err)
	}
	activation.MigrationChoice = "metadata_only"
	if err := activation.Validate(); err == nil {
		t.Fatal("unsupported migration choice accepted")
	}
}

func TestRsyncVersioningSummaryRejectsUnsafeProjectionValues(t *testing.T) {
	summary := RsyncVersioningSummary{
		Mode: PublicationVersionedFullCopy, State: RsyncVersioningReady,
		ReasonCode: RsyncVersioningReasonReady, CapabilityRevision: 3, TaskRevision: "9007199254740993",
	}
	if err := summary.Validate(); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	summary.State = "raw_provider_error"
	if err := summary.Validate(); err == nil {
		t.Fatal("unknown summary state accepted")
	}
}
