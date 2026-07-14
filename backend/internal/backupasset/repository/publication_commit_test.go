package repository

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
)

func TestRecordProviderCommitAdvancesOnlyPreparingToVerifying(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := execution.RecordProviderCommit(context.Background(), fixture.commitEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ProviderCommitRecorded || outcome.State != backupasset.RecoveryPointVerifying || outcome.RecoveryPointID != execution.Attempt().RecoveryPointID {
		t.Fatalf("commit outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", outcome.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointVerifying) {
		t.Fatalf("point state=%q, want verifying", point.State)
	}
	var activeLeases int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive).Count(&activeLeases).Error; err != nil || activeLeases != 0 {
		t.Fatalf("active execution leases=%d err=%v, want zero", activeLeases, err)
	}
}

func TestRecordProviderCommitPersistsEncryptedLocatorAndSafeDigestEnvelope(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), fixture.commitEvidence()); err != nil {
		t.Fatal(err)
	}
	var encryptedLocator, lineage, consistency string
	if err := fixture.db.Raw("SELECT encrypted_provider_locator, lineage_json, consistency_json FROM recovery_points WHERE id = ?", execution.Attempt().RecoveryPointID).Row().Scan(&encryptedLocator, &lineage, &consistency); err != nil {
		t.Fatal(err)
	}
	fullID := fixture.commitEvidence().NativePointID
	if !strings.HasPrefix(encryptedLocator, "enc:v2:") || strings.Contains(encryptedLocator, fullID) || strings.Contains(lineage, fullID) || strings.Contains(consistency, fullID) || strings.Contains(consistency, fixture.attemptIdentity()) {
		t.Fatalf("commit evidence leaked in persisted safe fields: locator=%q lineage=%q consistency=%q", encryptedLocator, lineage, consistency)
	}
	decoded, err := backupasset.DecodePublicationConsistency(consistency)
	if err != nil || decoded.ProviderCommitDigest == "" || decoded.RepositoryIdentityDigest == "" || decoded.RequestedTagDigest == "" {
		t.Fatalf("safe consistency=%+v err=%v", decoded, err)
	}
}

func TestRecordProviderCommitIsIdempotentOnlyForByteEquivalentEvidence(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	evidence := fixture.commitEvidence()
	first, err := execution.RecordProviderCommit(context.Background(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	second, err := execution.RecordProviderCommit(context.Background(), evidence)
	if err != nil || second != first {
		t.Fatalf("idempotent replay=%+v first=%+v err=%v", second, first, err)
	}
	evidence.LogicalBytes++
	if _, err := execution.RecordProviderCommit(context.Background(), evidence); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("different commit evidence error=%v, want conflict", err)
	}
}

func TestRecordProviderCommitReleasesExecutionLeaseAndWakeNeverBlocks(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	var wakes atomic.Int32
	fixture.service.tryWake = func(id string) bool {
		if id != fixture.expectedPointID(t) {
			t.Fatalf("wake id=%q", id)
		}
		wakes.Add(1)
		return false
	}
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), fixture.commitEvidence()); err != nil {
		t.Fatal(err)
	}
	if wakes.Load() != 1 {
		t.Fatalf("wake calls=%d, want one", wakes.Load())
	}
}

func TestRecordProviderCommitConflictCannotClaimAnotherRunOrNativePoint(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	evidence := fixture.commitEvidence()
	evidence.RepositoryIdentity = provider.NativeResticIdentityPrefix + strings.Repeat("b", 64)
	if _, err := execution.RecordProviderCommit(context.Background(), evidence); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("identity drift commit error=%v, want conflict", err)
	}
}

func TestRecordProviderCommitNativeSourceConstraintReturnsConflictWithoutMutation(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	evidence := fixture.commitEvidence()
	claimed := model.RecoveryPoint{
		ID: strings.Repeat("d", 32), RepositoryID: fixture.repository.ID,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointFailed),
		SourceFingerprint: resticSourceFingerprint(evidence.RepositoryIdentity, evidence.NativePointID),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&claimed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), evidence); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("native source conflict error=%v, want conflict", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", execution.Attempt().RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) || point.SourceFingerprint != "" || point.EncryptedProviderLocator != "" {
		t.Fatalf("native conflict mutated point=%+v", point)
	}
}

func TestPublicationDeferPersistsOnlyCompletionAndSafeCode(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", execution.Attempt().RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil || point.State != string(backupasset.RecoveryPointPreparing) || consistency.Completion != backupasset.CompletionKnownExitZero || consistency.Code != backupasset.FailureEvidenceMissingSummary ||
		strings.Contains(point.ConsistencyJSON, fixture.commitEvidence().NativePointID) {
		t.Fatalf("deferred point=%+v consistency=%+v err=%v", point, consistency, err)
	}
}

