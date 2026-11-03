package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestProcessVerifyingPointOutcomeSelectsImmutableSameTaskPreviousCommittedPoint(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	previousNativeID := strings.Repeat("b", 64)
	previousCapturedAt := fixture.now.Add(-10 * time.Second)
	previousRun := model.TaskRun{TaskID: fixture.task.ID, TriggerType: "manual", Status: "success", StartedAt: &previousCapturedAt}
	if err := fixture.db.Create(&previousRun).Error; err != nil {
		t.Fatal(err)
	}
	previousLineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: fixture.link.ID, TaskID: fixture.task.ID, TaskRunID: previousRun.ID,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot), PointCodecVersion: 1, TagCodecVersion: 1,
		StartedAt: previousCapturedAt, PreparedAt: previousCapturedAt, PointDeadlineAt: previousCapturedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	previousLocator, err := json.Marshal(resticPointLocatorV1{Version: 1, Provider: string(backupasset.ProviderRestic), FullSnapshotID: previousNativeID})
	if err != nil {
		t.Fatal(err)
	}
	previousConsistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	previousPoint := model.RecoveryPoint{
		ID: strings.Repeat("f", 32), RepositoryID: fixture.repository.ID, ProducingTaskID: &fixture.task.ID, ProducingTaskRunID: &previousRun.ID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.node.ID, ProducingNodeNameSnapshot: fixture.node.Name,
		LineageJSON: previousLineage, EncryptedProviderLocator: string(previousLocator), Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), ConsistencyJSON: previousConsistency, FidelityJSON: "{}",
		SourceFingerprint: resticSourceFingerprint(fixture.attemptIdentity(), previousNativeID), ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("d", 64),
		CapturedAt: &previousCapturedAt, CommittedAt: &previousCapturedAt, CapabilityRevision: 1, CapabilitiesJSON: "{}",
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: previousCapturedAt, UpdatedAt: previousCapturedAt,
	}
	if err := fixture.db.Create(&previousPoint).Error; err != nil {
		t.Fatal(err)
	}

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, _ provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestComplete, EntryCount: 1, LogicalBytes: 1, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: commit.CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointCommitted || outcome.PreviousNativePointID != previousNativeID {
		t.Fatalf("committed outcome=%+v, want previous native point %q", outcome, previousNativeID)
	}
}

func TestProcessVerifyingPointUsesFreshFenceAndOriginalDeadline(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	firstFence := resticAttemptForExecution(t, execution).Fence
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, evidence provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		if evidence != commit {
			t.Fatalf("manifest commit evidence=%+v want=%+v", evidence, commit)
		}
		if attempt.Fence.LeaseID == firstFence.LeaseID || !attempt.PointDeadlineAt.Equal(resticAttemptForExecution(t, execution).PointDeadlineAt) {
			t.Fatalf("manifest attempt did not use a fresh lease/fixed deadline: %+v", attempt)
		}
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestComplete, EntryCount: 2, LogicalBytes: 3456, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: commit.CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointCommitted || !outcome.ProviderCommitRecorded || outcome.NativePointID != commit.NativePointID || !outcome.CapturedAt.Equal(commit.CaptureStartedAt) {
		t.Fatalf("manifest outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", resticAttemptForExecution(t, execution).RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointCommitted) || point.CommittedAt == nil || point.CapturedAt == nil || !point.CapturedAt.Equal(commit.CaptureStartedAt) ||
		point.ManifestDigest != strings.Repeat("d", 64) || point.EntryCount != 2 || point.LogicalBytes != 3456 {
		t.Fatalf("committed point=%+v", point)
	}
	var manifests []model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ?", point.ID).Find(&manifests).Error; err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !manifests[0].IsActive || manifests[0].Completeness != string(backupasset.ManifestComplete) {
		t.Fatalf("committed manifest=%+v", manifests)
	}
	var encryptedEvidence string
	if err := fixture.db.Raw("SELECT encrypted_commit_evidence FROM recovery_point_manifests WHERE id = ?", manifests[0].ID).Row().Scan(&encryptedEvidence); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encryptedEvidence, "enc:v2:") || strings.Contains(encryptedEvidence, commit.NativePointID) {
		t.Fatalf("manifest evidence was not encrypted at rest: %q", encryptedEvidence)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("active manifest lease=%d err=%v", active, err)
	}
}

func TestProcessVerifyingPointRejectsLateManifestAfterTakeover(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(fixture.commitEvidence())); err != nil {
		t.Fatal(err)
	}
	started := make(chan provider.ResticAttemptV1, 1)
	releaseBuilder := make(chan struct{})
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, _ provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		started <- attempt
		<-releaseBuilder
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestComplete, EntryCount: 1, LogicalBytes: 1, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: fixture.commitEvidence().CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID)
		result <- err
	}()
	claim := <-started
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", claim.Fence.LeaseID).Update("fence_token", strings.Repeat("e", 64)).Error; err != nil {
		t.Fatal(err)
	}
	close(releaseBuilder)
	if err := <-result; !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("late manifest error=%v, want fence lost", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", resticAttemptForExecution(t, execution).RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointVerifying) {
		t.Fatalf("late manifest changed point=%+v", point)
	}
	var manifests int64
	if err := fixture.db.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ?", point.ID).Count(&manifests).Error; err != nil || manifests != 0 {
		t.Fatalf("late manifest rows=%d err=%v", manifests, err)
	}
}

