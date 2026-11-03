package recovery

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestRecoveryPristinePrewriteFailureSQLite(t *testing.T) {
	testRecoveryPristinePrewriteFailure(t, false)
}

func TestRecoveryPristinePrewriteFailurePostgres(t *testing.T) {
	testRecoveryPristinePrewriteFailure(t, true)
}

func newPristinePrewriteMigrationFixture(t *testing.T, postgres bool) (*authorizationReceiptServiceFixture, *WorkerCoordinator, RecoveryWorkerClaim, *recoveryExecutionTargetFake) {
	t.Helper()
	options := authorizationReceiptMigrationOptions{rsync: true}
	var fixture *authorizationReceiptServiceFixture
	if postgres {
		fixture = newAuthorizationReceiptPostgresMigrationFixtureWithOptions(t, AuthorizationReceiptExecute, false, true, 0, nil, options)
	} else {
		fixture = newAuthorizationReceiptSQLiteMigrationFixtureWithOptions(t, AuthorizationReceiptExecute, false, true, 0, nil, options)
	}
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery plan: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "pristine-prewrite-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	return fixture, coordinator, claim, target
}

func testRecoveryPristinePrewriteFailure(t *testing.T, postgres bool) {
	t.Run("builder_error", func(t *testing.T) {
		fixture, coordinator, claim, target := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		var item model.BackupAssetRecoveryPlanItem
		if err := fixture.db.Where("plan_id = ?", before.job.PlanID).Take(&item).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", item.CatalogGenerationID, item.EntryID).
			Update("size", int64(17)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.BuildRsyncRestoreRequest(context.Background(), claim); !errors.Is(err, ErrRecoverySourceChanged) {
			t.Fatalf("builder source mismatch error=%v", err)
		}
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); err != nil {
			t.Fatalf("close pre-dispatch failure: %v", err)
		}
		assertRecoveryFailedPristineState(t, fixture.db, claim, before)
		if len(target.workspaceCalls)+len(target.writes)+len(target.deletes)+len(target.verifies) != 0 {
			t.Fatal("builder failure reached target")
		}
		api, err := NewAPIService(APIServiceDependencies{DB: fixture.db, Now: func() time.Time { return fixture.now }})
		if err != nil {
			t.Fatal(err)
		}
		view, err := api.GetJob(context.Background(), fixture.request.RequesterID, claim.JobID)
		if err != nil || view.State != JobStateFailed || view.FailureCategory != RecoveryPublicFailurePreWriteFailed ||
			view.Progress.CompletedItems != 0 {
			t.Fatalf("public failed job=%+v error=%v", view, err)
		}
		rollback := errors.New("rollback unknown-category probe")
		err = fixture.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", claim.JobID).
				Update("failure_category", "unknown_failure").Error; err != nil {
				return err
			}
			txAPI, err := NewAPIService(APIServiceDependencies{DB: tx, Now: func() time.Time { return fixture.now }})
			if err != nil {
				return err
			}
			if _, err := txAPI.GetJob(context.Background(), fixture.request.RequesterID, claim.JobID); !errors.Is(err, ErrRecoveryAPIUnavailable) {
				t.Errorf("unknown failure category was exposed: %v", err)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("unknown-category probe did not roll back: %v", err)
		}
	})

	t.Run("materialization_error", func(t *testing.T) {
		fixture, coordinator, claim, target := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		source.materializeErr = errors.New("private credential and /private/source must remain redacted")
		err := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if !errors.Is(err, ErrRecoveryWorkerUnavailable) || strings.Contains(err.Error(), "/private/source") || strings.Contains(err.Error(), "credential") {
			t.Fatalf("unsafe or missing materialization error: %v", err)
		}
		assertRecoveryFailedBeforeFirstWrite(t, fixture.db, claim, before, target, source)
	})

	t.Run("wrapped_cancellation_redacted", func(t *testing.T) {
		fixture, coordinator, claim, target := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		source.materializeErr = errors.Join(context.Canceled, errors.New("private /hidden/source"))
		err := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "/hidden/source") {
			t.Fatalf("wrapped cancellation leaked private data: %v", err)
		}
		assertRecoveryFailedBeforeFirstWrite(t, fixture.db, claim, before, target, source)
	})

	t.Run("snapshot_error", func(t *testing.T) {
		fixture, coordinator, claim, target := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		callback := "test:pristine-snapshot-error:" + t.Name()
		if err := fixture.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (model.CatalogEntry{}).TableName() {
				_ = tx.AddError(errors.New("private snapshot database failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		err := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if removeErr := fixture.db.Callback().Query().Remove(callback); removeErr != nil {
			t.Fatal(removeErr)
		}
		if !errors.Is(err, ErrRecoveryWorkerUnavailable) || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe snapshot failure: %v", err)
		}
		assertRecoveryFailedBeforeFirstWrite(t, fixture.db, claim, before, target, source)
	})

	t.Run("source_revalidation_error", func(t *testing.T) {
		fixture, coordinator, claim, target := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		source.revalidate = func(int) error { return errors.New("private source revalidation failure") }
		err := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if !errors.Is(err, ErrRecoveryWorkerUnavailable) || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe source failure: %v", err)
		}
		assertRecoveryFailedBeforeFirstWrite(t, fixture.db, claim, before, target, source)
	})

	t.Run("canceled_entry", func(t *testing.T) {
		fixture, coordinator, claim, target := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := coordinator.ExecuteClaim(ctx, claim, source, ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-canceled execution error=%v", err)
		}
		assertRecoveryFailedBeforeFirstWrite(t, fixture.db, claim, before, target, source)
	})

	for _, tc := range []struct {
		name    string
		context func() context.Context
		expire  bool
	}{
		{name: "active", context: context.Background},
		{name: "canceled_context", context: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }},
		{name: "expired_exact_owner", context: context.Background, expire: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
			before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
			if tc.expire {
				fixture.advanceTo(claim.AbsoluteDeadline)
			}
			if err := coordinator.FailPristinePrewrite(tc.context(), claim); err != nil {
				t.Fatalf("close pristine failure: %v", err)
			}
			assertRecoveryFailedPristineState(t, fixture.db, claim, before)
		})
	}

	t.Run("unconsumed_grant_revoked", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		unused := model.BackupAssetRecoveryGrant{
			ID: strings.Repeat("9", 32), PlanID: before.job.PlanID, AuthorityCategory: string(AuthorityWrite),
			GrantHash: strings.Repeat("a", 64), ActorUserID: fixture.request.RequesterID,
			ActorSessionID: strings.Repeat("b", 64), BindingDigest: strings.Repeat("c", 64),
			EncryptedReason: "FAKE_UNUSED_GRANT_REASON", ExpiresAt: fixture.now.Add(time.Minute),
			CreatedAt: fixture.now, UpdatedAt: fixture.now,
		}
		if err := fixture.db.Create(&unused).Error; err != nil {
			t.Fatalf("create unused grant: %v", err)
		}
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); err != nil {
			t.Fatalf("close failure and revoke unused grant: %v", err)
		}
		assertRecoveryFailedPristineState(t, fixture.db, claim, before)
		if err := fixture.db.Where("id = ?", unused.ID).Take(&unused).Error; err != nil {
			t.Fatal(err)
		}
		if unused.RevokedAt == nil || unused.ConsumedAt != nil {
			t.Fatalf("unused grant was not revoked: %+v", unused)
		}
	})

	t.Run("lease_expiry_equality", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		fixture.advanceTo(claim.LeaseExpiresAt)
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); err != nil {
			t.Fatalf("close exact expired owner: %v", err)
		}
		assertRecoveryFailedPristineState(t, fixture.db, claim, before)
	})

	t.Run("concurrent_takeover", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		fixture.advanceTo(claim.LeaseExpiresAt.Add(time.Second))
		type takeoverResult struct {
			claim RecoveryWorkerClaim
			found bool
			err   error
		}
		start := make(chan struct{})
		takeoverResults, failureResults := make(chan takeoverResult, 1), make(chan error, 1)
		go func() {
			<-start
			taken, found, err := coordinator.TakeoverExpired(context.Background(), "concurrent-pristine-takeover")
			takeoverResults <- takeoverResult{claim: taken, found: found, err: err}
		}()
		go func() {
			<-start
			failureResults <- coordinator.FailPristinePrewrite(context.Background(), claim)
		}()
		close(start)
		taken, failureErr := <-takeoverResults, <-failureResults
		if taken.found && failureErr == nil {
			t.Fatal("takeover and old owner failure both committed")
		}
		if !taken.found && failureErr != nil {
			t.Fatalf("neither contender committed: takeover=%v failure=%v", taken.err, failureErr)
		}
		if failureErr == nil {
			assertRecoveryFailedPristineState(t, fixture.db, claim, before)
		} else if taken.found {
			var active model.BackupAssetRecoveryAttempt
			if err := fixture.db.Where("id = ?", taken.claim.AttemptID).Take(&active).Error; err != nil {
				t.Fatal(err)
			}
			if active.State != string(AttemptStateRunning) || active.OwnerID != taken.claim.WorkerID {
				t.Fatalf("takeover owner was changed by failed closure: %+v", active)
			}
		}
	})

	t.Run("stale_revision", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		stale := claim
		stale.TransitionRevision++
		if err := coordinator.FailPristinePrewrite(context.Background(), stale); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("stale claim closure error=%v", err)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("stale claim changed durable state: before=%+v after=%+v", before, after)
		}
	})
	t.Run("stale_source_fence", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		stale := claim
		stale.SourceFence.FenceToken = claim.SourceFence.FenceToken + "x"
		if err := coordinator.FailPristinePrewrite(context.Background(), stale); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("stale source fence closure error=%v", err)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("stale source changed durable state: before=%+v after=%+v", before, after)
		}
		assertPristineLeaseStates(t, fixture.db, claim, string(backupasset.LeaseActive), "active")
	})

	t.Run("revision_limit", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", claim.JobID).
			Update("transition_revision", uint64(math.MaxInt64)).Error; err != nil {
			t.Fatal(err)
		}
		claim.TransitionRevision = math.MaxInt64
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("overflowing revision closure error=%v", err)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("revision limit changed durable state: before=%+v after=%+v", before, after)
		}
	})

	t.Run("concurrent_arm", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		start := make(chan struct{})
		armResult, failResult := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := coordinator.PrepareFirstWrite(context.Background(), claim)
			armResult <- err
		}()
		go func() {
			<-start
			failResult <- coordinator.FailPristinePrewrite(context.Background(), claim)
		}()
		close(start)
		armErr, failErr := <-armResult, <-failResult
		if armErr == nil && failErr == nil {
			t.Fatal("arm and pristine failure both committed")
		}
		if armErr != nil && failErr != nil {
			t.Fatalf("neither arm nor failure committed: arm=%v failure=%v", armErr, failErr)
		}
		after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID)
		if after.attempt.MutationArmed && after.job.FailureCategory == recoveryPreWriteFailedCategory {
			t.Fatalf("armed attempt was closed as pristine: job=%+v attempt=%+v", after.job, after.attempt)
		}
		if failErr == nil {
			assertRecoveryFailedPristineState(t, fixture.db, claim, before)
		}
	})

	t.Run("armed_attempt", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
			t.Fatalf("arm control attempt: %v", err)
		}
		before := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID)
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("armed closure error=%v", err)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("armed closure changed durable state: before=%+v after=%+v", before, after)
		}
	})

	t.Run("takeover_fences_old_owner", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		fixture.advanceTo(claim.LeaseExpiresAt.Add(time.Second))
		takeover, found, err := coordinator.TakeoverExpired(context.Background(), "pristine-takeover-worker")
		if err != nil || !found || takeover.AttemptID == claim.AttemptID {
			t.Fatalf("takeover found=%t err=%v", found, err)
		}
		before := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, takeover.JobID, takeover.AttemptID)
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("old owner closed takeover: %v", err)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, takeover.JobID, takeover.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("old owner changed takeover: before=%+v after=%+v", before, after)
		}
		if err := coordinator.FailPristinePrewrite(context.Background(), takeover); err != nil {
			t.Fatalf("close unarmed takeover: %v", err)
		}
		assertRecoveryFailedPristineState(t, fixture.db, takeover, before)
	})

	t.Run("rollback", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		callback := "test:pristine-prewrite-rollback:" + t.Name()
		if err := fixture.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
				_ = tx.AddError(errors.New("controlled terminal update failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callback) })
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerUnavailable) {
			t.Fatalf("rollback closure error=%v", err)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("failed transaction changed job or attempt: before=%+v after=%+v", before, after)
		}
		assertPristineLeaseStates(t, fixture.db, claim, string(backupasset.LeaseActive), "active")
	})

	t.Run("lease_release_rollback", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		callback := "test:pristine-node-release-rollback:" + t.Name()
		if err := fixture.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryNodeLease{}).TableName() {
				_ = tx.AddError(errors.New("controlled node release failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerUnavailable) {
			t.Fatalf("node release rollback error=%v", err)
		}
		if removeErr := fixture.db.Callback().Update().Remove(callback); removeErr != nil {
			t.Fatal(removeErr)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("node release failure changed job or attempt: before=%+v after=%+v", before, after)
		}
		assertPristineLeaseStates(t, fixture.db, claim, string(backupasset.LeaseActive), "active")
	})

	t.Run("clock_rollback", func(t *testing.T) {
		fixture, coordinator, claim, _ := newPristinePrewriteMigrationFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
		fixture.advanceTo(before.job.UpdatedAt.Add(-time.Second))
		if err := coordinator.FailPristinePrewrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("clock rollback closure error=%v", err)
		}
		if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.db, claim.JobID, claim.AttemptID); !reflect.DeepEqual(before, after) {
			t.Fatalf("clock rollback changed job or attempt: before=%+v after=%+v", before, after)
		}
	})
}

