package processing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCoordinatorNoWorkerIsInformationalWithoutJobPersistence(t *testing.T) {
	harness := newCoordinatorHarness(t)
	_, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "workspace-a", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if !errors.Is(err, ErrNotDeployed) {
		t.Fatalf("RequestWork without Worker got %v, want ErrNotDeployed", err)
	}
	var count int64
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("no-Worker request persisted %d jobs: %v", count, err)
	}
}

func TestCoordinatorCoalescesSameWorkKeyAndKeepsIndependentInterests(t *testing.T) {
	harness := newCoordinatorHarness(t)
	harness.registerNoopWorker(t, "1")
	first, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "workspace-a", PriorityClass: PriorityBackground, Priority: 10},
	})
	if err != nil {
		t.Fatalf("RequestWork(first): %v", err)
	}
	second, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSearch, OwnerKey: "search-a", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatalf("RequestWork(second): %v", err)
	}
	if first.JobID != second.JobID || second.Created {
		t.Fatalf("same work was not coalesced: first=%+v second=%+v", first, second)
	}
	if backupasset.ValidateOpaqueID(first.InterestID) != nil || backupasset.ValidateOpaqueID(second.InterestID) != nil ||
		first.InterestID == second.InterestID || first.InterestID == first.JobID || second.InterestID == second.JobID {
		t.Fatalf("coalesced work did not return independent opaque interest handles: first=%+v second=%+v", first, second)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", first.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.PriorityClass != string(PriorityInteractive) || job.EffectivePriority != 100 {
		t.Fatalf("effective priority was not upgraded: %+v", job)
	}
	var interests int64
	if err := harness.db.Model(&model.BackupAssetProcessingInterest{}).Where("job_id = ? AND active = ?", first.JobID, true).Count(&interests).Error; err != nil || interests != 2 {
		t.Fatalf("active interests=%d, err=%v", interests, err)
	}
	for interestID, ownerKey := range map[string]string{first.InterestID: "workspace-a", second.InterestID: "search-a"} {
		var interest model.BackupAssetProcessingInterest
		if err := harness.db.First(&interest, "id = ?", interestID).Error; err != nil {
			t.Fatal(err)
		}
		if interest.JobID != first.JobID || interest.OwnerKey != ownerKey || !interest.Active {
			t.Fatalf("interest handle %q resolved to %+v", interestID, interest)
		}
	}
}

func TestCoordinatorConcurrentSameKeyCreatesOneCurrentJob(t *testing.T) {
	harness := newCoordinatorHarness(t)
	harness.registerNoopWorker(t, "2")
	const callers = 8
	results := make(chan WorkResult, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
				Descriptor: validWorkDescriptor(),
				Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: fmt.Sprintf("workspace-%d", index), PriorityClass: PriorityBackground, Priority: index},
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent RequestWork: %v", err)
	}
	jobID := ""
	for result := range results {
		if jobID == "" {
			jobID = result.JobID
		}
		if result.JobID != jobID {
			t.Fatalf("concurrent callers received different jobs: %q != %q", result.JobID, jobID)
		}
	}
	var count int64
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("is_current = ?", true).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("current job count=%d, err=%v", count, err)
	}
}

