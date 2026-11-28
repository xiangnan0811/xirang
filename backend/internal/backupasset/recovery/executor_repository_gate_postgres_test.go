package recovery

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type executorGateAggregate struct {
	plan        model.BackupAssetRecoveryPlan
	job         model.BackupAssetRecoveryJob
	attempt     model.BackupAssetRecoveryAttempt
	source      model.RecoveryPointLease
	node        model.BackupAssetRecoveryNodeLease
	grant       model.BackupAssetRecoveryGrant
	items       []model.BackupAssetRecoveryJobItem
	checkpoints []model.BackupAssetRecoveryCheckpoint
	evidence    []model.BackupAssetRecoveryEvidence
}

func captureExecutorGateAggregate(t *testing.T, paused pausedAuthorizedExactMirrorDelete) executorGateAggregate {
	t.Helper()
	fixture := paused.execution
	db := fixture.serviceFixture.db
	var state executorGateAggregate
	for _, query := range []struct {
		dest any
		id   string
	}{
		{&state.plan, paused.job.PlanID},
		{&state.job, fixture.jobID},
		{&state.attempt, fixture.claim.AttemptID},
		{&state.source, fixture.claim.SourceFence.LeaseID},
		{&state.node, fixture.claim.NodeLeaseID},
		{&state.grant, paused.result.GrantID},
	} {
		if err := db.Where("id = ?", query.id).Take(query.dest).Error; err != nil {
			t.Fatalf("capture recovery aggregate: %v", err)
		}
	}
	if err := db.Where("job_id = ?", fixture.jobID).Order("ordinal ASC").Find(&state.items).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", fixture.jobID).Order("sequence ASC").Find(&state.checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", fixture.jobID).Order("id ASC").Find(&state.evidence).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRecoveryExecutorRepositoryGateGrantExpiry(t *testing.T) {
	testRecoveryExecutorRepositoryGate(t, "expiry")
}

func TestRecoveryExecutorRepositoryGateCancellation(t *testing.T) {
	testRecoveryExecutorRepositoryGate(t, "cancellation")
}

func TestRecoveryExecutorRepositoryGateGrantControl(t *testing.T) {
	testRecoveryExecutorRepositoryGate(t, "control")
}