func TestProcessVerifyingPointDetectsTagRewriteWhenCommittedIDDisappears(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.build = func(context.Context, provider.ResticAttemptV1, provider.ResticCommitV1, provider.ManifestLimits) (provider.ResticManifestV1, error) {
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Generator: "xirang-restic-ls", GeneratorVersion: "1", Completeness: backupasset.ManifestUnavailable,
			Fidelity: provider.ResticManifestFidelityV1(), FailureCode: backupasset.FailureManifestUnavailable,
		}, nil
	}
	rewrittenID := strings.Repeat("d", 64)
	fixture.publisher.lookup = func(_ context.Context, attempt provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		return []provider.ResticSnapshotObservation{{
			RepositoryIdentity: fixture.attemptIdentity(), NativePointID: rewrittenID, SnapshotTime: commit.CaptureStartedAt,
			Tags: []string{attempt.RequiredTags[0], attempt.RequiredTags[1]},
		}}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureProviderSnapshotRewritten {
		t.Fatalf("rewritten manifest outcome=%+v", outcome)
	}
}

func TestProcessVerifyingPointPersistsInactivePartialDiagnosticAndFailsResourceLimit(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(fixture.commitEvidence())); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, _ provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestPartial, EntryCount: 1, LogicalBytes: 1, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: fixture.commitEvidence().CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
			FailureCode: backupasset.FailureProviderResourceLimit,
		}, nil
	}
	outcome, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureProviderResourceLimit {
		t.Fatalf("partial resource-limit outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", resticAttemptForExecution(t, execution).RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || point.ManifestDigest != "" || point.EntryCount != 0 || point.LogicalBytes != 0 {
		t.Fatalf("partial diagnostic projected trusted evidence onto point=%+v", point)
	}
	var manifests []model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ?", point.ID).Find(&manifests).Error; err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].IsActive || manifests[0].Completeness != string(backupasset.ManifestPartial) {
		t.Fatalf("partial diagnostic manifests=%+v", manifests)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("partial diagnostic lease=%d err=%v", active, err)
	}
}