func assertRecoveryFailedPristineState(t *testing.T, db *gorm.DB, claim RecoveryWorkerClaim, before recoveryBeforeFirstWriteProjection) {
	t.Helper()
	after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, db, claim.JobID, claim.AttemptID)
	var plan model.BackupAssetRecoveryPlan
	if err := db.Where("id = ?", before.job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var consumed model.BackupAssetRecoveryGrant
	if err := db.Where("id = ?", before.job.AuthorityGrantID).Take(&consumed).Error; err != nil {
		t.Fatal(err)
	}
	var checkpointCount int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).Where("job_id = ?", claim.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if after.job.State != string(JobStateFailed) || after.job.FailureCategory != recoveryPreWriteFailedCategory ||
		after.job.TransitionRevision != before.job.TransitionRevision+1 || after.job.TargetChainRevision != before.job.TargetChainRevision ||
		after.job.WorkspacePhase != before.job.WorkspacePhase || after.job.WorkspaceBindingDigest != before.job.WorkspaceBindingDigest ||
		after.attempt.State != string(AttemptStateFailed) || after.attempt.MutationArmed || after.attempt.ClosedAt == nil ||
		checkpointCount != 0 || after.checkpoints != 0 || after.terminalItems != 0 || after.latchRows != before.latchRows ||
		PlanState(plan.State) != PlanStateExecuted || consumed.ConsumedAt == nil || consumed.RevokedAt != nil {
		t.Fatalf("invalid pristine failure product: before=%+v after=%+v plan=%q grant=%+v", before, after, plan.State, consumed)
	}
	assertPristineLeaseStates(t, db, claim, string(backupasset.LeaseReleased), "released")
}