func TestPublicationDeferIdenticalReplayDoesNotRotateConsistencyOrDuplicateAudit(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	deferral := publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}
	if err := execution.Defer(context.Background(), deferral); err != nil {
		t.Fatal(err)
	}
	var first model.RecoveryPoint
	if err := fixture.db.First(&first, "id = ?", execution.Attempt().RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := execution.Defer(context.Background(), deferral); err != nil {
		t.Fatal(err)
	}
	var second model.RecoveryPoint
	if err := fixture.db.First(&second, "id = ?", execution.Attempt().RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if second.ConsistencyJSON != first.ConsistencyJSON || second.UpdatedAt != first.UpdatedAt {
		t.Fatalf("identical defer replay changed point: first=%+v second=%+v", first, second)
	}
	if len(fixture.audit.inputs) != 2 {
		t.Fatalf("identical defer replay duplicated audit events=%+v", fixture.audit.inputs)
	}
}

func TestPublicationRejectAllowsOnlyPreCommandPreconditionFailure(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Reject(context.Background(), backupasset.FailureProviderNonzeroExit); err == nil {
		t.Fatal("Reject accepted post-command code")
	}
	if err := execution.Reject(context.Background(), backupasset.FailurePublicationPreconditionMissing); err != nil {
		t.Fatal(err)
	}
	fixture.requirePointStateAndNoActiveLease(t, execution.Attempt().RecoveryPointID, backupasset.RecoveryPointFailed)
}

func TestPublicationFailRejectsUnknownCodesAndNeverOverwritesCommitted(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Fail(context.Background(), backupasset.PublicationFailureCode("not-allowlisted")); err == nil {
		t.Fatal("Fail accepted unknown code")
	}
	if _, err := execution.RecordProviderCommit(context.Background(), fixture.commitEvidence()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", execution.Attempt().RecoveryPointID).Update("state", backupasset.RecoveryPointCommitted).Error; err != nil {
		t.Fatal(err)
	}
	if err := execution.Fail(context.Background(), backupasset.FailureProviderNonzeroExit); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Fail committed point error=%v, want conflict", err)
	}
	fixture.requirePointStateAndNoActiveLease(t, execution.Attempt().RecoveryPointID, backupasset.RecoveryPointCommitted)
}

func TestPublicationFailIdenticalReplayDoesNotDuplicateAudit(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Fail(context.Background(), backupasset.FailureProviderNonzeroExit); err != nil {
		t.Fatal(err)
	}
	if err := execution.Fail(context.Background(), backupasset.FailureProviderNonzeroExit); err != nil {
		t.Fatalf("identical terminal replay error=%v", err)
	}
	fixture.requirePointStateAndNoActiveLease(t, execution.Attempt().RecoveryPointID, backupasset.RecoveryPointFailed)
	if len(fixture.audit.inputs) != 2 {
		t.Fatalf("identical terminal replay duplicated audit=%+v", fixture.audit.inputs)
	}
}

func TestPublicationMutationRejectsStaleFenceInsideSameTransaction(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", execution.Attempt().Fence.LeaseID).Update("fence_token", strings.Repeat("d", 64)).Error; err != nil {
		t.Fatal(err)
	}
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionOutcomeUnknown, Code: backupasset.FailureProviderTimeout}); !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("stale fence Defer error=%v, want fence lost", err)
	}
	fixture.requirePointStateAndActiveLease(t, execution.Attempt().RecoveryPointID, backupasset.RecoveryPointPreparing)
}

func TestRecordLegacyBlockWritesTypedAuditAndMetricWithoutRawFacts(t *testing.T) {
	fixture := newPublicationFixture(t, false, publication.AdmissionPristineLegacy)
	metrics := &legacyBlockMetrics{}
	fixture.service.metrics = metrics
	runID := fixture.taskRun.ID
	audit, err := publication.NewSystemLegacyBlockAuditContext(fixture.task.ID, &runID, publication.OperationLegacyRetention)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecordLegacyBlock(context.Background(), publication.LegacyBlock{
		TaskID: fixture.task.ID, TaskRunID: &runID, Operation: publication.OperationLegacyRetention, Audit: audit,
	}); err != nil {
		t.Fatal(err)
	}
	if got := metrics.operations; len(got) != 1 || got[0] != publication.OperationLegacyRetention {
		t.Fatalf("legacy-block metrics=%v", got)
	}
	if len(fixture.audit.inputs) != 1 {
		t.Fatalf("legacy-block audits=%+v", fixture.audit.inputs)
	}
	input := fixture.audit.inputs[0]
	if input.Action != backupasset.AuditActionResticLegacyOperationBlocked || input.Outcome != backupasset.AuditOutcomeBlocked ||
		input.TaskID == nil || *input.TaskID != fixture.task.ID || input.TaskRunID == nil || *input.TaskRunID != runID ||
		input.Fields[backupasset.AuditFieldStage] != string(publication.StageExecution) ||
		input.Fields[backupasset.AuditFieldCode] != string(backupasset.FailureLegacyOperationBlocked) ||
		input.Fields[backupasset.AuditFieldOperation] != string(publication.OperationLegacyRetention) ||
		input.Fields[backupasset.AuditFieldCorrelationID] != audit.CorrelationID {
		t.Fatalf("legacy-block audit=%+v", input)
	}
	if input.RepositoryID != "" || input.RecoveryPointID != "" || input.FailureCode != "" {
		t.Fatalf("legacy-block leaked unrelated facts: %+v", input)
	}
}