func TestReconcilePreparingKnownExitZeroRebuildsFromValidStoredSummary(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	fixture.publisher.lookup = func(_ context.Context, recovered provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		if recovered.RequiredTags != attempt.RequiredTags || recovered.RecoveryPointID != attempt.RecoveryPointID {
			t.Fatalf("reconcile attempt=%+v want tags/point from %+v", recovered, attempt)
		}
		return []provider.ResticSnapshotObservation{{
			RepositoryIdentity: fixture.attemptIdentity(), NativePointID: commit.NativePointID, SnapshotTime: commit.CaptureStartedAt,
			Tags: []string{attempt.RequiredTags[0], attempt.RequiredTags[1]}, Summary: &provider.ResticStoredSummary{
				BackupStartedAt: commit.CaptureStartedAt, BackupFinishedAt: commit.CaptureFinishedAt,
				FilesProcessed: commit.FilesProcessed, LogicalBytes: commit.LogicalBytes,
			},
		}}, nil
	}
	outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointVerifying || !outcome.ProviderCommitRecorded || outcome.NativePointID != commit.NativePointID {
		t.Fatalf("recovered preparing outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointVerifying) || point.SourceFingerprint != resticSourceFingerprint(commit.RepositoryIdentity, commit.NativePointID) {
		t.Fatalf("stored-summary recovery point=%+v", point)
	}
}

func TestReconcilePreparingOutcomeUnknownQuarantinesCompletionUnproven(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionOutcomeUnknown, Code: backupasset.FailureProviderTimeout}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	fixture.publisher.lookup = func(_ context.Context, recovered provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		return []provider.ResticSnapshotObservation{{
			RepositoryIdentity: fixture.attemptIdentity(), NativePointID: commit.NativePointID, SnapshotTime: commit.CaptureStartedAt,
			Tags: []string{recovered.RequiredTags[0], recovered.RequiredTags[1]},
		}}, nil
	}
	outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureProviderCompletionUnproven || outcome.ProviderCommitRecorded {
		t.Fatalf("outcome-unknown quarantine outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || point.SourceFingerprint != resticSourceFingerprint(commit.RepositoryIdentity, commit.NativePointID) ||
		!strings.HasPrefix(point.EncryptedProviderLocator, "{") || consistency.Code != backupasset.FailureProviderCompletionUnproven || consistency.Completion != "" {
		t.Fatalf("outcome-unknown quarantine point=%+v consistency=%+v", point, consistency)
	}
	var encryptedLocator string
	if err := fixture.db.Raw("SELECT encrypted_provider_locator FROM recovery_points WHERE id = ?", point.ID).Row().Scan(&encryptedLocator); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encryptedLocator, "enc:v2:") || strings.Contains(encryptedLocator, commit.NativePointID) {
		t.Fatalf("outcome-unknown quarantine locator=%q", encryptedLocator)
	}
}

func TestListCandidatesSkipsLiveLeaseWhileReadinessIncludesEveryUnresolvedPoint(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	pointID := resticAttemptForExecution(t, execution).RecoveryPointID
	if candidates, err := fixture.service.ListCandidates(context.Background(), 10); err != nil || len(candidates) != 0 {
		t.Fatalf("live lease candidates=%v err=%v", candidates, err)
	}
	unresolved, err := fixture.service.HasUnresolvedPublication(context.Background())
	if err != nil || !unresolved {
		t.Fatalf("live lease unresolved=%v err=%v", unresolved, err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", resticAttemptForExecution(t, execution).Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err := fixture.service.ListCandidates(context.Background(), 10)
	if err != nil || len(candidates) != 1 || candidates[0] != pointID {
		t.Fatalf("released lease candidates=%v err=%v", candidates, err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Update("state", backupasset.RecoveryPointFailed).Error; err != nil {
		t.Fatal(err)
	}
	unresolved, err = fixture.service.HasUnresolvedPublication(context.Background())
	if err != nil || unresolved {
		t.Fatalf("terminal unresolved=%v err=%v", unresolved, err)
	}
}

func TestListCandidatesUsesFixedDatabaseQueriesForBatch(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	for _, digit := range []string{"1", "2", "3", "4"} {
		clone := point
		clone.ID = strings.Repeat(digit, 32)
		clone.ProducingTaskRunID = nil
		if err := fixture.db.Create(&clone).Error; err != nil {
			t.Fatal(err)
		}
	}

	queryCount := 0
	const callbackName = "phase10:count-publication-candidate-queries"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(*gorm.DB) {
		queryCount++
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixture.db.Callback().Query().Remove(callbackName) }()

	candidates, err := fixture.service.ListCandidates(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidate count=%d, want 3", len(candidates))
	}
	if queryCount > 2 {
		t.Fatalf("publication candidate queries=%d, want fixed maximum 2", queryCount)
	}
}

func TestListCandidatesFreshServiceWalksPastBackedOffPrefix(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	var template model.RecoveryPoint
	if err := fixture.db.First(&template, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", template.ID).
		Update("state", backupasset.RecoveryPointFailed).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(template.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	consistency.AttemptCount = 1
	consistency.LastAttemptAt = &fixture.now
	backedOff, err := backupasset.EncodePublicationConsistency(consistency)
	if err != nil {
		t.Fatal(err)
	}
	targetID := fmt.Sprintf("%032x", 11)
	for index := 1; index <= 11; index++ {
		clone := template
		clone.ID = fmt.Sprintf("%032x", index)
		clone.ProducingTaskRunID = nil
		clone.UpdatedAt = fixture.now.Add(-time.Hour)
		if index <= 10 {
			clone.ConsistencyJSON = backedOff
			clone.UpdatedAt = fixture.now
		}
		if err := fixture.db.Create(&clone).Error; err != nil {
			t.Fatal(err)
		}
	}

	for restart := 0; restart < 2; restart++ {
		restarted := *fixture.service
		candidates, err := restarted.ListCandidates(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 || candidates[0] != targetID {
			t.Fatalf("restart %d candidates=%v, want [%s]", restart, candidates, targetID)
		}
	}
}

func TestHasUnresolvedPublicationRejectsInvalidLaterPointInsteadOfReportingReady(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	invalid := model.RecoveryPoint{
		ID: strings.Repeat("d", 32), RepositoryID: fixture.repository.ID,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointPreparing),
		LineageJSON: "{}", ConsistencyJSON: "{}", FidelityJSON: "{}",
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&invalid).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.HasUnresolvedPublication(context.Background()); err == nil {
		t.Fatal("readiness accepted an unresolved point with an invalid publication codec")
	}
}

func TestProcessVerifyingPointTakesOverExpiredPublicationLease(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(fixture.commitEvidence())); err != nil {
		t.Fatal(err)
	}
	previousFence := resticAttemptForExecution(t, execution).Fence
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", previousFence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseActive, "released_at": nil, "lease_expires_at": fixture.now.Add(-time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, _ provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		if attempt.Fence.LeaseID != previousFence.LeaseID || attempt.Fence.FenceToken == previousFence.FenceToken || attempt.Fence.AttemptID == previousFence.AttemptID {
			t.Fatalf("expired lease was not fenced-taken-over: old=%+v new=%+v", previousFence, attempt.Fence)
		}
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestComplete, EntryCount: 1, LogicalBytes: 1, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: fixture.commitEvidence().CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}
	if _, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID); err != nil {
		t.Fatal(err)
	}
}

func TestExpireAtDeadlineRequiresElapsedDeadlineNoLiveLeaseAndExactRevision(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	pointID := resticAttemptForExecution(t, execution).RecoveryPointID

	if _, expired, err := fixture.service.expireAtDeadline(context.Background(), pointID); err != nil || expired {
		t.Fatalf("live lease deadline expiry expired=%v err=%v", expired, err)
	}

	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		t.Fatal(err)
	}
	lineage.StartedAt = fixture.now.Add(-3 * time.Minute)
	lineage.PreparedAt = fixture.now.Add(-2 * time.Minute)
	lineage.PointDeadlineAt = fixture.now.Add(-time.Minute)
	encodedLineage, err := backupasset.EncodePublicationLineage(lineage)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Update("lineage_json", encodedLineage).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("recovery_point_id = ?", pointID).Delete(&model.RecoveryPointLease{}).Error; err != nil {
		t.Fatal(err)
	}

	outcome, expired, err := fixture.service.expireAtDeadline(context.Background(), pointID)
	if err != nil {
		t.Fatal(err)
	}
	if !expired || outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailurePublicationDeadlineExceeded {
		t.Fatalf("deadline outcome=%+v expired=%v", outcome, expired)
	}
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || consistency.Code != backupasset.FailurePublicationDeadlineExceeded || consistency.Completion != "" {
		t.Fatalf("expired point=%+v consistency=%+v", point, consistency)
	}
	if _, expired, err := fixture.service.expireAtDeadline(context.Background(), pointID); err != nil || expired {
		t.Fatalf("terminal replay expired=%v err=%v", expired, err)
	}
}

func TestReconcilePreparingZeroMatchKeepsStableMissingOriginUntilDeadline(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.publisher.lookup = func(_ context.Context, recovered provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		if recovered.RecoveryPointID != attempt.RecoveryPointID || recovered.RequiredTags != attempt.RequiredTags {
			t.Fatalf("reconciliation lookup attempt=%+v", recovered)
		}
		return nil, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointPreparing || outcome.RecoveryPointID != attempt.RecoveryPointID {
		t.Fatalf("zero-match outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) || consistency.FirstMissingObservedAt == nil || !consistency.FirstMissingObservedAt.Equal(fixture.now) || consistency.AttemptCount == 0 || consistency.LastAttemptAt == nil || !consistency.LastAttemptAt.Equal(fixture.now) {
		t.Fatalf("zero-match point=%+v consistency=%+v", point, consistency)
	}
	firstMissing := *consistency.FirstMissingObservedAt
	if _, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); !errors.Is(err, backupasset.ErrPublicationInProgress) {
		t.Fatalf("live zero-match replay err=%v", err)
	}
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err = backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if consistency.FirstMissingObservedAt == nil || !consistency.FirstMissingObservedAt.Equal(firstMissing) {
		t.Fatalf("first missing observation changed: %+v", consistency)
	}
}

func TestReconcilePreparingMissingGraceEmitsBoundedSafeAuditAndMetric(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	metrics := &missingGraceMetrics{}
	fixture.service.metrics = metrics
	clock := fixture.now
	fixture.service.now = func() time.Time { return clock }
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", resticAttemptForExecution(t, execution).Fence.LeaseID).Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.publisher.lookup = func(context.Context, provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		return nil, nil
	}
	if _, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", resticAttemptForExecution(t, execution).RecoveryPointID, backupasset.LeaseActive).Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(31 * time.Minute)
	if _, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID); err != nil {
		t.Fatal(err)
	}
	if len(fixture.audit.inputs) != 2 || len(metrics.outcomes) != 1 {
		t.Fatalf("missing grace did not emit one bounded audit/metric: audits=%+v outcomes=%+v", fixture.audit.inputs, metrics.outcomes)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", resticAttemptForExecution(t, execution).RecoveryPointID, backupasset.LeaseActive).Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": clock}).Error; err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if _, err := fixture.service.ProcessPoint(context.Background(), resticAttemptForExecution(t, execution).RecoveryPointID); err != nil {
		t.Fatal(err)
	}
	if len(fixture.audit.inputs) != 2 || len(metrics.outcomes) != 1 {
		t.Fatalf("missing grace audit replay was not deduplicated: audits=%+v outcomes=%+v", fixture.audit.inputs, metrics.outcomes)
	}
}

