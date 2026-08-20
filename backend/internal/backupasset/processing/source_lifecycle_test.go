package processing

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestLifecycleLateOutputRejectsProcessingDerivedManifestAfterSourceRevoke(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("late manifest payload")
	declaration := artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatalf("stage manifest artifact: %v", err)
	}
	attemptID := strings.Repeat("f", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: harness.lease.RecoveryPointFence.RecoveryPointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	owner, err := NewSourceLifecycle(harness.db, harness.lifecycle, &processingSearchProofSpy{}, harness.clock.Now, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: harness.lease.RecoveryPointFence.RecoveryPointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("prepare Processing lifecycle: %v", err)
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil || result != (CommitManifestResult{}) {
		t.Fatalf("late manifest result=%+v err=%v, want fenced rejection", result, err)
	}
	var sets int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("recovery_point_id = ?", request.RecoveryPointID).Count(&sets).Error; err != nil || sets != 0 {
		t.Fatalf("late manifest set count=%d err=%v, want zero", sets, err)
	}
}

func TestRecoveryPointSourceLifecycleProcessingRevokesPublicationBeforeInflightDrain(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("late manifest after lifecycle claim")
	declaration := artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatalf("stage manifest artifact: %v", err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second, MaxRequests: 8, MaxBytesPerRequest: 1 << 20, MaxCumulativeBytes: 4 << 20, MaxInFlight: 2,
	})
	if err != nil {
		t.Fatalf("NewGrantService: %v", err)
	}
	reservation, err := grants.Reserve(context.Background(), ReserveGrantRequest{
		GrantID: harness.sinkGrantID, Kind: GrantRequestUpload, Bytes: 1,
	})
	if err != nil {
		t.Fatalf("reserve in-flight Sink request: %v", err)
	}

	lifecycleAttemptID := strings.Repeat("7", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: lifecycleAttemptID, RecoveryPointID: harness.lease.RecoveryPointFence.RecoveryPointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	owner, err := NewSourceLifecycle(harness.db, harness.lifecycle, &processingSearchProofSpy{}, harness.clock.Now, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: harness.lease.RecoveryPointFence.RecoveryPointID, LifecycleAttemptID: lifecycleAttemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("in-flight Processing lifecycle error=%v, want ErrConflict", err)
	}

	var claimedGrant model.BackupAssetProcessingGrant
	var retainedLease model.RecoveryPointLease
	if err := harness.db.First(&claimedGrant, "id = ?", harness.sinkGrantID).Error; err != nil {
		t.Fatalf("load claimed Sink grant: %v", err)
	}
	if err := harness.db.First(&retainedLease, "id = ?", harness.lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatalf("load retained Processing lease: %v", err)
	}
	if claimedGrant.State != string(GrantRevoked) || claimedGrant.ActivationSecretHash != "" || claimedGrant.InFlight != 1 {
		t.Fatalf("lifecycle claim left manifest authority usable state=%q secret_cleared=%t in_flight=%d",
			claimedGrant.State, claimedGrant.ActivationSecretHash == "", claimedGrant.InFlight)
	}
	var usableGrants int64
	if err := harness.db.Model(&model.BackupAssetProcessingGrant{}).
		Where("job_id = ? AND state IN ?", harness.lease.JobID, []string{string(GrantIssued), string(GrantActive)}).
		Count(&usableGrants).Error; err != nil || usableGrants != 0 {
		t.Fatalf("lifecycle claim left %d issued/active grants err=%v", usableGrants, err)
	}
	if retainedLease.Status != string(backupasset.LeaseActive) {
		t.Fatalf("lifecycle claim released lease before in-flight drain status=%q", retainedLease.Status)
	}

	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil || result != (CommitManifestResult{}) {
		t.Fatalf("late manifest after in-flight claim result=%+v err=%v, want fenced rejection", result, err)
	}
	var sets int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).
		Where("recovery_point_id = ?", request.RecoveryPointID).Count(&sets).Error; err != nil || sets != 0 {
		t.Fatalf("late manifest set count=%d err=%v, want zero", sets, err)
	}

	if err := grants.Finalize(context.Background(), FinalizeGrantRequest{
		ReservationID: reservation.ReservationID, Outcome: GrantRequestCanceled,
		EvidenceKnown: true, FailureCode: GrantFailureClientCanceled,
	}); err != nil {
		t.Fatalf("settle in-flight Sink request: %v", err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("retry Processing lifecycle after drain: %v", err)
	}
	var settledJob model.BackupAssetProcessingJob
	var settledAttempt model.BackupAssetProcessingAttempt
	var settledGrant model.BackupAssetProcessingGrant
	var settledLease model.RecoveryPointLease
	for label, query := range map[string]*gorm.DB{
		"job":     harness.db.First(&settledJob, "id = ?", harness.lease.JobID),
		"attempt": harness.db.First(&settledAttempt, "id = ?", harness.lease.AttemptID),
		"grant":   harness.db.First(&settledGrant, "id = ?", harness.sinkGrantID),
		"lease":   harness.db.First(&settledLease, "id = ?", harness.lease.RecoveryPointFence.LeaseID),
	} {
		if query.Error != nil {
			t.Fatalf("load settled Processing %s: %v", label, query.Error)
		}
	}
	if settledJob.State != string(ProcessingCanceled) || settledJob.IsCurrent ||
		settledAttempt.State != "canceled" || settledAttempt.IsCurrent ||
		settledGrant.State != string(GrantRevoked) || settledGrant.InFlight != 0 ||
		settledLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("Processing authority remained after drain job_state=%q job_current=%t attempt_state=%q attempt_current=%t grant_state=%q grant_in_flight=%d lease_status=%q",
			settledJob.State, settledJob.IsCurrent, settledAttempt.State, settledAttempt.IsCurrent,
			settledGrant.State, settledGrant.InFlight, settledLease.Status)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent Processing lifecycle after drain: %v", err)
	}
}

func TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	if err := harness.db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{},
	); err != nil {
		t.Fatalf("migrate source lifecycle tables: %v", err)
	}
	payload := []byte("derived ciphertext retained until Search proof")
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("seed Derived blob: %v", err)
	}
	reference := harness.seedReference(t, blob.BlobID, "4", "5")
	pointID := reference.authorization.RecoveryPointID
	now := harness.store.utcNow()
	attemptID, leaseID := strings.Repeat("6", 32), strings.Repeat("7", 32)
	if err := harness.db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("8", 32)}).Error; err != nil {
		t.Fatalf("seed point: %v", err)
	}
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleExplicitPurge), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	jobID, processingAttemptID, grantID := strings.Repeat("4", 31)+"a", strings.Repeat("9", 32), strings.Repeat("a", 32)
	job := model.BackupAssetProcessingJob{ID: jobID, WorkKey: strings.Repeat("b", 64), DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID, State: string(ProcessingProcessing), TransitionRevision: 3, IsCurrent: true, CurrentAttemptID: &processingAttemptID, QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour)}
	if err := harness.db.Create(&job).Error; err != nil {
		t.Fatalf("seed Processing job: %v", err)
	}
	interest := model.BackupAssetProcessingInterest{ID: strings.Repeat("c", 32), JobID: jobID, OwnerKind: string(InterestSearch), OwnerKey: "search-source", PriorityClass: string(PriorityBackground), Active: true}
	if err := harness.db.Create(&interest).Error; err != nil {
		t.Fatalf("seed Processing interest: %v", err)
	}
	processingAttempt := model.BackupAssetProcessingAttempt{ID: processingAttemptID, JobID: jobID, State: "active", IsCurrent: true, RecoveryPointLeaseID: leaseID, WorkerLeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(time.Hour)}
	if err := harness.db.Create(&processingAttempt).Error; err != nil {
		t.Fatalf("seed Processing attempt: %v", err)
	}
	grant := model.BackupAssetProcessingGrant{ID: grantID, JobID: jobID, AttemptID: processingAttemptID, State: "active", InFlight: 0}
	if err := harness.db.Create(&grant).Error; err != nil {
		t.Fatalf("seed Processing grant: %v", err)
	}
	lease := model.RecoveryPointLease{ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderProcessingJob), OwnerID: jobID, AttemptID: strings.Repeat("d", 32), FenceToken: strings.Repeat("e", 64), Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now}
	if err := harness.db.Create(&lease).Error; err != nil {
		t.Fatalf("seed Processing lease: %v", err)
	}

	proof := &processingSearchProofSpy{beforeProof: func() error {
		var stored model.BackupAssetDerivedBlob
		if err := harness.db.First(&stored, "id = ?", blob.BlobID).Error; err != nil {
			return err
		}
		if stored.State != "active" || len(stored.WrappedDEK) == 0 {
			return errors.New("Processing destroyed Derived blob before Search proof")
		}
		if _, err := os.Stat(filepath.Join(harness.root, blob.OpaqueLocator)); err != nil {
			return err
		}
		return nil
	}}
	owner, err := NewSourceLifecycle(harness.db, harness.lifecycle, proof, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{RecoveryPointID: pointID, LifecycleAttemptID: attemptID, Operation: backupasset.LifecycleExplicitPurge, Stage: backupasset.SourceLifecyclePrepare}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("prepare Processing lifecycle: %v", err)
	}
	assertProcessingSourcePrepared(t, harness, jobID, processingAttemptID, grantID, leaseID)
	assertDerivedPayload(t, harness, reference, blob.BlobID, true)
	if proof.calls != 0 {
		t.Fatalf("Search proof called during prepare: %d", proof.calls)
	}

	if err := harness.db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle: %v", err)
	}
	request.Stage = backupasset.SourceLifecycleCleanup
	privatePath := filepath.Join(harness.root, blob.OpaqueLocator)
	const privateCanary = "source-lifecycle-derived-delete-canary"
	privateRootCause := errors.New(privateCanary)
	privatePathError := &os.PathError{Op: "remove", Path: privatePath, Err: privateRootCause}
	removeCalls := 0
	removeFile := harness.store.removeFile
	harness.store.removeFile = func(path string) error {
		removeCalls++
		if removeCalls == 1 {
			return privatePathError
		}
		return removeFile(path)
	}
	cleanupErr := owner.RevokeRecoveryPoint(context.Background(), request)
	assertClosedDerivedBlobUnavailableError(t, cleanupErr, privatePathError, privateRootCause, []string{
		privateCanary, harness.root, blob.OpaqueLocator, privatePath,
	})
	if proof.calls != 1 {
		t.Fatalf("Search cleanup proof calls=%d, want 1 before destruction", proof.calls)
	}
	var failedBlob model.BackupAssetDerivedBlob
	if err := harness.db.First(&failedBlob, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if failedBlob.State != "purge_failed" || len(failedBlob.WrappedDEK) != 0 {
		t.Fatalf("failed Derived purge retained authority state=%q key_erased=%t ref_count=%d",
			failedBlob.State, len(failedBlob.WrappedDEK) == 0, failedBlob.RefCount)
	}
	if _, err := os.Stat(filepath.Join(harness.root, blob.OpaqueLocator)); err != nil {
		t.Fatalf("failed Derived purge lost retryable ciphertext stat_error_present=%t", err != nil)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("retry Processing cleanup: %v", err)
	}
	if proof.calls != 2 || removeCalls != 2 {
		t.Fatalf("retry Processing cleanup proof/removal calls=%d/%d, want 2/2", proof.calls, removeCalls)
	}
	assertDerivedPayload(t, harness, reference, blob.BlobID, false)
	if _, err := os.Stat(filepath.Join(harness.root, blob.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Derived ciphertext removal proof failed stat_error_present=%t is_not_exist=%t",
			err != nil, errors.Is(err, os.ErrNotExist))
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent Processing cleanup: %v", err)
	}
}