func testRecoveryExecutorRepositoryGate(t *testing.T, scenario string) {
	t.Helper()
	fixture := newInterleavedExecutionFixture(t, true)
	// The delete grant must expire independently of the pause and worker leases.
	fixture.serviceFixture.service.deleteGrantTTL = 30 * time.Second
	paused := newPausedAuthorizedExactMirrorDeleteFromExecution(t, "executor-gate-"+scenario, fixture)
	before := captureExecutorGateAggregate(t, paused)
	if before.grant.ConsumedAt != nil || before.grant.RevokedAt != nil ||
		!before.grant.ExpiresAt.After(fixture.serviceFixture.now) || len(fixture.target.deletes) != 0 {
		t.Fatal("fixture did not retain a live unconsumed delete grant and untouched delete target")
	}
	expiredAt := before.grant.ExpiresAt.Add(time.Second)
	required := before.checkpoints[len(before.checkpoints)-1]
	if required.DeleteAuthorityExpiresAt == nil || before.attempt.LeaseExpiresAt == nil ||
		!required.DeleteAuthorityExpiresAt.After(expiredAt) || !before.attempt.LeaseExpiresAt.After(expiredAt) ||
		!before.node.LeaseExpiresAt.After(expiredAt) || !before.source.LeaseExpiresAt.After(expiredAt) ||
		!before.source.AbsoluteDeadline.After(expiredAt) || !before.job.PreflightExpiresAt.After(expiredAt) ||
		!before.job.AuthorityExpiresAt.After(expiredAt) {
		t.Fatal("fixture does not isolate the delete-grant expiry from other authority deadlines")
	}

	var clock atomic.Int64
	clock.Store(fixture.serviceFixture.now.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	fixture.coordinator.now = now
	fixture.target.now = now
	db := fixture.serviceFixture.db
	holder := db.Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	var holderPID int
	if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
		_ = holder.Rollback().Error
		t.Fatal(err)
	}
	key := "xirang:recovery-authority-repository:v1:" + before.plan.RepositoryID
	arrived := make(chan error, 1)
	var gateAcquired bool
	fixture.target.lstatErr = func(_ int) error {
		if gateAcquired {
			return nil
		}
		gateAcquired = true
		err := holder.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error
		arrived <- err
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	results := make(chan error, 1)
	done := make(chan struct{})
	source := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	go func() {
		defer close(done)
		results <- fixture.coordinator.ExecuteClaim(ctx, fixture.claim, source, paused.request.GrantSecret)
	}()
	defer func() {
		cancel()
		_ = holder.Rollback().Error
		<-done
	}()
	select {
	case err := <-arrived:
		if err != nil {
			t.Fatalf("acquire gate after target Lstat: %v", err)
		}
	case err := <-results:
		t.Fatalf("executor returned before target Lstat: %v", err)
	case <-ctx.Done():
		t.Fatalf("executor did not reach target Lstat: %v", ctx.Err())
	}

	// The holder was acquired in Lstat, before the consumption transaction.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiterPID := waitForExecutorRepositoryGate(t, ctx, db, holderPID, key, done)

	switch scenario {
	case "expiry":
		clock.Store(expiredAt.UnixNano())
	case "cancellation":
		var canceled bool
		if err := db.WithContext(ctx).Raw("SELECT pg_cancel_backend(?)", waiterPID).Scan(&canceled).Error; err != nil || !canceled {
			t.Fatalf("cancel waiting SQL: canceled=%t err=%v", canceled, err)
		}
		for {
			var stillWaiting bool
			if err := db.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = ? AND locktype = 'advisory' AND NOT granted)", waiterPID).Scan(&stillWaiting).Error; err != nil {
				t.Fatalf("inspect canceled waiter: %v", err)
			}
			if !stillWaiting {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("canceled SQL remained at repository gate: %v", ctx.Err())
			case <-ticker.C:
			}
		}
	case "control":
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	if err := holder.Rollback().Error; err != nil {
		t.Fatalf("release repository gate: %v", err)
	}
	var result error
	select {
	case result = <-results:
	case <-ctx.Done():
		t.Fatalf("executor did not finish after releasing gate: %v", ctx.Err())
	}
	after := captureExecutorGateAggregate(t, paused)
	if scenario == "control" {
		if result != nil || after.grant.ConsumedAt == nil || len(after.checkpoints) <= len(before.checkpoints) ||
			len(fixture.target.deletes) != 2 || after.job.State != string(JobStateSucceeded) {
			t.Fatalf("live grant control did not complete: err=%v grant consumed=%t checkpoints=%d/%d deletes=%d job=%s",
				result, after.grant.ConsumedAt != nil, len(after.checkpoints), len(before.checkpoints), len(fixture.target.deletes), after.job.State)
		}
		return
	}
	if scenario == "expiry" && !errors.Is(result, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("expired grant error = %v, want worker fence lost", result)
	}
	if scenario == "cancellation" && (!errors.Is(result, ErrRecoveryWorkerUnavailable) ||
		errors.Is(result, ErrRecoverySourceChanged) || errors.Is(result, ErrRecoveryWorkerFenceLost)) {
		t.Fatalf("canceled SQL error = %v, want operational worker unavailable", result)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("gate failure changed the durable recovery aggregate")
	}
	if len(fixture.target.deletes) != 0 {
		t.Fatalf("gate failure issued %d target deletes", len(fixture.target.deletes))
	}
}