type legacyBlockMetrics struct {
	publication.NoopMetrics
	operations []publication.ResticOperation
}

func (metrics *legacyBlockMetrics) ObserveLegacyBlocked(operation publication.ResticOperation) {
	metrics.operations = append(metrics.operations, operation)
}

func TestPublicationStateAuditsContainOnlyActorOpaqueIDsSafeCountsCodeAndCorrelation(t *testing.T) {
	tests := []struct {
		name     string
		complete func(t *testing.T, execution publication.Execution, fixture *publicationFixture)
		action   backupasset.AuditAction
		outcome  backupasset.AuditOutcome
		status   backupasset.RecoveryPointState
		code     backupasset.PublicationFailureCode
	}{
		{
			name: "provider commit", action: backupasset.AuditActionRecoveryPointPublicationCommit, outcome: backupasset.AuditOutcomeSuccess,
			status: backupasset.RecoveryPointVerifying,
			complete: func(t *testing.T, execution publication.Execution, fixture *publicationFixture) {
				t.Helper()
				if _, err := execution.RecordProviderCommit(context.Background(), fixture.commitEvidence()); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "evidence deferral", action: backupasset.AuditActionRecoveryPointPublicationVerify, outcome: backupasset.AuditOutcomeFailure,
			status: backupasset.RecoveryPointPreparing, code: backupasset.FailureEvidenceMissingSummary,
			complete: func(t *testing.T, execution publication.Execution, _ *publicationFixture) {
				t.Helper()
				if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "terminal failure", action: backupasset.AuditActionRecoveryPointPublicationFail, outcome: backupasset.AuditOutcomeFailure,
			status: backupasset.RecoveryPointFailed, code: backupasset.FailureProviderNonzeroExit,
			complete: func(t *testing.T, execution publication.Execution, _ *publicationFixture) {
				t.Helper()
				if err := execution.Fail(context.Background(), backupasset.FailureProviderNonzeroExit); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
			fixture.connectExactResticBinding(t)
			execution, err := fixture.service.Prepare(context.Background(), fixture.run())
			if err != nil {
				t.Fatal(err)
			}
			test.complete(t, execution, fixture)
			if len(fixture.audit.inputs) != 2 {
				t.Fatalf("audit events=%+v", fixture.audit.inputs)
			}
			input := fixture.audit.inputs[1]
			if input.Action != test.action || input.Outcome != test.outcome || input.RepositoryID != fixture.repository.ID ||
				input.RecoveryPointID != execution.Attempt().RecoveryPointID || input.TaskID == nil || *input.TaskID != fixture.task.ID ||
				input.TaskRunID == nil || *input.TaskRunID != fixture.taskRun.ID || input.Fields[backupasset.AuditFieldStage] != string(publication.StageExecution) ||
				input.Fields[backupasset.AuditFieldStatus] != string(test.status) || input.Fields[backupasset.AuditFieldCorrelationID] != fixture.run().Audit.CorrelationID {
				t.Fatalf("publication audit=%+v", input)
			}
			if test.code == "" {
				if _, ok := input.Fields[backupasset.AuditFieldCode]; ok {
					t.Fatalf("unexpected success audit code=%+v", input.Fields)
				}
			} else if input.Fields[backupasset.AuditFieldCode] != string(test.code) {
				t.Fatalf("failure audit fields=%+v", input.Fields)
			}
			for field := range input.Fields {
				switch field {
				case backupasset.AuditFieldStage, backupasset.AuditFieldStatus, backupasset.AuditFieldCode, backupasset.AuditFieldCorrelationID:
				default:
					t.Fatalf("unsafe publication audit field %q in %+v", field, input.Fields)
				}
			}
		})
	}
}
