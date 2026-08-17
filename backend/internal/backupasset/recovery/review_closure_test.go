package recovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm/clause"
)

// These review selectors bind the immutable Task 11 ledger to the substantive
// regression tests that closed each finding during Tasks 1-10. The original
// frozen commands exited successfully without selecting these absent entries;
// Task 11 records that historical gap separately from the inventory-guard RED.

func TestRecoveryReviewF1WriteAuthorityOneUse(t *testing.T) {
	TestRecoveryAuthorizationReceiptWriteAuthorizeReplayAndConflict(t)
}

func TestRecoveryReviewF1ExactMirrorDeleteAuthority(t *testing.T) {
	TestContractExactMirrorDeleteAuthorityConsumption(t)
}

func TestRecoveryReviewF1InPlaceResultRefDenied(t *testing.T) {
	TestRecoveryResultResolverCollapsesUnsafeBindings(t)
}

func TestRecoveryReviewF2SecurityDecisionMatrix(t *testing.T) {
	TestPreflightSecurityDecisionMatrixAndRevisionDrift(t)
}

func TestRecoveryReviewF2AdminOverrideBinding(t *testing.T) {
	TestRecoveryAuthorizationReceiptSecurityOverrideRebindsDownstreamAuthority(t)
}

func TestRecoveryReviewF2OverrideAuditNoLeak(t *testing.T) {
	TestRecoveryAuthorizationAuditOmitsGrantProofAndSecretMaterial(t)
}

func TestRecoveryReviewF4RevokingCrashTakeover(t *testing.T) {
	testRecoveryReviewF4RevokingCrashTakeover(t, newRecoveryResultLifecycleFixture)
}

func TestRecoveryReviewF4RevokingCrashTakeoverPostgres(t *testing.T) {
	testRecoveryReviewF4RevokingCrashTakeover(t, newRecoveryResultLifecyclePostgresFixture)
}

func testRecoveryReviewF4RevokingCrashTakeover(
	t *testing.T,
	newFixture func(*testing.T) *recoveryResultLifecycleFixture,
) {
	t.Helper()
	fixture := newFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish cleanup takeover fixture: %v", err)
	}
	oldLeaseID := fixture.setResultSetCleanupState(t, published, ResultSetStateRevoking)
	oldExpiry := fixture.now.Add(-time.Minute)
	oldCreatedAt := oldExpiry.Add(-time.Minute)
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", oldLeaseID).
		Updates(map[string]any{
			"lease_expires_at": oldExpiry, "created_at": oldCreatedAt, "updated_at": fixture.now,
		}).Error; err != nil {
		t.Fatalf("expire old cleanup node lease: %v", err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
		Updates(map[string]any{
			"cleanup_phase": string(CleanupPhaseDrained), "cleanup_lease_expires_at": oldExpiry,
			"updated_at": fixture.now,
		}).Error; err != nil {
		t.Fatalf("expire old cleanup owner: %v", err)
	}
	priorMaxFence := fixture.maxNodeFence(t)

	claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: "cleanup-takeover-worker",
	})
	if err != nil {
		t.Fatalf("take over expired cleanup owner: %v", err)
	}
	if claim.CleanupFence != 2 || claim.CleanupAttempt != 2 ||
		claim.NodeFence != priorMaxFence+1 || claim.Phase != CleanupPhaseDrained {
		t.Fatalf("unexpected cleanup takeover claim: %+v prior_node_fence=%d", claim, priorMaxFence)
	}
	fixture.assertCleanupClaimRow(t, claim)
	var oldLease model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", oldLeaseID).Take(&oldLease).Error; err != nil {
		t.Fatalf("load expired cleanup node lease: %v", err)
	}
	if oldLease.State != "expired" || oldLease.ReleasedAt == nil {
		t.Fatalf("old cleanup node lease was not expired: %+v", oldLease)
	}
}

func TestRecoveryReviewF5CleanupNodeLeaseRaces(t *testing.T) {
	TestRecoveryResultCleanupClaimLocksJobAndNodeBeforeResultSetCAS(t)
	TestRecoveryResultCleanupLostCASReleasesFreshNodeLeaseInTransaction(t)
}