func TestInterestRemovalCancelsOnlyAfterLastReferenceAndRevokesGrantsFirst(t *testing.T) {
	harness := newCoordinatorHarness(t)
	harness.registerNoopWorker(t, "3")
	first, _ := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "workspace-a", PriorityClass: PriorityInteractive, Priority: 100},
	})
	_, _ = harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSearch, OwnerKey: "search-a", PriorityClass: PriorityBackground, Priority: 10},
	})
	if err := harness.coordinator.RemoveInterest(context.Background(), first.JobID, InterestWorkspace, "workspace-a", InterestRemovedCanceled); err != nil {
		t.Fatalf("RemoveInterest(first): %v", err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", first.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingQueued) || job.PriorityClass != string(PriorityBackground) || job.EffectivePriority != 10 {
		t.Fatalf("one-interest removal canceled or failed to reprioritize job: %+v", job)
	}

	grant := model.BackupAssetProcessingGrant{
		ID: strings.Repeat("7", 32), JobID: first.JobID, AttemptID: strings.Repeat("8", 32), WorkerID: strings.Repeat("3", 32),
		Kind: "input", ActivationSecretHash: strings.Repeat("9", 64), FenceHash: strings.Repeat("a", 64), State: "issued",
		MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1,
		ExpiresAt: harness.clock.Now().Add(time.Minute), CreatedAt: harness.clock.Now(), UpdatedAt: harness.clock.Now(), Version: 1,
	}
	if err := harness.db.Create(&grant).Error; err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	if err := harness.coordinator.RemoveInterest(context.Background(), first.JobID, InterestSearch, "search-a", InterestRemovedCanceled); err != nil {
		t.Fatalf("RemoveInterest(last): %v", err)
	}
	if err := harness.db.First(&job, "id = ?", first.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var storedGrant model.BackupAssetProcessingGrant
	if err := harness.db.First(&storedGrant, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingCancelRequested) || storedGrant.State != "revoked" || storedGrant.ActivationSecretHash != "" || storedGrant.RevokedAt == nil {
		t.Fatalf("last-interest order/product invalid: job=%+v grant=%+v", job, storedGrant)
	}
}

func TestCoordinatorPullBindsWorkerAndRecoveryPointLeases(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "4")
	result, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "workspace-a", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if lease.JobID != result.JobID || lease.AttemptID == "" || lease.RecoveryPointFence.LeaseID == "" || lease.RecoveryPointFence.HolderType != backupasset.LeaseHolderProcessingJob {
		t.Fatalf("pull did not bind dual leases: %+v", lease)
	}
	beforeWorkerDeadline := lease.WorkerLeaseExpiresAt
	beforePointDeadline := lease.RecoveryPointLeaseExpiresAt
	harness.clock.Advance(10 * time.Second)
	heartbeat, err := harness.coordinator.Heartbeat(context.Background(), HeartbeatRequest{AttemptID: lease.AttemptID, WorkerID: workerID})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !heartbeat.WorkerLeaseExpiresAt.After(beforeWorkerDeadline) || !heartbeat.RecoveryPointLeaseExpiresAt.After(beforePointDeadline) {
		t.Fatalf("heartbeat did not renew both leases: before=%+v after=%+v", lease, heartbeat)
	}
}