func assertPristineLeaseStates(t *testing.T, db *gorm.DB, claim RecoveryWorkerClaim, sourceState, nodeState string) {
	t.Helper()
	var source model.RecoveryPointLease
	var node model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", claim.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	if recoverySourceFence(source) != claim.SourceFence || source.Status != sourceState ||
		node.ID != claim.NodeLeaseID || node.AttemptID == nil || *node.AttemptID != claim.AttemptID ||
		node.OwnerID != claim.WorkerID || node.Fence != claim.NodeFence || node.State != nodeState ||
		(sourceState == string(backupasset.LeaseReleased)) != (source.ReleasedAt != nil) ||
		(nodeState == "released") != (node.ReleasedAt != nil) {
		t.Fatalf("source/node lease state source=%+v node=%+v", source, node)
	}
}

func assertRecoveryFailedBeforeFirstWrite(t *testing.T, db *gorm.DB, claim RecoveryWorkerClaim, before recoveryBeforeFirstWriteProjection, target *recoveryExecutionTargetFake, source *recoveryRepositoryContractSource) {
	t.Helper()
	assertRecoveryFailedPristineState(t, db, claim, before)
	if len(target.workspaceCalls)+len(target.writes)+len(target.deletes)+len(target.verifies) != 0 ||
		len(source.opened) != 0 || source.closes != 1 {
		t.Fatalf("prewrite failure crossed target or source stream boundary: workspace=%d writes=%d deletes=%d verifies=%d streams=%d closes=%d",
			len(target.workspaceCalls), len(target.writes), len(target.deletes), len(target.verifies), len(source.opened), source.closes)
	}
}
