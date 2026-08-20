package processing

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestLeaseCrashRecoveryCreatesNewFenceAndRejectsOldAttempt(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "6")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "recovery", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second, MaxRequests: 4, MaxBytesPerRequest: 16, MaxCumulativeBytes: 32, MaxInFlight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	material, err := grants.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
		JobID: oldLease.JobID, AttemptID: oldLease.AttemptID, WorkerID: oldLease.WorkerID,
		RecoveryPointFence: oldLease.RecoveryPointFence,
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(31 * time.Second)
	reconciler, err := NewReconciler(harness.coordinator, grants, harness.clock.Now, ReconcilerConfig{BatchSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Reconcile(context.Background())
	if err != nil || result.ExpiredAttempts != 1 {
		t.Fatalf("Reconcile result=%+v err=%v", result, err)
	}
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var inputGrant model.BackupAssetProcessingGrant
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", oldLease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&inputGrant, "id = ?", material.Input.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingRetryWait) || job.RetryAt == nil || attempt.State != "expired" ||
		attempt.OutcomeCode != string(ProcessingErrorLeaseLost) || attempt.IsCurrent ||
		inputGrant.State != string(GrantRevoked) || inputGrant.RevocationReason != "lease_lost" {
		t.Fatalf("crash product invalid: job=%+v attempt=%+v grant=%+v", job, attempt, inputGrant)
	}
	if _, err := harness.coordinator.Heartbeat(context.Background(), HeartbeatRequest{AttemptID: oldLease.AttemptID, WorkerID: workerID}); !errors.Is(err, ErrAttemptLost) {
		t.Fatalf("old heartbeat got %v", err)
	}

	harness.clock.Advance(6 * time.Second)
	promoted, err := reconciler.PromoteRetries(context.Background())
	if err != nil || promoted != 1 {
		t.Fatalf("PromoteRetries=%d err=%v", promoted, err)
	}
	newLease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatalf("Pull takeover: %v", err)
	}
	if newLease.AttemptID == oldLease.AttemptID || newLease.RecoveryPointFence.AttemptID == oldLease.RecoveryPointFence.AttemptID ||
		newLease.RecoveryPointFence.FenceToken == oldLease.RecoveryPointFence.FenceToken {
		t.Fatalf("takeover reused old authority: old=%+v new=%+v", oldLease, newLease)
	}
	if err := harness.coordinator.leaseService.ValidateFence(context.Background(), oldLease.RecoveryPointFence); err == nil {
		t.Fatal("old RecoveryPoint fence became valid after takeover")
	}
}