func TestRecoveryPointSourceLifecycleProcessingDeduplicatesSharedBlobCleanup(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	if err := harness.db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{},
	); err != nil {
		t.Fatalf("migrate shared-blob source lifecycle tables: %v", err)
	}
	sharedPayload := []byte("same-point artifacts share one Derived blob")
	sharedBlob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(sharedPayload), bytes.NewReader(sharedPayload))
	if err != nil {
		t.Fatalf("seed shared Derived blob: %v", err)
	}
	target := harness.seedReference(t, sharedBlob.BlobID, "4", "5")
	now := harness.store.utcNow()
	secondArtifactID := strings.Repeat("6", 32)
	secondReferenceID := strings.Repeat("7", 32)
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.BackupAssetDerivedArtifact{
			ID: secondArtifactID, ArtifactSetID: target.setID, Ordinal: 1, Role: "content", MediaType: "text/plain",
			PlaintextSize: int64(len(sharedPayload)), PlaintextDigest: strings.Repeat("6", 64), Completeness: "complete",
			CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: sharedBlob.BlobID, CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.BackupAssetDerivedBlobReference{
			ID: secondReferenceID, BlobID: sharedBlob.BlobID, ArtifactID: secondArtifactID,
			RecoveryPointID:     target.authorization.RecoveryPointID,
			CatalogGenerationID: target.authorization.CatalogGenerationID,
			EntryID:             target.authorization.EntryID, SourceFingerprint: target.authorization.SourceFingerprint,
			State: "active", CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", target.setID).
			Updates(map[string]any{"artifact_count": 2, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", sharedBlob.BlobID).
			Update("ref_count", gorm.Expr("ref_count + 1")).Error
	}); err != nil {
		t.Fatalf("seed second same-point Derived reference: %v", err)
	}

	unrelatedPayload := []byte("unrelated Derived reference remains readable")
	unrelatedBlob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(unrelatedPayload), bytes.NewReader(unrelatedPayload))
	if err != nil {
		t.Fatalf("seed unrelated Derived blob: %v", err)
	}
	unrelated := harness.seedReference(t, unrelatedBlob.BlobID, "8", "9")
	pointID := target.authorization.RecoveryPointID
	attemptID := strings.Repeat("a", 32)
	if err := harness.db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("b", 32)}).Error; err != nil {
		t.Fatalf("seed shared-blob point: %v", err)
	}
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID,
		Operation: string(backupasset.LifecycleExplicitPurge), Phase: string(backupasset.LifecyclePhaseCleaning),
	}).Error; err != nil {
		t.Fatalf("seed shared-blob lifecycle attempt: %v", err)
	}

	sharedRemoveCalls := 0
	removeFile := harness.store.removeFile
	harness.store.removeFile = func(path string) error {
		if path == filepath.Join(harness.root, sharedBlob.OpaqueLocator) {
			sharedRemoveCalls++
		}
		return removeFile(path)
	}
	purgeFailedUpdates, unavailableUpdates := 0, 0
	callbackName := "test:processing-source-shared-blob-update-count"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "backup_asset_derived_blobs" {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok {
			return
		}
		state, ok := updates["state"].(string)
		if !ok {
			return
		}
		switch state {
		case "purge_failed":
			purgeFailedUpdates++
		case "unavailable":
			unavailableUpdates++
		}
	}); err != nil {
		t.Fatalf("register shared-blob update counter: %v", err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Update().Remove(callbackName) })

	proof := &processingSearchProofSpy{}
	owner, err := NewSourceLifecycle(harness.db, harness.lifecycle, proof, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle shared-blob owner: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleExplicitPurge, Stage: backupasset.SourceLifecycleCleanup,
	}
	if cleanupErr := owner.RevokeRecoveryPoint(context.Background(), request); cleanupErr != nil {
		t.Errorf("same-point shared-blob cleanup returned an error sentinel=%t", errors.Is(cleanupErr, ErrDerivedBlobUnavailable))
	}
	if sharedRemoveCalls != 1 || purgeFailedUpdates != 1 || unavailableUpdates != 1 {
		t.Errorf("same-point shared blob processed more than once remove_calls=%d purge_updates=%d unavailable_updates=%d",
			sharedRemoveCalls, purgeFailedUpdates, unavailableUpdates)
	}

	restarted, err := NewSourceLifecycle(harness.db, harness.lifecycle, proof, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatalf("restart shared-blob SourceLifecycle: %v", err)
	}
	if err := restarted.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("restart shared-blob cleanup error=%v", err)
	}
	if err := restarted.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent shared-blob cleanup error=%v", err)
	}
	if sharedRemoveCalls != 1 || purgeFailedUpdates != 1 || unavailableUpdates != 1 {
		t.Errorf("restart repeated shared-blob destruction remove_calls=%d purge_updates=%d unavailable_updates=%d",
			sharedRemoveCalls, purgeFailedUpdates, unavailableUpdates)
	}

	var targetReferenceCount, unavailableTargetReferenceCount int64
	if err := harness.db.Model(&model.BackupAssetDerivedBlobReference{}).
		Where("recovery_point_id = ?", pointID).Count(&targetReferenceCount).Error; err != nil {
		t.Fatalf("count target Derived references: %v", err)
	}
	if err := harness.db.Model(&model.BackupAssetDerivedBlobReference{}).
		Where("recovery_point_id = ? AND state = ?", pointID, "unavailable").Count(&unavailableTargetReferenceCount).Error; err != nil {
		t.Fatalf("count unavailable target Derived references: %v", err)
	}
	var targetSet model.BackupAssetDerivedArtifactSet
	var persistedSharedBlob model.BackupAssetDerivedBlob
	if err := harness.db.First(&targetSet, "id = ?", target.setID).Error; err != nil {
		t.Fatalf("load target Derived set: %v", err)
	}
	if err := harness.db.First(&persistedSharedBlob, "id = ?", sharedBlob.BlobID).Error; err != nil {
		t.Fatalf("load shared Derived blob: %v", err)
	}
	if targetReferenceCount != 2 || unavailableTargetReferenceCount != 2 || targetSet.State != "unavailable" ||
		persistedSharedBlob.State != "unavailable" || persistedSharedBlob.RefCount != 0 || len(persistedSharedBlob.WrappedDEK) != 0 {
		t.Fatalf("shared-blob cleanup product invalid references=%d unavailable_references=%d set_state=%q blob_state=%q ref_count=%d key_erased=%t",
			targetReferenceCount, unavailableTargetReferenceCount, targetSet.State, persistedSharedBlob.State,
			persistedSharedBlob.RefCount, len(persistedSharedBlob.WrappedDEK) == 0)
	}
	if _, err := os.Stat(filepath.Join(harness.root, sharedBlob.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared Derived ciphertext removal proof failed error_present=%t is_not_exist=%t",
			err != nil, errors.Is(err, os.ErrNotExist))
	}
	assertDerivedPayload(t, harness, unrelated, unrelatedBlob.BlobID, true)
	if _, err := os.Stat(filepath.Join(harness.root, unrelatedBlob.OpaqueLocator)); err != nil {
		t.Fatalf("unrelated Derived ciphertext changed error_present=%t", err != nil)
	}
	if proof.calls != 3 {
		t.Fatalf("shared-blob cleanup Search proof calls=%d, want 3", proof.calls)
	}
}