func TestRecoveryReviewF5CleanupNodeLeaseRacesPostgres(t *testing.T) {
	fixture := newRecoveryResultLifecyclePostgresFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish PostgreSQL cleanup lease fixture: %v", err)
	}

	// Hold the job row so both callers complete the candidate read and enter a
	// distinct PostgreSQL transaction before either can claim the shared node.
	// Waiting for both blocked FOR UPDATE statements makes this a real lock race,
	// rather than a sequential winner-then-loser check.
	barrier := fixture.db.Begin()
	if barrier.Error != nil {
		t.Fatalf("begin PostgreSQL cleanup race barrier: %v", barrier.Error)
	}
	barrierReleased := false
	defer func() {
		if !barrierReleased {
			_ = barrier.Rollback().Error
		}
	}()
	var lockedJob model.BackupAssetRecoveryJob
	if err := barrier.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", published.JobID).Take(&lockedJob).Error; err != nil {
		t.Fatalf("lock PostgreSQL cleanup race job: %v", err)
	}

	type claimResult struct {
		claim RecoveryResultCleanupClaim
		err   error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for _, workerID := range []string{"postgres-cleanup-one", "postgres-cleanup-two"} {
		workerID := workerID
		go func() {
			<-start
			claim, claimErr := fixture.service.ClaimCleanup(ctx, ClaimRecoveryResultCleanupRequest{
				ResultSetID: published.ResultSetID, WorkerID: workerID,
			})
			results <- claimResult{claim: claim, err: claimErr}
		}()
	}
	close(start)
	waitForRecoveryReviewF5PostgresLockRace(t, fixture, 2)
	if err := barrier.Commit().Error; err != nil {
		t.Fatalf("release PostgreSQL cleanup race barrier: %v", err)
	}
	barrierReleased = true

	var winner RecoveryResultCleanupClaim
	successes := 0
	busy := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successes++
			winner = result.claim
		case errors.Is(result.err, ErrRecoveryResultCleanupBusy):
			busy++
		default:
			t.Fatalf("concurrent PostgreSQL cleanup claim returned unstable error: %v", result.err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("concurrent PostgreSQL cleanup results successes=%d busy=%d, want 1/1", successes, busy)
	}
	var active []model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("job_id = ? AND holder_kind = ? AND state = ?",
		published.JobID, "recovery_cleanup", "active").Find(&active).Error; err != nil {
		t.Fatalf("load PostgreSQL cleanup node leases: %v", err)
	}
	if len(active) != 1 || active[0].ID != winner.NodeLeaseID || active[0].OwnerID != winner.WorkerID {
		t.Fatalf("PostgreSQL cleanup lease winners=%+v, want exactly %+v", active, winner)
	}
}

func waitForRecoveryReviewF5PostgresLockRace(
	t *testing.T,
	fixture *recoveryResultLifecycleFixture,
	want int64,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiters int64
		if err := fixture.db.Raw(`
			SELECT COUNT(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
				AND pid <> pg_backend_pid()
				AND wait_event_type = 'Lock'
				AND query LIKE '%backup_asset_recovery_jobs%'
				AND query LIKE '%FOR UPDATE%'`).Scan(&waiters).Error; err != nil {
			t.Fatalf("observe PostgreSQL cleanup race waiters: %v", err)
		}
		if waiters >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("PostgreSQL cleanup race lock waiters=%d, want at least %d", waiters, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRecoveryReviewF5CleanupLeaseLossDuringDelete(t *testing.T) {
	TestTargetCleanupLiveValidatorRunsBeforeEveryMutation(t)
}

func TestRecoveryReviewF5BusyNodeFairness(t *testing.T) {
	TestRecoveryListScheduledCleanupCandidatesGloballyOrdersLifecycleKinds(t)
}

func TestRecoveryReviewF8SourceLocatorSubstitution(t *testing.T) {
	TestSourceValidatorRejectsMutableLocatorAndProviderSubstitutionBeforeConsumer(t)
}

func TestRecoveryReviewF8TargetRootSubstitution(t *testing.T) {
	TestTargetPreflightRejectsTargetSubstitutionBeforeProbe(t)
}

func TestRecoveryReviewF8LocatorNoLeak(t *testing.T) {
	TestRecoveryLocatorProductNoPlaintextLeak(t)
}