func TestCoordinatorPullAttemptAtomicallyBindsLeasesAndOneUseGrants(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "5")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "workspace-atomic", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second, MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Exec(`CREATE TRIGGER reject_atomic_sink_grant BEFORE INSERT ON backup_asset_processing_grants
		WHEN NEW.kind = 'sink' BEGIN SELECT RAISE(ABORT, 'reject atomic sink grant'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: workerID}, grants); err == nil {
		t.Fatal("PullAttempt unexpectedly committed a partial lease")
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingQueued) || job.CurrentAttemptID != nil || job.TransitionRevision != 1 {
		t.Fatalf("failed atomic pull mutated job: %+v", job)
	}
	for name, table := range map[string]any{
		"attempts": &model.BackupAssetProcessingAttempt{},
		"grants":   &model.BackupAssetProcessingGrant{},
		"leases":   &model.RecoveryPointLease{},
	} {
		var count int64
		if err := harness.db.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("failed atomic pull left %s=%d: %v", name, count, err)
		}
	}
	if err := harness.db.Exec(`DROP TRIGGER reject_atomic_sink_grant`).Error; err != nil {
		t.Fatal(err)
	}

	leased, err := harness.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: workerID}, grants)
	if err != nil {
		t.Fatalf("PullAttempt: %v", err)
	}
	if leased.Lease.JobID != work.JobID || leased.Lease.AttemptID == "" ||
		leased.Grants.Input.GrantID == leased.Grants.Sink.GrantID || leased.Grants.Input.Secret == leased.Grants.Sink.Secret ||
		!lowerHex(leased.Grants.Input.Secret, 64) || !lowerHex(leased.Grants.Sink.Secret, 64) {
		t.Fatalf("invalid atomic pull envelope: %+v", leased)
	}
	var stored []model.BackupAssetProcessingGrant
	if err := harness.db.Order("kind ASC").Find(&stored).Error; err != nil || len(stored) != 2 {
		t.Fatalf("stored grants=%d: %v", len(stored), err)
	}
	for _, grant := range stored {
		secret := leased.Grants.Input.Secret
		if grant.Kind == string(GrantSink) {
			secret = leased.Grants.Sink.Secret
		}
		if grant.ActivationSecretHash == secret || !constantTimeSecretMatch(grant.ActivationSecretHash, secret) ||
			grant.AttemptID != leased.Lease.AttemptID || grant.WorkerID != workerID {
			t.Fatalf("grant did not persist a bound hash only: %+v", grant)
		}
	}
}

func TestCoordinatorAttemptTransitionsKeepFetchingAndMaterializingIndependent(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "6")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "transition-test", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	revision := int64(2)
	for _, state := range []ProcessingState{ProcessingFetching, ProcessingMaterializing, ProcessingProcessing, ProcessingUploading} {
		result, transitionErr := harness.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
			JobID: work.JobID, AttemptID: lease.AttemptID, WorkerID: workerID,
			ExpectedRevision: revision, To: state,
		})
		if transitionErr != nil {
			t.Fatalf("transition to %s: %v", state, transitionErr)
		}
		revision++
		if result.State != state || result.Revision != revision || result.CancelRequested {
			t.Fatalf("transition to %s result=%+v", state, result)
		}
	}
	if _, err := harness.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
		JobID: work.JobID, AttemptID: lease.AttemptID, WorkerID: workerID,
		ExpectedRevision: revision - 1, To: ProcessingUploading,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale transition got %v, want ErrRevisionConflict", err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingUploading) || job.TransitionRevision != revision {
		t.Fatalf("stale transition mutated job: %+v", job)
	}
}

func TestCoordinatorAttemptTransitionFailsClosedAfterFenceLoss(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "7")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "transition-fence", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", lease.RecoveryPointFence.LeaseID).
		Update("fence_token", strings.Repeat("f", 64)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
		JobID: work.JobID, AttemptID: lease.AttemptID, WorkerID: workerID,
		ExpectedRevision: 2, To: ProcessingFetching,
	}); !errors.Is(err, ErrAttemptLost) {
		t.Fatalf("lost-fence transition got %v, want ErrAttemptLost", err)
	}
}

func TestCoordinatorHeartbeatReportsCancelAndDrainWithoutLosingCoreAvailability(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "8")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "heartbeat-cancel", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.RemoveInterest(context.Background(), work.JobID, InterestWorkspace, "heartbeat-cancel", InterestRemovedCanceled); err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetWorkerIdentity{}).Where("id = ?", workerID).Update("health_state", "draining").Error; err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(time.Second)
	heartbeat, err := harness.coordinator.Heartbeat(context.Background(), HeartbeatRequest{AttemptID: lease.AttemptID, WorkerID: workerID})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !heartbeat.CancelRequested || heartbeat.CancelReason != CancelReasonInterestWithdrawn || !heartbeat.WorkerDraining || heartbeat.TransitionRevision != 3 {
		t.Fatalf("heartbeat omitted cancel/drain state: %+v", heartbeat)
	}
	wantEffective := heartbeat.WorkerLeaseExpiresAt
	if heartbeat.RecoveryPointLeaseExpiresAt.Before(wantEffective) {
		wantEffective = heartbeat.RecoveryPointLeaseExpiresAt
	}
	if !heartbeat.EffectiveLeaseExpiresAt.Equal(wantEffective) {
		t.Fatalf("effective deadline=%s, want %s", heartbeat.EffectiveLeaseExpiresAt, wantEffective)
	}
}

func TestCoordinatorCancellationKeepsAttemptOutcomeSeparateFromCancelReason(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "c")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestWorkspace, OwnerKey: "cancel-outcome-product",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.RemoveInterest(
		context.Background(), work.JobID, InterestWorkspace, "cancel-outcome-product", InterestRemovedCanceled,
	); err != nil {
		t.Fatal(err)
	}
	result, err := harness.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
		JobID: work.JobID, AttemptID: lease.AttemptID, WorkerID: workerID,
		ExpectedRevision: 3, To: ProcessingCanceled, CancelReason: CancelReasonInterestWithdrawn,
	})
	if err != nil {
		t.Fatalf("acknowledge cancellation: %v", err)
	}
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if result.State != ProcessingCanceled || job.CancelReason != string(CancelReasonInterestWithdrawn) ||
		attempt.State != "canceled" || attempt.OutcomeCode != "" {
		t.Fatalf("cancel reason leaked into attempt outcome: result=%+v job=%+v attempt=%+v", result, job, attempt)
	}
}

func TestCoordinatorHeartbeatRollsBackRecoveryPointRenewalWhenWorkerLeaseUpdateFails(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "d")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "heartbeat-atomic", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil || lease.JobID != work.JobID {
		t.Fatalf("Pull: lease=%+v err=%v", lease, err)
	}
	var beforeLease model.RecoveryPointLease
	var beforeAttempt model.BackupAssetProcessingAttempt
	if err := harness.db.First(&beforeLease, "id = ?", lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&beforeAttempt, "id = ?", lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(5 * time.Second)
	if err := harness.db.Exec(`CREATE TRIGGER fail_processing_attempt_heartbeat
		BEFORE UPDATE OF worker_lease_expires_at ON backup_asset_processing_attempts
		BEGIN SELECT RAISE(ABORT, 'injected attempt heartbeat failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.Heartbeat(context.Background(), HeartbeatRequest{AttemptID: lease.AttemptID, WorkerID: workerID}); err == nil {
		t.Fatal("heartbeat unexpectedly survived injected attempt update failure")
	}
	var afterLease model.RecoveryPointLease
	var afterAttempt model.BackupAssetProcessingAttempt
	if err := harness.db.First(&afterLease, "id = ?", lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&afterAttempt, "id = ?", lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if !afterLease.LeaseExpiresAt.Equal(beforeLease.LeaseExpiresAt) || !afterLease.LastHeartbeatAt.Equal(beforeLease.LastHeartbeatAt) ||
		!afterAttempt.WorkerLeaseExpiresAt.Equal(beforeAttempt.WorkerLeaseExpiresAt) || !afterAttempt.LastHeartbeatAt.Equal(beforeAttempt.LastHeartbeatAt) {
		t.Fatalf("dual heartbeat committed partially: lease before=%+v after=%+v attempt before=%+v after=%+v",
			beforeLease, afterLease, beforeAttempt, afterAttempt)
	}
}

func TestCoordinatorRetryExhaustionClosesJobAsFailedInsteadOfReturningInvalidTransition(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "e")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "retry-exhausted", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", work.JobID).Update("retry_count", 5).Error; err != nil {
		t.Fatal(err)
	}
	retryAt := harness.clock.Now().Add(time.Minute)
	result, err := harness.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
		JobID: work.JobID, AttemptID: lease.AttemptID, WorkerID: workerID,
		ExpectedRevision: 2, To: ProcessingRetryWait, ErrorCode: ProcessingErrorWorkerUnavailable, RetryAt: &retryAt,
	})
	if err != nil {
		t.Fatalf("retry exhaustion returned an open error: %v", err)
	}
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var recoveryLease model.RecoveryPointLease
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&recoveryLease, "id = ?", lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if result.State != ProcessingFailed || result.Revision != 3 || job.State != string(ProcessingFailed) || job.IsCurrent ||
		job.RetryCount != 5 || job.ErrorCode != string(ProcessingErrorWorkerUnavailable) || attempt.State != "failed" || attempt.IsCurrent ||
		recoveryLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("retry exhaustion was not terminal: result=%+v job=%+v attempt=%+v lease=%+v", result, job, attempt, recoveryLease)
	}
}