type missingGraceMetrics struct {
	publication.NoopMetrics
	outcomes []backupasset.PublicationOutcomeCode
}

func (metrics *missingGraceMetrics) ObserveOutcome(_ backupasset.ProviderKind, _ publication.PublicationStage, outcome backupasset.PublicationOutcomeCode) {
	metrics.outcomes = append(metrics.outcomes, outcome)
}

func TestReconcilePreparingTransientProviderFailureRemainsPending(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.publisher.lookup = func(context.Context, provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		return nil, context.DeadlineExceeded
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointPreparing || outcome.RecoveryPointID != attempt.RecoveryPointID || outcome.Code != "" {
		t.Fatalf("transient reconcile outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) {
		t.Fatalf("transient reconcile changed point=%+v", point)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("transient reconcile active lease=%d err=%v", active, err)
	}
}

func TestReconcilePreparingMultipleMatchesFailClosed(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	fixture.publisher.lookup = func(_ context.Context, recovered provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		return []provider.ResticSnapshotObservation{
			{
				RepositoryIdentity: fixture.attemptIdentity(), NativePointID: commit.NativePointID, SnapshotTime: commit.CaptureStartedAt,
				Tags: []string{recovered.RequiredTags[0], recovered.RequiredTags[1]}, Summary: &provider.ResticStoredSummary{BackupStartedAt: commit.CaptureStartedAt, BackupFinishedAt: commit.CaptureFinishedAt, FilesProcessed: commit.FilesProcessed, LogicalBytes: commit.LogicalBytes},
			},
			{
				RepositoryIdentity: fixture.attemptIdentity(), NativePointID: strings.Repeat("f", 64), SnapshotTime: commit.CaptureStartedAt,
				Tags: []string{recovered.RequiredTags[0], recovered.RequiredTags[1]}, Summary: &provider.ResticStoredSummary{BackupStartedAt: commit.CaptureStartedAt, BackupFinishedAt: commit.CaptureFinishedAt, FilesProcessed: commit.FilesProcessed, LogicalBytes: commit.LogicalBytes},
			},
		}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureAmbiguousRunTags || outcome.ProviderCommitRecorded {
		t.Fatalf("multiple-match outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || consistency.Code != backupasset.FailureAmbiguousRunTags || consistency.Completion != "" {
		t.Fatalf("multiple-match point=%+v consistency=%+v", point, consistency)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("multiple-match active lease=%d err=%v", active, err)
	}
}

func TestReconcilePreparingRewriteFailsWithoutClaimingChangedNativeID(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	original := strings.Repeat("e", 64)
	fixture.publisher.lookup = func(_ context.Context, recovered provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		return []provider.ResticSnapshotObservation{{
			RepositoryIdentity: fixture.attemptIdentity(), NativePointID: commit.NativePointID, SnapshotTime: commit.CaptureStartedAt,
			Tags: []string{recovered.RequiredTags[0], recovered.RequiredTags[1]}, OriginalPresent: true, Original: &original,
			Summary: &provider.ResticStoredSummary{BackupStartedAt: commit.CaptureStartedAt, BackupFinishedAt: commit.CaptureFinishedAt, FilesProcessed: commit.FilesProcessed, LogicalBytes: commit.LogicalBytes},
		}}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureProviderSnapshotRewritten || outcome.ProviderCommitRecorded {
		t.Fatalf("rewritten outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || consistency.Code != backupasset.FailureProviderSnapshotRewritten || !strings.HasPrefix(point.EncryptedProviderLocator, "{") || point.SourceFingerprint != resticSourceFingerprint(fixture.attemptIdentity(), commit.NativePointID) {
		t.Fatalf("rewritten point=%+v consistency=%+v", point, consistency)
	}
	var encryptedLocator string
	if err := fixture.db.Raw("SELECT encrypted_provider_locator FROM recovery_points WHERE id = ?", point.ID).Row().Scan(&encryptedLocator); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encryptedLocator, "enc:v2:") || strings.Contains(encryptedLocator, commit.NativePointID) {
		t.Fatalf("rewritten locator=%q", encryptedLocator)
	}
}

func TestReconcilePreparingMissingStoredSummaryFailsClosed(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if err := execution.Defer(context.Background(), publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: backupasset.FailureEvidenceMissingSummary}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.Fence.LeaseID).Updates(map[string]any{
		"status": backupasset.LeaseReleased, "released_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	fixture.publisher.lookup = func(_ context.Context, recovered provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
		return []provider.ResticSnapshotObservation{{
			RepositoryIdentity: fixture.attemptIdentity(), NativePointID: commit.NativePointID, SnapshotTime: commit.CaptureStartedAt,
			Tags: []string{recovered.RequiredTags[0], recovered.RequiredTags[1]},
		}}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureEvidenceMissingSummary || outcome.ProviderCommitRecorded {
		t.Fatalf("missing-summary outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || consistency.Code != backupasset.FailureEvidenceMissingSummary || consistency.Completion != backupasset.CompletionKnownExitZero {
		t.Fatalf("missing-summary point=%+v consistency=%+v", point, consistency)
	}
}

func TestProcessVerifyingPointTransientManifestErrorRemainsVerifying(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(fixture.commitEvidence())); err != nil {
		t.Fatal(err)
	}
	pointID := resticAttemptForExecution(t, execution).RecoveryPointID
	fixture.manifest.build = func(context.Context, provider.ResticAttemptV1, provider.ResticCommitV1, provider.ManifestLimits) (provider.ResticManifestV1, error) {
		return provider.ResticManifestV1{}, context.DeadlineExceeded
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), pointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointVerifying || outcome.RecoveryPointID != pointID || outcome.Code != "" {
		t.Fatalf("transient manifest outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointVerifying) {
		t.Fatalf("transient manifest changed point=%+v", point)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", pointID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("transient manifest active lease=%d err=%v", active, err)
	}
	var diagnostics []model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ?", pointID).Find(&diagnostics).Error; err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].IsActive || diagnostics[0].Completeness != string(backupasset.ManifestUnavailable) {
		t.Fatalf("transient manifest did not retain an inactive diagnostic: %+v", diagnostics)
	}
}

func TestProcessVerifyingPointFailsDeterministicManifestRewrite(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(fixture.commitEvidence())); err != nil {
		t.Fatal(err)
	}
	pointID := resticAttemptForExecution(t, execution).RecoveryPointID
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, _ provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestPartial, EntryCount: 1, LogicalBytes: 1, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: fixture.commitEvidence().CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
			FailureCode: backupasset.FailureProviderSnapshotRewritten,
		}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), pointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureProviderSnapshotRewritten || !outcome.ProviderCommitRecorded {
		t.Fatalf("manifest rewrite outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || consistency.Code != backupasset.FailureProviderSnapshotRewritten || consistency.Completion != "" {
		t.Fatalf("manifest rewrite point=%+v consistency=%+v", point, consistency)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", pointID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("manifest rewrite active lease=%d err=%v", active, err)
	}
}

func TestPublicationLeaseDeadlinePrecisionSQLite(t *testing.T) {
	runPublicationLeaseDeadlinePrecision(t, "sqlite")
}

func TestPublicationLeaseDeadlinePrecisionPostgres(t *testing.T) {
	runPublicationLeaseDeadlinePrecision(t, "postgres")
}

func runPublicationLeaseDeadlinePrecision(t *testing.T, engine string) {
	t.Helper()
	ctx := context.Background()

	t.Run("fresh_stage", func(t *testing.T) {
		fixture := newPublicationDeadlinePrecisionFixture(t, engine)
		commit := fixture.publication.commitEvidence()
		if _, err := fixture.execution.RecordProviderCommit(ctx, resticProviderCommit(commit)); err != nil {
			t.Fatalf("record provider commit: %v", err)
		}
		if previous := fixture.loadLease(t, fixture.initialLease.ID); previous.Status != string(backupasset.LeaseReleased) {
			t.Fatalf("execution lease was not released: %+v", previous)
		}
		var nextLeaseID string
		fixture.publication.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, evidence provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
			if evidence != commit || attempt.Fence.LeaseID == fixture.initialLease.ID || !attempt.PointDeadlineAt.Equal(fixture.rawDeadline) {
				t.Fatalf("new stage changed its source or deadline: attempt=%+v evidence=%+v", attempt, evidence)
			}
			nextLeaseID = attempt.Fence.LeaseID
			return provider.ResticManifestV1{
				DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
				Completeness: backupasset.ManifestComplete, EntryCount: 1, LogicalBytes: 1, Fidelity: provider.ResticManifestFidelityV1(),
				HeaderCapturedAt: commit.CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
			}, nil
		}
		outcome, err := fixture.publication.service.ProcessPoint(ctx, fixture.attempt.RecoveryPointID)
		if err != nil || outcome.State != backupasset.RecoveryPointCommitted || nextLeaseID == "" {
			t.Fatalf("fresh reconciliation outcome=%+v lease=%q err=%v", outcome, nextLeaseID, err)
		}
		next := fixture.loadLease(t, nextLeaseID)
		if next.Status != string(backupasset.LeaseReleased) || !next.AbsoluteDeadline.Equal(fixture.storedDeadline) {
			t.Fatalf("new stage lease changed deadline or was not released: %+v, want %v", next, fixture.storedDeadline)
		}
		fixture.requireLineage(t, backupasset.RecoveryPointCommitted)
	})

	t.Run("active_live", func(t *testing.T) {
		fixture := newPublicationDeadlinePrecisionFixture(t, engine)
		if _, err := fixture.publication.service.claimPreparingPoint(ctx, fixture.attempt.RecoveryPointID); !errors.Is(err, backupasset.ErrPublicationInProgress) {
			t.Fatalf("live publication lease error=%v, want publication in progress", err)
		}
		fixture.requireOriginalLease(t)
		fixture.requireLineage(t, backupasset.RecoveryPointPreparing)
	})

	t.Run("takeover", func(t *testing.T) {
		fixture := newPublicationDeadlinePrecisionFixture(t, engine)
		fixture.clock = fixture.initialLease.LeaseExpiresAt.Add(time.Second)
		claim, err := fixture.publication.service.claimPreparingPoint(ctx, fixture.attempt.RecoveryPointID)
		if err != nil {
			t.Fatalf("claim expired short lease: %v", err)
		}
		current := fixture.loadLease(t, fixture.initialLease.ID)
		if claim.lease.ID != fixture.initialLease.ID || current.Status != string(backupasset.LeaseActive) ||
			current.AttemptID == fixture.initialLease.AttemptID || current.FenceToken == fixture.initialLease.FenceToken ||
			!claim.lease.AbsoluteDeadline.Equal(fixture.storedDeadline) || !current.AbsoluteDeadline.Equal(fixture.storedDeadline) {
			t.Fatalf("takeover failed to rotate the fence while retaining deadline: before=%+v after=%+v claim=%+v", fixture.initialLease, current, claim.lease)
		}
		fixture.requireLineage(t, backupasset.RecoveryPointPreparing)
	})

	t.Run("point_expiry", func(t *testing.T) {
		fixture := newPublicationDeadlinePrecisionFixture(t, engine)
		fixture.clock = fixture.rawDeadline.Add(-time.Nanosecond)
		if _, expired, err := fixture.publication.service.expireAtDeadline(ctx, fixture.attempt.RecoveryPointID); err != nil || expired {
			t.Fatalf("point expired before its signed deadline: expired=%v err=%v", expired, err)
		}
		fixture.clock = fixture.rawDeadline
		outcome, expired, err := fixture.publication.service.expireAtDeadline(ctx, fixture.attempt.RecoveryPointID)
		if err != nil || !expired || outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailurePublicationDeadlineExceeded {
			t.Fatalf("point deadline outcome=%+v expired=%v err=%v", outcome, expired, err)
		}
		fixture.requireOriginalLease(t)
		fixture.requireLineage(t, backupasset.RecoveryPointFailed)
	})

	t.Run("deadline_drift", func(t *testing.T) {
		fixture := newPublicationDeadlinePrecisionFixture(t, engine)
		drift := time.Nanosecond
		if engine == "postgres" {
			drift = time.Microsecond
		}
		changedDeadline := fixture.storedDeadline.Add(drift)
		if err := fixture.publication.db.Model(&model.RecoveryPointLease{}).Where("id = ?", fixture.initialLease.ID).
			UpdateColumn("absolute_deadline", changedDeadline).Error; err != nil {
			t.Fatalf("introduce persisted deadline drift: %v", err)
		}
		if actual := fixture.loadLease(t, fixture.initialLease.ID); !actual.AbsoluteDeadline.Equal(changedDeadline) {
			t.Fatalf("deadline drift did not persist: got %v want %v", actual.AbsoluteDeadline, changedDeadline)
		}
		if _, err := fixture.publication.service.claimPreparingPoint(ctx, fixture.attempt.RecoveryPointID); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("live lease deadline drift error=%v, want conflict", err)
		}
		if err := fixture.publication.service.lease.Release(ctx, fixture.attempt.Fence); err != nil {
			t.Fatalf("release drifted lease: %v", err)
		}
		if _, err := fixture.publication.service.claimPreparingPoint(ctx, fixture.attempt.RecoveryPointID); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("fresh stage deadline drift error=%v, want conflict", err)
		}
		fixture.clock = fixture.rawDeadline
		if _, expired, err := fixture.publication.service.expireAtDeadline(ctx, fixture.attempt.RecoveryPointID); !errors.Is(err, backupasset.ErrConflict) || expired {
			t.Fatalf("expiry deadline drift expired=%v err=%v, want conflict", expired, err)
		}
		var count int64
		if err := fixture.publication.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ?", fixture.attempt.RecoveryPointID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("deadline drift changed lease history: count=%d err=%v", count, err)
		}
		fixture.requireLineage(t, backupasset.RecoveryPointPreparing)
	})

	t.Run("submicro_expiry_window", func(t *testing.T) {
		exercisePublicationDeadlineWindow(t, engine, false)
		exercisePublicationDeadlineWindow(t, engine, true)
	})
}

type publicationDeadlinePrecisionFixture struct {
	publication    *publicationFixture
	execution      publication.Execution
	attempt        provider.ResticAttemptV1
	clock          time.Time
	rawDeadline    time.Time
	storedDeadline time.Time
	initialLease   model.RecoveryPointLease
	lineageJSON    string
}

func newPublicationDeadlinePrecisionFixture(t *testing.T, engine string) *publicationDeadlinePrecisionFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 11, 0, 0, 123456876, time.UTC)
	db := openProducerRetentionIntegrationDB(t, engine)
	publicationFixture := newPublicationFixtureWithDB(t, db, true, publication.AdmissionManaged, now)
	publicationFixture.connectExactResticBinding(t)
	fixture := &publicationDeadlinePrecisionFixture{publication: publicationFixture, clock: now}
	clock := func() time.Time { return fixture.clock }
	leaseConfig, err := publicationFixture.service.foundation.LeaseConfig()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(db, clock, leaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	publicationFixture.service.now = clock
	publicationFixture.service.lease = lease
	fixture.execution, err = publicationFixture.service.Prepare(ctx, publicationFixture.run())
	if err != nil {
		t.Fatalf("prepare publication: %v", err)
	}
	t.Cleanup(func() { _ = fixture.execution.Abandon(backupasset.ErrPublicationSessionAbandoned) })
	if err := fixture.execution.Cancel(nil); err != nil {
		t.Fatalf("stop publication heartbeat: %v", err)
	}
	fixture.attempt = resticAttemptForExecution(t, fixture.execution)
	fixture.rawDeadline = time.Date(2026, 7, 21, 11, 0, 0, 123456876, time.UTC)
	if !fixture.attempt.PointDeadlineAt.Equal(fixture.rawDeadline) {
		t.Fatalf("signed attempt deadline=%v want %v", fixture.attempt.PointDeadlineAt, fixture.rawDeadline)
	}
	fixture.storedDeadline = fixture.rawDeadline
	if engine == "postgres" {
		fixture.storedDeadline = time.Date(2026, 7, 21, 11, 0, 0, 123456000, time.UTC)
	}
	fixture.initialLease = fixture.loadLease(t, fixture.attempt.Fence.LeaseID)
	if fixture.initialLease.Status != string(backupasset.LeaseActive) || !fixture.initialLease.AbsoluteDeadline.Equal(fixture.storedDeadline) {
		t.Fatalf("initial persisted lease=%+v want active deadline %v", fixture.initialLease, fixture.storedDeadline)
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", fixture.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || !lineage.PointDeadlineAt.Equal(fixture.rawDeadline) {
		t.Fatalf("stored lineage deadline=%v want %v err=%v", lineage.PointDeadlineAt, fixture.rawDeadline, err)
	}
	fixture.lineageJSON = point.LineageJSON
	return fixture
}