func TestReconcilerExpiresAbsoluteDeadlineWithoutRetry(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "7")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "deadline", PriorityClass: PriorityBackground, Priority: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", work.JobID).
		Update("absolute_deadline", harness.clock.Now().Add(20*time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", lease.AttemptID).
		Update("absolute_deadline", harness.clock.Now().Add(20*time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(31 * time.Second)
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: time.Second, MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(harness.coordinator, grants, harness.clock.Now, ReconcilerConfig{BatchSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingExpired) || job.ExpiryReason != string(ExpiryReasonDeadline) || job.IsCurrent || job.FinishedAt == nil {
		t.Fatalf("deadline did not produce closed expiry: %+v", job)
	}
}

func TestReconcilerShutdownRetiresAttemptsReleasesLeasesAndPreservesQueuedWork(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "3")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "shutdown-drain",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL:         30 * time.Second,
		InputLimits: GrantLimits{MaxRequests: 2, MaxBytesPerRequest: 16, MaxCumulativeBytes: 32, MaxInFlight: 1},
		SinkLimits:  GrantLimits{MaxRequests: 2, MaxBytesPerRequest: 16, MaxCumulativeBytes: 32, MaxInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := harness.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: workerID}, grants)
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(harness.coordinator, grants, harness.clock.Now, ReconcilerConfig{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	drained, err := reconciler.Shutdown(context.Background())
	if err != nil || drained != 1 {
		t.Fatalf("Shutdown drained=%d err=%v", drained, err)
	}

	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var lease model.RecoveryPointLease
	var worker model.BackupAssetWorkerIdentity
	var persistedGrants []model.BackupAssetProcessingGrant
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", leased.Lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&lease, "id = ?", leased.Lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&worker, "id = ?", workerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("attempt_id = ?", attempt.ID).Find(&persistedGrants).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingRetryWait) || !job.IsCurrent || job.CurrentAttemptID != nil ||
		job.ErrorCode != string(ProcessingErrorWorkerUnavailable) || job.RetryAt == nil || job.FinishedAt != nil {
		t.Fatalf("shutdown did not preserve retryable work: %+v", job)
	}
	if attempt.State != "canceled" || attempt.IsCurrent || attempt.OutcomeCode != "" || attempt.FinishedAt == nil {
		t.Fatalf("shutdown attempt product invalid: %+v", attempt)
	}
	if lease.Status != string(backupasset.LeaseReleased) || worker.HealthState != "draining" {
		t.Fatalf("shutdown authority remains active: lease=%+v worker=%+v", lease, worker)
	}
	if len(persistedGrants) != 2 {
		t.Fatalf("shutdown grant count=%d, want 2", len(persistedGrants))
	}
	for _, grant := range persistedGrants {
		if grant.State != string(GrantRevoked) || grant.RevocationReason != "shutdown" || grant.ActivationSecretHash != "" {
			t.Fatalf("shutdown grant product invalid: %+v", grant)
		}
	}
	if drained, err = reconciler.Shutdown(context.Background()); err != nil || drained != 0 {
		t.Fatalf("idempotent Shutdown drained=%d err=%v", drained, err)
	}
}

func TestDerivedReconcilerRevokesProjectionBeforeRepairingMissingReferencedBlob(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("missing-referenced-derived"), 4000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "5", "e")
	harness.projection.onRevoke = func(*gorm.DB, DerivedProjectionRevoke) error {
		var row model.BackupAssetDerivedBlobReference
		if err := harness.db.First(&row, "artifact_id = ?", reference.authorization.ArtifactID).Error; err != nil {
			return err
		}
		if row.State != "active" {
			return errors.New("derived reference changed before Search revoke")
		}
		return nil
	}
	if err := os.Remove(filepath.Join(harness.root, blob.OpaqueLocator)); err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewDerivedReconciler(harness.store, harness.lifecycle, 32)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RevokedUnreadableSets != 1 || harness.projection.revocations != 1 {
		t.Fatalf("missing blob reconciliation=%+v projection=%d", result, harness.projection.revocations)
	}
	var row model.BackupAssetDerivedBlob
	if err := harness.db.First(&row, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "unavailable" || row.RefCount != 0 || len(row.WrappedDEK) != 0 {
		t.Fatalf("missing referenced blob remained readable: %+v", row)
	}
}

func TestDerivedReconcilerRetriesMissingBlobImmediatelyAfterSearchRevokeFailure(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("missing-blob-search-retry"), 3000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "4", "b")
	if err := os.Remove(filepath.Join(harness.root, blob.OpaqueLocator)); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("search revoke temporarily unavailable")
	harness.projection.onRevoke = func(*gorm.DB, DerivedProjectionRevoke) error { return injected }
	reconciler, err := NewDerivedReconciler(harness.store, harness.lifecycle, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("first missing-blob reconciliation error=%v, want Search failure", err)
	}
	var set model.BackupAssetDerivedArtifactSet
	var persistedReference model.BackupAssetDerivedBlobReference
	var persistedBlob model.BackupAssetDerivedBlob
	if err := harness.db.First(&set, "id = ?", reference.setID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&persistedReference, "artifact_id = ?", reference.authorization.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&persistedBlob, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if set.State != "active" || !set.ProjectionPublished || persistedReference.State != "active" ||
		persistedBlob.State != "active" || persistedBlob.RefCount != 1 || len(persistedBlob.WrappedDEK) == 0 {
		t.Fatalf("Search failure mutated Derived state: set=%+v reference=%+v blob=%+v", set, persistedReference, persistedBlob)
	}

	harness.projection.onRevoke = nil
	result, err := reconciler.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("immediate missing-blob retry: %v", err)
	}
	if result.RevokedUnreadableSets != 1 || harness.projection.revocations != 2 {
		t.Fatalf("missing-blob retry skipped failed cursor: result=%+v revocations=%d", result, harness.projection.revocations)
	}
}

func TestDerivedReconcilerRepairsRefcountStagingAndFileOrphans(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	referencedPayload := bytes.Repeat([]byte("refcount-repair"), 4000)
	referenced, err := harness.store.PutBlob(context.Background(), derivedDeclaration(referencedPayload), bytes.NewReader(referencedPayload))
	if err != nil {
		t.Fatal(err)
	}
	harness.seedReference(t, referenced.BlobID, "6", "f")
	if err := harness.db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", referenced.BlobID).Update("ref_count", 99).Error; err != nil {
		t.Fatal(err)
	}

	stagedPayload := bytes.Repeat([]byte("staging-recovery"), 4000)
	staged, err := harness.store.PutBlob(context.Background(), derivedDeclaration(stagedPayload), bytes.NewReader(stagedPayload))
	if err != nil {
		t.Fatal(err)
	}
	stagingPath := filepath.Join(harness.root, ".staging-"+staged.BlobID)
	if err := os.Rename(filepath.Join(harness.root, staged.OpaqueLocator), stagingPath); err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", staged.BlobID).
		Updates(map[string]any{"state": "staged", "updated_at": harness.store.utcNow().Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}

	orphanID := strings.Repeat("9", 32)
	orphanPath := filepath.Join(harness.root, orphanID+".xrd")
	if err := os.WriteFile(orphanPath, []byte("opaque-orphan-ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewDerivedReconciler(harness.store, harness.lifecycle, 32)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RepairedRefCounts != 1 || result.RecoveredStaging != 1 || result.RemovedFileOrphans != 1 {
		t.Fatalf("Derived reconciliation result=%+v", result)
	}
	var referencedRow model.BackupAssetDerivedBlob
	var stagedRow model.BackupAssetDerivedBlob
	if err := harness.db.First(&referencedRow, "id = ?", referenced.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&stagedRow, "id = ?", staged.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if referencedRow.RefCount != 1 || stagedRow.State != "active" {
		t.Fatalf("Derived rows not repaired: referenced=%+v staged=%+v", referencedRow, stagedRow)
	}
	if _, err := os.Stat(filepath.Join(harness.root, staged.OpaqueLocator)); err != nil {
		t.Fatalf("staged ciphertext was not finalized: %v", err)
	}
	if _, err := os.Stat(orphanPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filesystem orphan remains: %v", err)
	}
}

func TestDerivedReconcilerResumesDEKRewrapToActiveVersion(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("resumable-rewrap"), 4000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(harness.root, blob.OpaqueLocator)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	active, err := harness.keyring.Rotate(context.Background(), backupasset.KeyDomainDerivedStore, 0)
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewDerivedReconciler(harness.store, harness.lifecycle, 32)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RewrappedBlobs != 1 {
		t.Fatalf("Derived rewrap reconciliation=%+v", result)
	}
	var row model.BackupAssetDerivedBlob
	if err := harness.db.First(&row, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if row.DerivedKEKVersion != active.Version || !bytes.Equal(before, after) {
		t.Fatalf("reconciliation rewrote ciphertext or left old KEK: row=%+v", row)
	}
}

func TestDerivedReconcilerContinuesCiphertextCleanupAfterKeyLoss(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("lost-key-orphan-cleanup"), 2000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	active, err := harness.keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.lifecycle.MarkActiveKeyLost(context.Background(), active.Version, 32); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(harness.root, blob.OpaqueLocator)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("key-loss setup removed unreferenced ciphertext before reconciliation: %v", err)
	}
	reconciler, err := NewDerivedReconciler(harness.store, harness.lifecycle, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after key loss: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost-key ciphertext remains: %v", err)
	}
}

func TestDerivedReconcilerRetriesFailedPurgeAndClearsFailureState(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("retry-purge"), 3000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "9", "a")
	injected := errors.New("injected ciphertext removal failure")
	removeCalls := 0
	harness.store.removeFile = func(path string) error {
		removeCalls++
		if removeCalls == 1 {
			return injected
		}
		return os.Remove(path)
	}
	if err := harness.lifecycle.RevokeSetFenced(context.Background(), reference.setID, DerivedRevokeExpired,
		derivedLifecycleFence("9", reference.authorization.RecoveryPointID)); !errors.Is(err, ErrDerivedBlobUnavailable) || errors.Is(err, injected) || err.Error() != ErrDerivedBlobUnavailable.Error() {
		t.Fatalf("RevokeSet removal error=%v, want closed ErrDerivedBlobUnavailable without private cause", err)
	}
	var failed model.BackupAssetDerivedBlob
	if err := harness.db.First(&failed, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if failed.State != "purge_failed" || len(failed.WrappedDEK) != 0 {
		t.Fatalf("failed purge was not cryptographically safe: %+v", failed)
	}
	reconciler, err := NewDerivedReconciler(harness.store, harness.lifecycle, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile retry: %v", err)
	}
	var repaired model.BackupAssetDerivedBlob
	if err := harness.db.First(&repaired, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if repaired.State != "unavailable" {
		t.Fatalf("successful purge retry retained failure state: %+v", repaired)
	}
	if _, err := os.Stat(filepath.Join(harness.root, blob.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext remains after purge retry: %v", err)
	}
}