func TestCoordinatorContractFailureQuarantinesWorkerAndRevokesGrantsAtomically(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "9")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "contract-failure", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second, MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := harness.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: workerID}, grants)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
		JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: workerID,
		ExpectedRevision: 2, To: ProcessingFetching,
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := harness.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
		JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: workerID,
		ExpectedRevision: 3, To: ProcessingFailed, ErrorCode: ProcessingErrorInvalidOutput,
	})
	if err != nil {
		t.Fatalf("contract failure transition: %v", err)
	}
	if failed.State != ProcessingFailed || failed.Revision != 4 {
		t.Fatalf("contract failure result=%+v", failed)
	}
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var worker model.BackupAssetWorkerIdentity
	var storedGrants []model.BackupAssetProcessingGrant
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", leased.Lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&worker, "id = ?", workerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("attempt_id = ?", attempt.ID).Find(&storedGrants).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingFailed) || job.IsCurrent || job.ErrorCode != string(ProcessingErrorInvalidOutput) ||
		attempt.State != "failed" || attempt.IsCurrent || worker.TrustState != "quarantined" || worker.HealthState != "draining" ||
		worker.QuarantineCode != string(ProcessingErrorInvalidOutput) || len(storedGrants) != 2 {
		t.Fatalf("contract failure product invalid: job=%+v attempt=%+v worker=%+v grants=%+v", job, attempt, worker, storedGrants)
	}
	for _, grant := range storedGrants {
		if grant.State != string(GrantRevoked) || grant.ActivationSecretHash != "" || grant.RevocationReason != "quarantine" {
			t.Fatalf("contract failure grant remained usable: %+v", grant)
		}
	}
	var lease model.RecoveryPointLease
	if err := harness.db.First(&lease, "id = ?", leased.Lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("contract failure retained RecoveryPoint lease: %+v", lease)
	}
}