func (fixture *publicationDeadlinePrecisionFixture) loadLease(t *testing.T, leaseID string) model.RecoveryPointLease {
	t.Helper()
	var lease model.RecoveryPointLease
	if err := fixture.publication.db.First(&lease, "id = ?", leaseID).Error; err != nil {
		t.Fatalf("reload lease %s: %v", leaseID, err)
	}
	return lease
}

func (fixture *publicationDeadlinePrecisionFixture) requireOriginalLease(t *testing.T) {
	t.Helper()
	current := fixture.loadLease(t, fixture.initialLease.ID)
	if current.Status != fixture.initialLease.Status || current.AttemptID != fixture.initialLease.AttemptID ||
		current.FenceToken != fixture.initialLease.FenceToken || !current.LeaseExpiresAt.Equal(fixture.initialLease.LeaseExpiresAt) ||
		!current.AbsoluteDeadline.Equal(fixture.storedDeadline) {
		t.Fatalf("original lease changed: before=%+v after=%+v", fixture.initialLease, current)
	}
}

func (fixture *publicationDeadlinePrecisionFixture) requireLineage(t *testing.T, state backupasset.RecoveryPointState) {
	t.Helper()
	var point model.RecoveryPoint
	if err := fixture.publication.db.First(&point, "id = ?", fixture.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(state) || point.LineageJSON != fixture.lineageJSON {
		t.Fatalf("point state or signed lineage changed: state=%q lineage=%q", point.State, point.LineageJSON)
	}
}

func exercisePublicationDeadlineWindow(t *testing.T, engine string, fresh bool) {
	t.Helper()
	ctx := context.Background()
	fixture := newPublicationDeadlinePrecisionFixture(t, engine)
	if fresh {
		if err := fixture.publication.service.lease.Release(ctx, fixture.attempt.Fence); err != nil {
			t.Fatalf("release original lease for fresh stage: %v", err)
		}
	}
	fixture.clock = fixture.rawDeadline.Add(-time.Nanosecond)
	if engine == "postgres" {
		fixture.clock = fixture.storedDeadline.Add(time.Nanosecond)
		if !fixture.clock.Before(fixture.rawDeadline) {
			t.Fatal("PostgreSQL test clock is not inside the submicrosecond window")
		}
	}
	if _, expired, err := fixture.publication.service.expireAtDeadline(ctx, fixture.attempt.RecoveryPointID); err != nil || expired {
		t.Fatalf("point expired before signed deadline: fresh=%v expired=%v err=%v", fresh, expired, err)
	}
	claim, err := fixture.publication.service.claimPreparingPoint(ctx, fixture.attempt.RecoveryPointID)
	if engine == "postgres" {
		if !errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
			t.Fatalf("expired PostgreSQL lease was extended: fresh=%v claim=%+v err=%v", fresh, claim.lease, err)
		}
		current := fixture.loadLease(t, fixture.initialLease.ID)
		if !current.AbsoluteDeadline.Equal(fixture.storedDeadline) || current.AttemptID != fixture.initialLease.AttemptID || current.FenceToken != fixture.initialLease.FenceToken {
			t.Fatalf("expired PostgreSQL lease changed: before=%+v after=%+v", fixture.initialLease, current)
		}
	} else {
		if err != nil || !claim.lease.AbsoluteDeadline.Equal(fixture.storedDeadline) || !claim.lease.LeaseExpiresAt.Equal(fixture.storedDeadline) {
			t.Fatalf("SQLite lease before its deadline: fresh=%v claim=%+v err=%v", fresh, claim.lease, err)
		}
		current := fixture.loadLease(t, claim.lease.ID)
		if !current.AbsoluteDeadline.Equal(fixture.storedDeadline) || !current.LeaseExpiresAt.Equal(fixture.storedDeadline) {
			t.Fatalf("SQLite lease extended beyond deadline: %+v", current)
		}
	}
	fixture.requireLineage(t, backupasset.RecoveryPointPreparing)
	fixture.clock = fixture.rawDeadline
	outcome, expired, err := fixture.publication.service.expireAtDeadline(ctx, fixture.attempt.RecoveryPointID)
	if err != nil || !expired || outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailurePublicationDeadlineExceeded {
		t.Fatalf("point did not expire at signed deadline: fresh=%v outcome=%+v expired=%v err=%v", fresh, outcome, expired, err)
	}
	fixture.requireLineage(t, backupasset.RecoveryPointFailed)
}