func TestRecoveryPointSourceLifecycleProcessingLocksSharedBlobBeforeReferenceProof(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	if err := harness.db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{},
	); err != nil {
		t.Fatalf("migrate shared-reference source lifecycle tables: %v", err)
	}
	payload := []byte("shared blob gains another point reference while cleanup waits")
	sharedBlob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("seed shared-reference Derived blob: %v", err)
	}
	target := harness.seedReference(t, sharedBlob.BlobID, "1", "2")
	now := harness.store.utcNow()

	otherPointID := strings.Repeat("3", 32)
	otherSetID := strings.Repeat("4", 32)
	otherArtifactID := strings.Repeat("5", 32)
	otherReferenceID := strings.Repeat("6", 32)
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: otherSetID, RecoveryPointID: otherPointID, State: "active", ArtifactCount: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed concurrent Derived set: %v", err)
	}
	if err := harness.db.Create(&model.BackupAssetDerivedArtifact{
		ID: otherArtifactID, ArtifactSetID: otherSetID, Ordinal: 0, Role: "content", MediaType: "text/plain",
		PlaintextSize: int64(len(payload)), PlaintextDigest: strings.Repeat("7", 64), Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: sharedBlob.BlobID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed concurrent Derived artifact: %v", err)
	}

	pointID := target.authorization.RecoveryPointID
	attemptID := strings.Repeat("8", 32)
	if err := harness.db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("9", 32)}).Error; err != nil {
		t.Fatalf("seed shared-reference point: %v", err)
	}
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID,
		Operation: string(backupasset.LifecycleExplicitPurge), Phase: string(backupasset.LifecyclePhaseCleaning),
	}).Error; err != nil {
		t.Fatalf("seed shared-reference lifecycle attempt: %v", err)
	}

	injected := false
	const callbackName = "test:processing-source-reference-after-count"
	if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement == nil || tx.Statement.Table != (model.BackupAssetDerivedBlob{}).TableName() {
			return
		}
		injected = true
		if err := tx.Exec(`INSERT INTO backup_asset_derived_blob_references
			(id, blob_id, artifact_id, recovery_point_id, catalog_generation_id, entry_id,
			 source_fingerprint, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
			otherReferenceID, sharedBlob.BlobID, otherArtifactID, otherPointID,
			strings.Repeat("a", 32), strings.Repeat("b", 64), "concurrent-source", now, now).Error; err != nil {
			_ = tx.AddError(err)
			return
		}
		if err := tx.Exec(`UPDATE backup_asset_derived_blobs SET ref_count = ref_count + 1 WHERE id = ?`, sharedBlob.BlobID).Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatalf("register shared-reference callback: %v", err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })

	proof := &processingSearchProofSpy{}
	owner, err := NewSourceLifecycle(harness.db, harness.lifecycle, proof, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle shared-reference owner: %v", err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleExplicitPurge, Stage: backupasset.SourceLifecycleCleanup,
	}); err != nil {
		t.Fatalf("shared-reference cleanup: %v", err)
	}
	if !injected {
		t.Fatal("shared-reference callback did not reach the blob lock boundary")
	}

	var targetReference model.BackupAssetDerivedBlobReference
	var otherReference model.BackupAssetDerivedBlobReference
	var persistedBlob model.BackupAssetDerivedBlob
	if err := harness.db.First(&targetReference, "artifact_id = ?", target.authorization.ArtifactID).Error; err != nil {
		t.Fatalf("load target Derived reference: %v", err)
	}
	if err := harness.db.First(&otherReference, "id = ?", otherReferenceID).Error; err != nil {
		t.Fatalf("load concurrent Derived reference: %v", err)
	}
	if err := harness.db.First(&persistedBlob, "id = ?", sharedBlob.BlobID).Error; err != nil {
		t.Fatalf("load shared-reference Derived blob: %v", err)
	}
	if targetReference.State != "unavailable" || otherReference.State != "active" ||
		persistedBlob.State != "active" || persistedBlob.RefCount != 1 || len(persistedBlob.WrappedDEK) == 0 {
		t.Fatalf("shared-reference cleanup violated active authority target_state=%q other_state=%q blob_state=%q ref_count=%d key_present=%t",
			targetReference.State, otherReference.State, persistedBlob.State, persistedBlob.RefCount, len(persistedBlob.WrappedDEK) != 0)
	}
	if _, err := os.Stat(filepath.Join(harness.root, sharedBlob.OpaqueLocator)); err != nil {
		t.Fatalf("shared-reference ciphertext not preserved error_present=%t", err != nil)
	}
}

func TestRecoveryPointSourceLifecycleProcessingPrivateStatDiagnosticsAreClosed(t *testing.T) {
	source, err := os.ReadFile("source_lifecycle_test.go")
	if err != nil {
		t.Fatalf("read Processing SourceLifecycle test source: %v", err)
	}
	checked, unsafeLines, err := processingPrivateStatDiagnosticAudit(source)
	if err != nil {
		t.Fatalf("audit Processing private stat diagnostics: %v", err)
	}
	if checked != 2 {
		t.Fatalf("Processing private stat diagnostic guard coverage=%d, want 2", checked)
	}
	if len(unsafeLines) != 0 {
		t.Fatalf("Processing private stat diagnostics expose whole errors at lines=%v", unsafeLines)
	}

	t.Run("guard detects a raw-error mutation", func(t *testing.T) {
		mutant := []byte(`package processing

func TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof(t *testing.T) {
	if _, err := os.Stat(privatePath); err != nil {
		t.Fatalf("private stat failed: %v", err)
	}
	if _, err := os.Stat(privatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private stat proof failed error_present=%t is_not_exist=%t", err != nil, errors.Is(err, os.ErrNotExist))
	}
}`)
		mutantChecked, mutantUnsafeLines, auditErr := processingPrivateStatDiagnosticAudit(mutant)
		if auditErr != nil {
			t.Fatalf("audit mutated Processing diagnostic: %v", auditErr)
		}
		if mutantChecked != 2 || len(mutantUnsafeLines) != 1 {
			t.Fatalf("Processing private stat diagnostic guard missed raw-error mutation checked=%d unsafe=%d",
				mutantChecked, len(mutantUnsafeLines))
		}
	})
}

func processingPrivateStatDiagnosticAudit(source []byte) (int, []int, error) {
	files := token.NewFileSet()
	syntax, err := parser.ParseFile(files, "source_lifecycle_test.go", source, 0)
	if err != nil {
		return 0, nil, err
	}
	checked := 0
	unsafeLines := make([]int, 0, 2)
	for _, declaration := range syntax.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			statement, ok := node.(*ast.IfStmt)
			if !ok || !processingStatAssignment(statement.Init) {
				return true
			}
			ast.Inspect(statement.Body, func(bodyNode ast.Node) bool {
				call, ok := bodyNode.(*ast.CallExpr)
				if !ok || !processingFatalfCall(call) {
					return true
				}
				checked++
				if !processingStatDiagnosticArgsClosed(call.Args[1:]) {
					unsafeLines = append(unsafeLines, files.Position(call.Pos()).Line)
				}
				return false
			})
			return true
		})
	}
	return checked, unsafeLines, nil
}

func processingStatAssignment(statement ast.Stmt) bool {
	assignment, ok := statement.(*ast.AssignStmt)
	if !ok {
		return false
	}
	for _, expression := range assignment.Rhs {
		call, ok := expression.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		packageName, packageOK := selector.X.(*ast.Ident)
		if packageOK && packageName.Name == "os" && selector.Sel.Name == "Stat" {
			return true
		}
	}
	return false
}

func processingFatalfCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	receiver, receiverOK := selector.X.(*ast.Ident)
	return receiverOK && receiver.Name == "t" && selector.Sel.Name == "Fatalf" && len(call.Args) >= 1
}

func processingStatDiagnosticArgsClosed(arguments []ast.Expr) bool {
	if len(arguments) == 0 {
		return false
	}
	for _, argument := range arguments {
		if processingErrorPresenceComparison(argument) || processingErrorsIsCall(argument) {
			continue
		}
		return false
	}
	return true
}

func processingErrorPresenceComparison(expression ast.Expr) bool {
	comparison, ok := expression.(*ast.BinaryExpr)
	if !ok || comparison.Op != token.NEQ {
		return false
	}
	left, leftOK := comparison.X.(*ast.Ident)
	right, rightOK := comparison.Y.(*ast.Ident)
	return leftOK && rightOK && left.Name == "err" && right.Name == "nil"
}

func processingErrorsIsCall(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, packageOK := selector.X.(*ast.Ident)
	return packageOK && packageName.Name == "errors" && selector.Sel.Name == "Is"
}

func TestRecoveryPointSourceLifecycleProcessingSettlesNonCurrentLiveAuthority(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	runProcessingSourceLifecycleNonCurrentAuthority(t, harness.db, harness.lifecycle, harness.store.utcNow)
}

func TestRecoveryPointSourceLifecycleProcessingSettlesNonCurrentLiveAuthorityPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_PROCESSING_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	fixture := openProcessingBehaviorPostgres(t, dsn)
	derived := &DerivedLifecycle{db: fixture.db, store: &DerivedStore{db: fixture.db}}
	runProcessingSourceLifecycleNonCurrentAuthority(t, fixture.db, derived, fixture.clock.Now)
}

func runProcessingSourceLifecycleNonCurrentAuthority(t *testing.T, db *gorm.DB, derived *DerivedLifecycle, nowFn func() time.Time) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{},
	); err != nil {
		t.Fatal(err)
	}
	now := nowFn().UTC()
	pointID, otherPointID, lifecycleAttemptID := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	point := func(id string) model.RecoveryPoint {
		return model.RecoveryPoint{
			ID: id, RepositoryID: strings.Repeat("e", 32), Semantics: string(backupasset.PointXirangManifest),
			State: string(backupasset.RecoveryPointCommitted), CommittedAt: &now, LineageJSON: `{}`,
			ConsistencyJSON: `{}`, FidelityJSON: `{}`, CapabilitiesJSON: `{}`,
			ImmutabilityLevel:    string(backupasset.ImmutabilityXirangManaged),
			PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
			CreatedAt: now, UpdatedAt: now,
		}
	}
	for _, point := range []model.RecoveryPoint{point(pointID), point(otherPointID)} {
		if err := db.Create(&point).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: lifecycleAttemptID, RecoveryPointID: pointID,
		Operation: string(backupasset.LifecycleExplicitPurge), Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatal(err)
	}

	type jobFixture struct {
		pointID    string
		generation string
		entryID    string
		jobID      string
		attemptID  string
		interest   string
		grantID    string
		leaseID    string
		seed       string
	}
	fixtures := []jobFixture{
		{pointID: pointID, generation: strings.Repeat("4", 32), entryID: strings.Repeat("3", 64), jobID: strings.Repeat("5", 32), attemptID: strings.Repeat("6", 32), interest: strings.Repeat("7", 32), grantID: strings.Repeat("8", 32), leaseID: strings.Repeat("9", 32), seed: "a"},
		{pointID: otherPointID, generation: strings.Repeat("0", 32), entryID: strings.Repeat("4", 64), jobID: strings.Repeat("b", 32), attemptID: strings.Repeat("c", 32), interest: strings.Repeat("d", 32), grantID: strings.Repeat("e", 32), leaseID: strings.Repeat("f", 32), seed: "b"},
	}
	for _, fixture := range fixtures {
		finishedAt := now
		if err := db.Create(&model.CatalogGeneration{
			ID: fixture.generation, RecoveryPointID: fixture.pointID, Generation: 1, State: "complete",
			SourceFingerprint: "source-" + fixture.seed, ExpectedEntryCount: 1, WrittenEntryCount: 1,
			StartedAt: now, FinishedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.CatalogEntry{
			GenerationID: fixture.generation, EntryID: fixture.entryID, RecoveryPointID: fixture.pointID,
			NormalizedPath: "/source-" + fixture.seed, Name: "source-" + fixture.seed, EntryType: "file",
			Fingerprint: "entry-" + fixture.seed, FingerprintStrength: "strong", SecurityState: "non_secret", CreatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		attemptID := fixture.attemptID
		if err := db.Create(&model.BackupAssetProcessingJob{
			ID: fixture.jobID, WorkKey: strings.Repeat(fixture.seed, 64), DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
			RecoveryPointID: fixture.pointID, CatalogGenerationID: fixture.generation, EntryID: fixture.entryID,
			SourceFingerprint: "source-" + fixture.seed, EntryFingerprint: "entry-" + fixture.seed,
			ProviderCapabilityRevision: 1, Capability: "noop", CapabilitySchema: "noop.v1",
			PipelineFingerprint: "pipeline-" + fixture.seed, OutputProfile: "noop.v1", SecurityPolicyRevision: "policy-v1",
			PriorityClass: string(PriorityBackground), EffectivePriority: 1,
			State: string(ProcessingProcessing), TransitionRevision: 3,
			IsCurrent: false, CurrentAttemptID: &attemptID, QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour),
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.BackupAssetProcessingJob{}).
			Where("id = ?", fixture.jobID).
			UpdateColumn("is_current", false).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.BackupAssetProcessingInterest{
			ID: fixture.interest, JobID: fixture.jobID, OwnerKind: string(InterestSearch), OwnerKey: "source-" + fixture.seed,
			PriorityClass: string(PriorityBackground), Active: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.RecoveryPointLease{
			ID: fixture.leaseID, RecoveryPointID: fixture.pointID, HolderType: string(backupasset.LeaseHolderProcessingJob), OwnerID: fixture.jobID,
			AttemptID: strings.Repeat(fixture.seed, 32), FenceToken: strings.Repeat(fixture.seed, 64), Status: string(backupasset.LeaseActive),
			LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.BackupAssetProcessingAttempt{
			ID: fixture.attemptID, JobID: fixture.jobID, AttemptNumber: 1, WorkerID: strings.Repeat("d", 32),
			SlotClass: string(PriorityBackground), State: "active", IsCurrent: true,
			RecoveryPointLeaseID: fixture.leaseID, RecoveryPointAttemptID: strings.Repeat(fixture.seed, 32),
			RecoveryPointFenceHash: strings.Repeat(fixture.seed, 64), WorkerLeaseExpiresAt: now.Add(time.Hour),
			LastHeartbeatAt: now, AbsoluteDeadline: now.Add(time.Hour), StartedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		inFlight := int64(0)
		if fixture.pointID == pointID {
			inFlight = 1
		}
		if err := db.Create(&model.BackupAssetProcessingGrant{
			ID: fixture.grantID, JobID: fixture.jobID, AttemptID: fixture.attemptID, WorkerID: strings.Repeat("d", 32),
			Kind: "input", FenceHash: strings.Repeat(fixture.seed, 64), State: "active", InFlight: inFlight,
			MaxRequests: 4, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
			ExpiresAt: now.Add(time.Hour), ActivatedAt: &now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	releasedLeaseID := strings.Repeat("0", 32)
	releasedAt := now.Add(-time.Minute)
	if err := db.Create(&model.RecoveryPointLease{
		ID: releasedLeaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderProcessingJob), OwnerID: fixtures[0].jobID,
		AttemptID: strings.Repeat("1", 32), FenceToken: strings.Repeat("2", 64), Status: string(backupasset.LeaseReleased),
		LeaseExpiresAt: now.Add(-time.Hour), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now.Add(-2 * time.Hour), ReleasedAt: &releasedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	proof := &processingSearchProofSpy{}
	owner, err := NewSourceLifecycle(db, derived, proof, func() time.Time { return now }, 1)
	if err != nil {
		t.Fatal(err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: lifecycleAttemptID,
		Operation: backupasset.LifecycleExplicitPurge, Stage: backupasset.SourceLifecyclePrepare,
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("in-flight non-current authority error=%v, want ErrConflict", err)
	}
	var exactInterest model.BackupAssetProcessingInterest
	var exactGrant model.BackupAssetProcessingGrant
	var exactLease model.RecoveryPointLease
	db.First(&exactInterest, "id = ?", fixtures[0].interest)
	db.First(&exactGrant, "id = ?", fixtures[0].grantID)
	db.First(&exactLease, "id = ?", fixtures[0].leaseID)
	if !exactInterest.Active || exactGrant.State != string(GrantRevoked) || exactGrant.ActivationSecretHash != "" ||
		exactGrant.InFlight != 1 || exactLease.Status != string(backupasset.LeaseActive) {
		t.Fatalf("in-flight claim did not revoke publication authority while retaining drain lease interest_active=%t grant_state=%q grant_secret_cleared=%t grant_in_flight=%d lease_status=%q",
			exactInterest.Active, exactGrant.State, exactGrant.ActivationSecretHash == "", exactGrant.InFlight, exactLease.Status)
	}
	if err := db.Model(&model.BackupAssetProcessingGrant{}).Where("id = ?", fixtures[0].grantID).Update("in_flight", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("settle non-current Processing authority: %v", err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent non-current Processing settlement: %v", err)
	}

	var activeInterests, currentAttempts, usableGrants, inFlightGrants, liveLeases int64
	for queryName, query := range map[string]*gorm.DB{
		"interests": db.Table("backup_asset_processing_interests AS interests").Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = interests.job_id").Where("jobs.recovery_point_id = ? AND interests.active = ?", pointID, true),
		"attempts":  db.Table("backup_asset_processing_attempts AS attempts").Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = attempts.job_id").Where("jobs.recovery_point_id = ? AND attempts.state = ? AND attempts.is_current = ?", pointID, "active", true),
		"grants":    db.Table("backup_asset_processing_grants AS grants").Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = grants.job_id").Where("jobs.recovery_point_id = ? AND grants.state IN ?", pointID, []string{"issued", "active"}),
		"in_flight": db.Table("backup_asset_processing_grants AS grants").Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = grants.job_id").Where("jobs.recovery_point_id = ? AND grants.in_flight <> 0", pointID),
		"leases":    db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND holder_type = ? AND status = ?", pointID, backupasset.LeaseHolderProcessingJob, backupasset.LeaseActive),
	} {
		var destination *int64
		switch queryName {
		case "interests":
			destination = &activeInterests
		case "attempts":
			destination = &currentAttempts
		case "grants":
			destination = &usableGrants
		case "in_flight":
			destination = &inFlightGrants
		case "leases":
			destination = &liveLeases
		}
		if err := query.Count(destination).Error; err != nil {
			t.Fatalf("count %s: %v", queryName, err)
		}
	}
	if activeInterests != 0 || currentAttempts != 0 || usableGrants != 0 || inFlightGrants != 0 || liveLeases != 0 {
		t.Fatalf("non-current authority remains interests=%d attempts=%d grants=%d in_flight=%d leases=%d", activeInterests, currentAttempts, usableGrants, inFlightGrants, liveLeases)
	}
	var exactJob, otherJob model.BackupAssetProcessingJob
	var otherInterest model.BackupAssetProcessingInterest
	var otherGrant model.BackupAssetProcessingGrant
	var otherLease model.RecoveryPointLease
	db.First(&exactJob, "id = ?", fixtures[0].jobID)
	db.First(&otherJob, "id = ?", fixtures[1].jobID)
	db.First(&otherInterest, "id = ?", fixtures[1].interest)
	db.First(&otherGrant, "id = ?", fixtures[1].grantID)
	db.First(&otherLease, "id = ?", fixtures[1].leaseID)
	if exactJob.State != string(ProcessingCanceled) || exactJob.IsCurrent || exactJob.CurrentAttemptID != nil {
		t.Fatalf("exact non-current job not settled state=%q current=%t has_current_attempt=%t",
			exactJob.State, exactJob.IsCurrent, exactJob.CurrentAttemptID != nil)
	}
	if otherJob.State != string(ProcessingProcessing) || otherJob.IsCurrent || !otherInterest.Active || otherGrant.State != "active" || otherLease.Status != string(backupasset.LeaseActive) {
		t.Fatalf("unrelated Processing authority changed job_state=%q job_current=%t interest_active=%t grant_state=%q lease_status=%q",
			otherJob.State, otherJob.IsCurrent, otherInterest.Active, otherGrant.State, otherLease.Status)
	}
	var releasedHistory model.RecoveryPointLease
	if err := db.First(&releasedHistory, "id = ?", releasedLeaseID).Error; err != nil || releasedHistory.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("released Processing lease history changed status=%q err=%v", releasedHistory.Status, err)
	}
	if proof.calls != 0 {
		t.Fatalf("prepare called Search proof %d times", proof.calls)
	}
}

type processingSearchProofSpy struct {
	calls       int
	beforeProof func() error
}

func (spy *processingSearchProofSpy) ProveRecoveryPointRevoked(context.Context, backupasset.SourceLifecycleRequest) error {
	spy.calls++
	if spy.calls == 1 && spy.beforeProof != nil {
		return spy.beforeProof()
	}
	return nil
}

func assertProcessingSourcePrepared(t *testing.T, harness *derivedLifecycleHarness, jobID, attemptID, grantID, leaseID string) {
	t.Helper()
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var grant model.BackupAssetProcessingGrant
	var lease model.RecoveryPointLease
	harness.db.First(&job, "id = ?", jobID)
	harness.db.First(&attempt, "id = ?", attemptID)
	harness.db.First(&grant, "id = ?", grantID)
	harness.db.First(&lease, "id = ?", leaseID)
	if job.State != string(ProcessingCanceled) || job.IsCurrent || attempt.State != "canceled" || attempt.IsCurrent || grant.State != "revoked" || lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("Processing prepare job_state=%q job_current=%t attempt_state=%q attempt_current=%t grant_state=%q grant_in_flight=%d lease_status=%q",
			job.State, job.IsCurrent, attempt.State, attempt.IsCurrent, grant.State, grant.InFlight, lease.Status)
	}
}

func assertDerivedPayload(t *testing.T, harness *derivedLifecycleHarness, reference derivedReferenceFixture, blobID string, preserved bool) {
	t.Helper()
	var set model.BackupAssetDerivedArtifactSet
	var ref model.BackupAssetDerivedBlobReference
	var blob model.BackupAssetDerivedBlob
	harness.db.First(&set, "id = ?", reference.setID)
	harness.db.First(&ref, "artifact_id = ?", reference.authorization.ArtifactID)
	harness.db.First(&blob, "id = ?", blobID)
	if preserved {
		if set.State != "active" || ref.State != "active" || blob.State != "active" || len(blob.WrappedDEK) == 0 {
			t.Fatalf("Derived payload not preserved set_state=%q reference_state=%q blob_state=%q key_present=%t",
				set.State, ref.State, blob.State, len(blob.WrappedDEK) != 0)
		}
		return
	}
	if set.State != "unavailable" || ref.State != "unavailable" || blob.State != "unavailable" || len(blob.WrappedDEK) != 0 {
		t.Fatalf("Derived payload not revoked set_state=%q reference_state=%q blob_state=%q key_erased=%t",
			set.State, ref.State, blob.State, len(blob.WrappedDEK) == 0)
	}
}
