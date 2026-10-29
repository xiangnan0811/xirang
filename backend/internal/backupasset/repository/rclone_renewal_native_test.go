package repository

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestRcloneVersioningRenewalNativePreservesProofAndCapability(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	activation, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint})
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, activation.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	initial, err := fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, initial, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	var task model.Task
	if err := fixture.db.First(&task, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, _ := managedRsyncTaskRevision(task)
	plan, err := fixture.service.loadRcloneBindingWorkflowPlan(context.Background(), task.ID, revision, rcloneBindingSetupNative)
	if err != nil {
		t.Fatal(err)
	}
	before := *plan.managedBinding
	now := before.PreflightExpiresAt.Add(time.Second)
	fixture.service.now = func() time.Time { return now }
	expired, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, expired, backupasset.RcloneStatePreflightRequired, backupasset.RcloneReasonPreflightExpired, backupasset.RcloneRollbackCleanAvailable)
	stub := fixture.service.rclonePreflighter.(*rcloneVersioningPreflighterStub)
	// A new canary necessarily has a different proof; deterministic capabilities remain equal.
	stub.evidence.RepositoryMarkerDigest = strings.Repeat("a", 64)
	stub.evidence.EvidenceDigest = strings.Repeat("b", 64)
	stub.evidence.Native.CanaryEncryptionEvidenceDigest = strings.Repeat("c", 64)
	ticket, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationNativeObjectVersions})
	if err != nil || ticket.Purpose != backupasset.RclonePreflightPurposeRenewal {
		t.Fatalf("native renewal preflight=%+v err=%v", ticket, err)
	}
	assertRcloneRollbackSummary(t, ticket.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	ready, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, ready, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	if stub.nativeInput.CapabilityRevision != before.CapabilityRevision || stub.nativeInput.ExternalID != before.Native.ExternalID ||
		!reflect.DeepEqual(stub.nativeInput.RetainedReadKeys, before.Native.RetainedReadKeys) {
		t.Fatal("native renewal did not reuse binding identity and retained keys")
	}
	renewed, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: ticket.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint})
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, renewed.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	persisted, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, persisted, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	if err := fixture.db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, _ = managedRsyncTaskRevision(task)
	plan, err = fixture.service.loadRcloneBindingWorkflowPlan(context.Background(), task.ID, revision, rcloneBindingSetupNative)
	if err != nil {
		t.Fatal(err)
	}
	after := *plan.managedBinding
	if !after.PreflightExpiresAt.After(before.PreflightExpiresAt) {
		t.Fatal("native expiry did not advance")
	}
	after.PreflightExpiresAt = before.PreflightExpiresAt
	if !reflect.DeepEqual(after, before) {
		t.Fatal("native renewal changed historical identity or proof")
	}
}