func waitForExecutorRepositoryGate(t *testing.T, ctx context.Context, db *gorm.DB, holderPID int, key string, done <-chan struct{}) int {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiterPID int
		err := db.WithContext(ctx).Raw(`SELECT COALESCE((
			SELECT waiting.pid FROM pg_locks waiting
			JOIN pg_locks held ON held.locktype = waiting.locktype AND held.database = waiting.database
				AND held.classid = waiting.classid AND held.objid = waiting.objid AND held.objsubid = waiting.objsubid
			WHERE waiting.locktype = 'advisory' AND NOT waiting.granted AND waiting.mode = 'ExclusiveLock'
				AND held.granted AND held.mode = 'ExclusiveLock' AND held.pid = ?
				AND held.pid = ANY(pg_blocking_pids(waiting.pid)) AND waiting.objsubid = 1
				AND waiting.classid::bigint = ((hashtextextended(?, 0) >> 32) & 4294967295)
				AND waiting.objid::bigint = (hashtextextended(?, 0) & 4294967295)
			LIMIT 1), 0)`, holderPID, key, key).Scan(&waiterPID).Error
		if err != nil {
			t.Fatalf("inspect recovery repository gate wait: %v", err)
		}
		if waiterPID != 0 {
			return waiterPID
		}
		select {
		case <-done:
			t.Fatal("recovery operation returned before repository gate wait")
		case <-ctx.Done():
			t.Fatalf("recovery operation did not wait at repository gate: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestRecoveryExecutorRepositoryGateProjectionLeaseExpiry(t *testing.T) {
	for _, scenario := range []string{"expired", "live_control"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newInterleavedExecutionFixture(t, true)
			db := fixture.serviceFixture.db
			if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
				t.Fatalf("prepare first write: %v", err)
			}
			start := fixture.serviceFixture.now.UTC()
			leaseExpiry := start.Add(30 * time.Second)
			for _, update := range []struct {
				model any
				id    string
			}{
				{&model.BackupAssetRecoveryAttempt{}, fixture.claim.AttemptID},
				{&model.BackupAssetRecoveryNodeLease{}, fixture.claim.NodeLeaseID},
			} {
				result := db.Model(update.model).Where("id = ?", update.id).Update("lease_expires_at", leaseExpiry)
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("shorten worker lease: rows=%d err=%v", result.RowsAffected, result.Error)
				}
			}
			var job model.BackupAssetRecoveryJob
			var item model.BackupAssetRecoveryJobItem
			if err := db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationOverwrite).Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			handoff, err := fixture.coordinator.loadOrdinaryOperationHandoff(context.Background(), fixture.claim, item.ID)
			if err != nil {
				t.Fatalf("load pending write handoff: %v", err)
			}
			paused := pausedAuthorizedExactMirrorDelete{
				execution: fixture, job: job, result: RecoveryAuthorizationResult{GrantID: job.AuthorityGrantID},
			}
			before := captureExecutorGateAggregate(t, paused)
			expiredAt := leaseExpiry.Add(time.Second)
			if before.attempt.LeaseExpiresAt == nil || !before.attempt.LeaseExpiresAt.Equal(leaseExpiry) ||
				!before.node.LeaseExpiresAt.Equal(leaseExpiry) || !before.source.LeaseExpiresAt.After(expiredAt) ||
				!before.source.AbsoluteDeadline.After(expiredAt) || !before.job.PreflightExpiresAt.After(expiredAt) ||
				!before.job.AuthorityExpiresAt.After(expiredAt) {
				t.Fatal("fixture does not isolate worker lease expiry from source and job authority")
			}
			var clock atomic.Int64
			clock.Store(start.UnixNano())
			fixture.coordinator.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			holder := db.Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			var holderPID int
			if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
				_ = holder.Rollback().Error
				t.Fatal(err)
			}
			key := "xirang:recovery-authority-repository:v1:" + before.plan.RepositoryID
			if err := holder.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
				_ = holder.Rollback().Error
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			type projectionResult struct {
				evidence model.BackupAssetRecoveryEvidence
				err      error
			}
			results := make(chan projectionResult, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				evidence, err := fixture.coordinator.projectPendingOperationUnresolvedOwned(
					ctx, fixture.claim, handoff, job.TargetChainRevision,
					ordinaryOperationResult{writeCallFailed: true, unresolvedCategory: UnresolvedOperationWriteResultInvalid},
					SourceRevalidationMatched, start, false,
				)
				results <- projectionResult{evidence: evidence, err: err}
			}()
			defer func() {
				cancel()
				_ = holder.Rollback().Error
				<-done
			}()
			waitForExecutorRepositoryGate(t, ctx, db, holderPID, key, done)
			if scenario == "expired" {
				clock.Store(expiredAt.UnixNano())
			}
			if err := holder.Rollback().Error; err != nil {
				t.Fatalf("release projection repository gate: %v", err)
			}
			var result projectionResult
			select {
			case result = <-results:
			case <-ctx.Done():
				t.Fatalf("projection did not finish: %v", ctx.Err())
			}
			after := captureExecutorGateAggregate(t, paused)
			if scenario == "live_control" {
				if result.err != nil || result.evidence.ID == "" || len(after.evidence) != len(before.evidence)+1 ||
					len(after.checkpoints) != len(before.checkpoints)+1 || after.job.State != string(JobStateNeedsAttention) {
					t.Fatalf("live projection did not commit: err=%v evidence=%d/%d checkpoints=%d/%d job=%s",
						result.err, len(after.evidence), len(before.evidence), len(after.checkpoints), len(before.checkpoints), after.job.State)
				}
				return
			}
			if !errors.Is(result.err, ErrRecoveryWorkerFenceLost) || result.evidence.ID != "" {
				t.Fatalf("expired worker projection error=%v evidence=%q, want fence lost and no evidence", result.err, result.evidence.ID)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("expired worker lease changed the durable recovery aggregate")
			}
		})
	}
}