type coordinatorHarness struct {
	db          *gorm.DB
	clock       *coordinatorClock
	coordinator *Coordinator
}

type coordinatorClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *coordinatorClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *coordinatorClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func newCoordinatorHarness(t *testing.T) *coordinatorHarness {
	t.Helper()
	dsn := processingTestSQLiteDSN(t)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(16)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetWorkerIdentity{}, &model.BackupAssetWorkerCapability{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{},
		&model.BackupAssetProcessingGrantRequest{},
		&model.RecoveryPointLease{},
	); err != nil {
		t.Fatalf("migrate coordinator harness: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_processing_test_current_work ON backup_asset_processing_jobs(work_key) WHERE is_current = 1`).Error; err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorClock{now: time.Date(2026, 7, 19, 5, 6, 7, 0, time.UTC)}
	leaseService, err := backupasset.NewLeaseService(db, clock.Now, backupasset.LeaseConfig{Duration: 30 * time.Second, Heartbeat: 10 * time.Second, AbsoluteDeadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(db, leaseService, clock.Now, CoordinatorConfig{
		QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2,
		PullLease: 30 * time.Second, AttemptTimeout: 2 * time.Hour, RetryMax: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &coordinatorHarness{db: db, clock: clock, coordinator: coordinator}
}

func (harness *coordinatorHarness) registerNoopWorker(t *testing.T, suffix string) string {
	t.Helper()
	workerID := strings.Repeat(suffix, 32)
	now := harness.clock.Now()
	worker := model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: "local", TransportFingerprint: strings.Repeat(suffix, 64), InstanceID: strings.Repeat(suffix, 32),
		IdentityRevision: 1, ProtocolVersion: 1, TrustState: "active", HealthState: "ready", InteractiveSlots: 2, BackgroundSlots: 2,
		LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := harness.db.Create(&worker).Error; err != nil {
		t.Fatal(err)
	}
	descriptor := validWorkDescriptor()
	capability := model.BackupAssetWorkerCapability{
		ID: strings.Repeat(suffix, 31) + "f", WorkerID: workerID, Capability: descriptor.Capability,
		CapabilitySchema: descriptor.CapabilitySchema, PipelineFingerprint: descriptor.PipelineFingerprint,
		OutputProfile: descriptor.OutputProfile, InputModes: "stat,sequential,range", LimitsCanonical: []byte{1},
		AdvertisementDigest: strings.Repeat("a", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := harness.db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	return workerID
}
