package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sealedPublicationMigrationFixture struct {
	execution exactMirrorOrdinaryExecutionFixture
	service   *ResultLifecycleService
	job       model.BackupAssetRecoveryJob
}

// The completed checkpoint fixture has passed the production migration guards.
// Complete through the worker's real terminal transition, rather than assigning
// a sealed state directly in a migration-backed test.
func newSealedPublicationMigrationFixture(t *testing.T, postgres bool) *sealedPublicationMigrationFixture {
	t.Helper()
	execution, _, _ := newCompletedIsolatedRecoveryAuthorityFixture(t, postgres)
	base := execution.serviceFixture
	var running model.BackupAssetRecoveryJob
	if err := base.db.Where("id = ?", execution.jobID).Take(&running).Error; err != nil {
		t.Fatal(err)
	}
	if err := base.db.Transaction(func(tx *gorm.DB) error {
		return execution.coordinator.completeOrdinaryRecoveryJobTx(
			context.Background(), tx, execution.claim, running, running.TargetChainRevision, base.now,
		)
	}); err != nil {
		t.Fatalf("complete isolated job through worker transaction: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := base.db.Where("id = ?", execution.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateSucceeded) || job.WorkspacePhase != string(WorkspacePhaseSealed) {
		t.Fatalf("worker did not produce a sealed terminal job: state=%s phase=%s", job.State, job.WorkspacePhase)
	}
	keys, ok := base.dependencies.LocatorKeys.(*authorizationReceiptLocatorKeys)
	if !ok {
		t.Fatalf("unexpected locator key source %T", base.dependencies.LocatorKeys)
	}
	service, err := NewResultLifecycleService(ResultLifecycleDependencies{
		DB: base.db, Now: func() time.Time { return base.now },
		WorkspaceKeys:       recoveryResultTestKeySource{material: keys.material},
		DefaultPlaintextTTL: recoveryResultTestDefaultTTL, RetainHardCap: recoveryResultTestHardCap,
		NodeAdmission:    &recoveryResultNodeAdmission{now: func() time.Time { return base.now }},
		ContentLifecycle: &recoveryResultContentLifecycleFake{db: base.db},
		Target:           &recoveryCleanupValidationTargetFake{db: base.db, now: func() time.Time { return base.now }},
		CleanupLeaseTTL:  recoveryResultCleanupTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &sealedPublicationMigrationFixture{execution: execution, service: service, job: job}
}

// Add an independent authorized job to the existing migration-backed database.
// Its checkpoint follows the same guarded persisted-history seam used by
// newCompletedIsolatedRecoveryAuthorityFixture, then the worker seals it.
func addSealedPublicationJob(t *testing.T, fixture *sealedPublicationMigrationFixture, sequence int) model.BackupAssetRecoveryJob {
	t.Helper()
	base := fixture.execution.serviceFixture
	base.advanceTo(base.now.Add(time.Second))
	db := base.db
	newID := func() string {
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	var plan model.BackupAssetRecoveryPlan
	if err := db.Where("id = ?", fixture.job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var preflight model.BackupAssetRecoveryPreflight
	if err := db.Where("id = ?", fixture.job.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	var planItems []model.BackupAssetRecoveryPlanItem
	if err := db.Where("plan_id = ?", plan.ID).Order("ordinal ASC").Find(&planItems).Error; err != nil {
		t.Fatal(err)
	}
	plan.ID = newID()
	plan.IdempotencyKeyDigest = framedDigest(planIdempotencyDigestDomain, plan.ID)
	plan.State, plan.TransitionRevision = string(PlanStatePreflightReady), 1
	plan.CreatedAt, plan.UpdatedAt = base.now, base.now
	if err := db.Create(&plan).Error; err != nil {
		t.Fatalf("create independent scan plan: %v", err)
	}
	for _, sourceItem := range planItems {
		item := sourceItem
		item.ID, item.PlanID, item.CreatedAt = newID(), plan.ID, base.now
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create independent scan selection: %v", err)
		}
	}
	preflight.ID, preflight.PlanID, preflight.CreatedAt = newID(), plan.ID, base.now
	if err := db.Create(&preflight).Error; err != nil {
		t.Fatalf("create independent scan preflight: %v", err)
	}
	write := base.newRequest(AuthorizationReceiptWriteAuthorize, plan, preflight)
	write.IdempotencyKey = fmt.Sprintf("sealed-scan-write-%d", sequence)
	write.Proof.JTI = fmt.Sprintf("FAKE_SEALED_SCAN_WRITE_PROOF_%d", sequence)
	authorized, err := base.service.Authorize(context.Background(), write)
	if err != nil {
		t.Fatalf("authorize independent scan plan: %v", err)
	}
	execute := write
	execute.Operation, execute.Category, execute.Endpoint = AuthorizationReceiptExecute, AuthorizationReceiptCategoryExecute, recoveryExecuteEndpoint
	execute.Reason, execute.IdempotencyKey = "", fmt.Sprintf("sealed-scan-execute-%d", sequence)
	execute.Proof.JTI = fmt.Sprintf("FAKE_SEALED_SCAN_EXECUTE_PROOF_%d", sequence)
	execute.ExpectedPlanRevision, execute.GrantID = authorized.PlanTransitionRevision, authorized.GrantID
	executed, err := base.service.Authorize(context.Background(), execute)
	if err != nil {
		t.Fatalf("execute independent scan plan: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, base)
	coordinator.target = &recoveryExecutionTargetFake{db: db, now: func() time.Time { return base.now }}
	claim, found, err := coordinator.ClaimNext(context.Background(), "sealed-scan-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim independent scan job: found=%t error=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("arm independent scan job: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	var item model.BackupAssetRecoveryJobItem
	if err := db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", claim.JobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	advance := recoveryTargetChainAdvanceForTest(t, job, item, claim)
	nextRevision, err := advance.NextRevision()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := model.BackupAssetRecoveryCheckpoint{
		ID: newID(), JobID: job.ID, JobItemID: item.ID, AttemptID: claim.AttemptID,
		Sequence: 1, Phase: string(CheckpointPhaseOperation), AuthorityCategory: string(AuthorityWrite),
		OperationDigest: recoveryJobItemOperationDigest(item), PriorTargetRevision: job.TargetChainRevision,
		NextTargetRevision: nextRevision, NodeFence: claim.NodeFence, AttemptFence: claim.AttemptFence,
		PlanBindingDigest: job.PlanBindingDigest, SourceRevisionDigest: job.SourceRevisionDigest,
		PreflightID: job.PreflightID, PreflightRevision: job.PreflightRevision, PreflightExpiresAt: job.PreflightExpiresAt,
		SecurityDecision: job.SecurityDecision, SecurityDecisionDigest: job.SecurityDecisionDigest,
		SecurityFindingSetDigest: job.SecurityFindingSetDigest, SecurityPolicyRevision: job.SecurityPolicyRevision,
		AuthorityGrantID: job.AuthorityGrantID, JobAuthorityCategory: job.AuthorityCategory,
		AuthorityBindingDigest: job.AuthorityBindingDigest, AuthorityExpiresAt: job.AuthorityExpiresAt, CreatedAt: base.now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&checkpoint).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.BackupAssetRecoveryJobItem{}).Where("id = ? AND outcome = ''", item.ID).
			Updates(map[string]any{"outcome": "succeeded", "bytes_written": item.ExpectedPostBytes,
				"verified_size": item.ExpectedPostBytes, "verified_digest": item.ExpectedPostIdentityDigest, "updated_at": base.now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetRecoveryJob{}).Where("id = ? AND state = ?", job.ID, JobStateRunning).
			Updates(map[string]any{"target_chain_revision": nextRevision, "workspace_phase": WorkspacePhaseWriting, "updated_at": base.now}).Error
	}); err != nil {
		t.Fatalf("persist guarded independent scan checkpoint: %v", err)
	}
	if err := db.Where("id = ?", job.ID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return coordinator.completeOrdinaryRecoveryJobTx(context.Background(), tx, claim, job, nextRevision, base.now)
	}); err != nil {
		t.Fatalf("seal independent scan job: %v", err)
	}
	if err := db.Where("id = ?", job.ID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateSucceeded) || job.WorkspacePhase != string(WorkspacePhaseSealed) {
		t.Fatalf("independent scan job did not seal: state=%s phase=%s", job.State, job.WorkspacePhase)
	}
	return job
}

type failFirstSealedPublicationKeySource struct {
	RecoveryWorkspaceKeySource
	remaining int
}

func (source *failFirstSealedPublicationKeySource) ByVersion(ctx context.Context, domain backupasset.KeyDomain, version int) (backupasset.DomainKeyMaterial, error) {
	if source.remaining > 0 {
		source.remaining--
		return backupasset.DomainKeyMaterial{}, errors.New("test: sealed candidate key unavailable")
	}
	return source.RecoveryWorkspaceKeySource.ByVersion(ctx, domain, version)
}

func TestRecoverySealedPublicationLifecycleSQLite(t *testing.T) {
	testRecoverySealedPublicationLifecycle(t, false)
}

func TestRecoverySealedPublicationLifecyclePostgres(t *testing.T) {
	testRecoverySealedPublicationLifecycle(t, true)
}

func testRecoverySealedPublicationLifecycle(t *testing.T, postgres bool) {
	t.Run("PublishAndRestart", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		// A restarted owner starts with no cursor and discovers the durable seal.
		restarted := *fixture.service
		cursor, err := restarted.PublishSealedBatch(context.Background(), RecoverySealedResultCursor{}, 10)
		if err != nil || cursor != (RecoverySealedResultCursor{}) {
			t.Fatalf("publish batch cursor=%+v error=%v", cursor, err)
		}
		published, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil || len(published.Results) != 1 || published.JobRevision != fixture.job.TransitionRevision+1 ||
			published.Results[0].Classification != (RecoveryResultClassificationBinding{
				Kind: RecoveryResultClassificationUnknown, Revision: 1, SourceRevision: 1,
			}) {
			t.Fatalf("replayed publication=%+v error=%v", published, err)
		}
		// Replaying the durable publication preserves its IDs and deadline.
		again, err := restarted.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil || again.ResultSetID != published.ResultSetID || again.Results[0].ID != published.Results[0].ID ||
			!again.PlaintextDeadline.Equal(*fixture.job.PlaintextDeadline) {
			t.Fatalf("restart changed the publication: %+v error=%v", again, err)
		}
		resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
			DB: fixture.execution.serviceFixture.db, Now: func() time.Time { return fixture.execution.serviceFixture.now },
		})
		if err != nil {
			t.Fatal(err)
		}
		request := ResolveRecoveryResultRequest{RequesterID: fixture.execution.serviceFixture.request.RequesterID,
			RecoveryJobID: fixture.job.ID, ResultID: published.Results[0].ID}
		if _, err := resolver.Resolve(context.Background(), request); err != nil {
			t.Fatalf("unexpired published result was not resolvable: %v", err)
		}
		fixture.execution.serviceFixture.advanceTo(*fixture.job.PlaintextDeadline)
		again, err = restarted.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil || again.ResultSetID != published.ResultSetID || !again.PlaintextDeadline.Equal(*fixture.job.PlaintextDeadline) {
			t.Fatalf("expired durable publication was not identified without extending it: %+v error=%v", again, err)
		}
		if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrRecoveryResultUnavailable) {
			t.Fatalf("expired result remained deliverable: %v", err)
		}
		var count int64
		if err := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
			Where("job_id = ?", fixture.job.ID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("restart created %d result sets: %v", count, err)
		}
	})

	t.Run("ConcurrentPublishers", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		start := make(chan struct{})
		type outcome struct {
			set PublishedRecoveryResultSet
			err error
		}
		results := make(chan outcome, 2)
		var group sync.WaitGroup
		for range 2 {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				set, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
				results <- outcome{set: set, err: err}
			}()
		}
		close(start)
		group.Wait()
		first, second := <-results, <-results
		if first.err != nil || second.err != nil || first.set.ResultSetID == "" ||
			first.set.ResultSetID != second.set.ResultSetID || len(first.set.Results) != 1 || len(second.set.Results) != 1 ||
			first.set.Results[0].ID != second.set.Results[0].ID {
			t.Fatalf("concurrent publication did not converge: first=%+v second=%+v", first, second)
		}
	})

	t.Run("LostCommitAcknowledgement", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		var armed atomic.Bool
		armed.Store(true)
		wrapped := fixture.execution.serviceFixture.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
		wrapped.Statement.ConnPool = sealedPublicationCommitPool{ConnPool: wrapped.Statement.ConnPool, armed: &armed}
		fixture.service.db = wrapped
		published, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil || armed.Load() || published.ResultSetID == "" || len(published.Results) != 1 {
			t.Fatalf("committed publication was not confirmed by complete readback: %+v error=%v", published, err)
		}
		var count int64
		if err := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryResult{}).
			Where("job_id = ?", fixture.job.ID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("lost acknowledgement persisted %d result rows: %v", count, err)
		}
	})

	t.Run("ReadbackRejectsCorruption", func(t *testing.T) {
		for _, corruption := range []string{"missing", "extra", "digest"} {
			t.Run(corruption, func(t *testing.T) {
				fixture := newSealedPublicationMigrationFixture(t, postgres)
				published, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
				if err != nil || len(published.Results) != 1 {
					t.Fatalf("publish fixture: %+v error=%v", published, err)
				}
				db := fixture.execution.serviceFixture.db
				var row model.BackupAssetRecoveryResult
				if err := db.Where("id = ?", published.Results[0].ID).Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				switch corruption {
				case "missing":
					err = db.Where("id = ?", row.ID).Delete(&model.BackupAssetRecoveryResult{}).Error
				case "extra":
					row.ID = strings.Repeat("e", 32)
					row.LocatorDigest = recoveryResultLocatorDigest(row.JobID, row.ResultSetID, row.ID, row.EncryptedRelativeLocator)
					err = db.Create(&row).Error
				case "digest":
					err = db.Model(&model.BackupAssetRecoveryResult{}).Where("id = ?", row.ID).
						UpdateColumn("locator_digest", strings.Repeat("f", 64)).Error
				}
				if err != nil {
					t.Fatalf("create permitted corruption fixture: %v", err)
				}
				if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
					t.Fatal("readback accepted contradictory result rows")
				}
			})
		}
	})

	t.Run("ActiveAttemptMigrationGuard", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		db := fixture.execution.serviceFixture.db
		var attempt model.BackupAssetRecoveryAttempt
		if err := db.Where("id = ?", fixture.execution.claim.AttemptID).Take(&attempt).Error; err != nil {
			t.Fatal(err)
		}
		attempt.ID, attempt.Fence = strings.Repeat("e", 32), attempt.Fence+1
		attempt.State, attempt.ClosedAt = string(AttemptStateRunning), nil
		if err := db.Create(&attempt).Error; err == nil {
			t.Fatal("migration admitted an active attempt on a terminal sealed job")
		}
		if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err != nil {
			t.Fatalf("rejected active attempt prevented legal publication: %v", err)
		}
	})

	t.Run("CanceledCommitReadback", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var armed atomic.Bool
		armed.Store(true)
		wrapped := fixture.execution.serviceFixture.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
		wrapped.Statement.ConnPool = sealedPublicationCommitPool{ConnPool: wrapped.Statement.ConnPool, armed: &armed, cancel: cancel}
		fixture.service.db = wrapped
		if _, err := fixture.service.publishTerminalSealed(ctx, fixture.job.ID, fixture.job.TransitionRevision); err == nil || ctx.Err() == nil {
			t.Fatalf("canceled caller received a confirmed publication: %v", err)
		}
		published, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil || published.ResultSetID == "" || len(published.Results) != 1 {
			t.Fatalf("fresh caller could not reconcile committed publication: %+v error=%v", published, err)
		}
	})

	t.Run("PublishExpiryRace", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		publisher, expirer := *fixture.service, *fixture.service
		before := fixture.execution.serviceFixture.now
		deadline := *fixture.job.PlaintextDeadline
		publisher.now = func() time.Time { return before }
		expirer.now = func() time.Time { return deadline }
		start := make(chan struct{})
		published := make(chan error, 1)
		expired := make(chan error, 1)
		go func() {
			<-start
			_, err := publisher.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
			published <- err
		}()
		go func() {
			<-start
			expired <- expirer.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		}()
		close(start)
		publishErr, expiryErr := <-published, <-expired
		if (publishErr == nil) == (expiryErr == nil) {
			t.Fatalf("publication and expiry did not choose exactly one winner: publish=%v expiry=%v", publishErr, expiryErr)
		}
		var job model.BackupAssetRecoveryJob
		if err := fixture.execution.serviceFixture.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
			Where("job_id = ?", job.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if job.TransitionRevision != fixture.job.TransitionRevision+1 ||
			(publishErr == nil && (job.WorkspacePhase != string(WorkspacePhasePublished) || count != 1)) ||
			(expiryErr == nil && (job.WorkspacePhase != string(WorkspacePhaseCleanupDue) || count != 0)) {
			t.Fatalf("race left inconsistent authority: phase=%s revision=%d sets=%d", job.WorkspacePhase, job.TransitionRevision, count)
		}
	})

	t.Run("ExpireAtDeadline", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		fixture.execution.serviceFixture.advanceTo(*fixture.job.PlaintextDeadline)
		cursor, err := fixture.service.ExpireSealedBatch(context.Background(), RecoverySealedResultCursor{}, 10)
		if err != nil || cursor != (RecoverySealedResultCursor{}) {
			t.Fatalf("expire batch cursor=%+v error=%v", cursor, err)
		}
		var after model.BackupAssetRecoveryJob
		if err := fixture.execution.serviceFixture.db.Where("id = ?", fixture.job.ID).Take(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.WorkspacePhase != string(WorkspacePhaseCleanupDue) || after.TransitionRevision != fixture.job.TransitionRevision+1 ||
			after.State != fixture.job.State || after.FailureCategory != fixture.job.FailureCategory ||
			after.WorkspaceMarkerBindingDigest != fixture.job.WorkspaceMarkerBindingDigest ||
			after.EncryptedWorkspaceRelativeLocator != fixture.job.EncryptedWorkspaceRelativeLocator ||
			after.TargetChainRevision != fixture.job.TargetChainRevision || !after.PlaintextDeadline.Equal(*fixture.job.PlaintextDeadline) ||
			after.WorkspaceCleanupPhase != string(CleanupPhaseClaimed) || after.WorkspaceCleanupOwner != "" ||
			after.WorkspaceCleanupLeaseExpiresAt != nil || after.WorkspaceCleanupFence != 0 ||
			after.WorkspaceCleanupNodeLeaseID != nil || after.WorkspaceCleanupNodeFence != 0 || after.WorkspaceCleanupAttempt != 0 ||
			!validRecoveryWorkspaceCleanupCandidate(after, fixture.execution.serviceFixture.now) {
			t.Fatalf("expiry did not preserve cleanup authority: %+v", after)
		}
	})

	t.Run("FinalClockRollbackAndExpiry", func(t *testing.T) {
		for _, mode := range []string{"rollback", "equality"} {
			t.Run(mode, func(t *testing.T) {
				fixture := newSealedPublicationMigrationFixture(t, postgres)
				start := fixture.execution.serviceFixture.now
				calls := 0
				fixture.service.now = func() time.Time {
					calls++
					if calls == 3 {
						if mode == "rollback" {
							return start.Add(-time.Microsecond)
						}
						return *fixture.job.PlaintextDeadline
					}
					return start
				}
				if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
					t.Fatal("publication crossed its final clock barrier")
				}
				assertSealedPublicationAbsent(t, fixture)
			})
		}
	})

	t.Run("LockWaitDeadline", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		db := fixture.execution.serviceFixture.db
		locked := db.Begin()
		if locked.Error != nil {
			t.Fatal(locked.Error)
		}
		defer locked.Rollback()
		var job model.BackupAssetRecoveryJob
		if err := locked.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		var clock atomic.Int64
		clock.Store(fixture.execution.serviceFixture.now.UnixNano())
		fixture.service.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
		started := make(chan int, 1)
		if postgres {
			callback := "test:sealed-publication-lock-wait"
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
					var pid int
					if err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
						_ = tx.AddError(err)
					}
					select {
					case started <- pid:
					default:
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
		} else {
			// SQLite's immediate transaction waits at BEGIN before its first query.
			wrapped := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
			wrapped.Statement.ConnPool = sealedPublicationBeginSignalPool{ConnPool: wrapped.Statement.ConnPool, started: started}
			fixture.service.db = wrapped
		}
		finished := make(chan error, 1)
		go func() {
			_, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
			finished <- err
		}()
		var backendPID int
		select {
		case backendPID = <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("publisher did not start the locked job query")
		}
		if postgres {
			if backendPID <= 0 {
				t.Fatal("publisher did not expose its PostgreSQL backend")
			}
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.NewTimer(5 * time.Second)
			defer timeout.Stop()
			for {
				var blocked bool
				if err := db.Raw("SELECT cardinality(pg_blocking_pids(?)) > 0", backendPID).Scan(&blocked).Error; err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case <-ticker.C:
				case <-timeout.C:
					t.Fatal("publisher never waited for the held PostgreSQL row lock")
				}
			}
		} else {
			select {
			case err := <-finished:
				t.Fatalf("SQLite publisher returned while the write lock was held: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
		}
		clock.Store(fixture.job.PlaintextDeadline.UnixNano())
		if err := locked.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if err := <-finished; err == nil {
			t.Fatal("publication used a time sampled before acquiring the job lock")
		}
		assertSealedPublicationAbsent(t, fixture)
	})

	t.Run("StaleRevision", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision+1); err == nil {
			t.Fatal("stale publication revision succeeded")
		}
		fixture.execution.serviceFixture.advanceTo(*fixture.job.PlaintextDeadline)
		if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision+1); err == nil {
			t.Fatal("stale expiry revision succeeded")
		}
		assertSealedPublicationAbsent(t, fixture)
	})

	t.Run("PaginationContinuesAcrossArrivals", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		base := fixture.execution.serviceFixture
		clock := func() time.Time { return base.now.UTC() }
		db := base.db.Session(&gorm.Session{NewDB: true, Context: context.Background(), NowFunc: clock})
		leases, err := backupasset.NewLeaseService(db, clock, backupasset.LeaseConfig{
			Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		dependencies := base.dependencies
		dependencies.DB, dependencies.Now, dependencies.SourceLeases = db, clock, leases
		authorization, err := NewAuthorizationService(dependencies)
		if err != nil {
			t.Fatal(err)
		}
		base.db, base.dependencies, base.service = db, dependencies, authorization
		fixture.service.db, fixture.service.now = db, clock
		second := addSealedPublicationJob(t, fixture, 1)
		third := addSealedPublicationJob(t, fixture, 2)
		fourth := addSealedPublicationJob(t, fixture, 3)
		keys := &failFirstSealedPublicationKeySource{RecoveryWorkspaceKeySource: fixture.service.workspaceKeys, remaining: 1}
		fixture.service.workspaceKeys = keys
		cursor, err := fixture.service.PublishSealedBatch(context.Background(), RecoverySealedResultCursor{}, 2)
		if err == nil || keys.remaining != 0 || cursor.afterJobID != second.ID || cursor.highWaterJobID != fourth.ID {
			t.Fatalf("failed first candidate prevented same-page progress: cursor=%+v error=%v", cursor, err)
		}
		fifth := addSealedPublicationJob(t, fixture, 4)
		cursor, err = fixture.service.PublishSealedBatch(context.Background(), cursor, 1)
		if err != nil || cursor.afterJobID != third.ID || cursor.highWaterJobID != fourth.ID {
			t.Fatalf("third candidate did not advance the original window: cursor=%+v error=%v", cursor, err)
		}
		sixth := addSealedPublicationJob(t, fixture, 5)
		cursor, err = fixture.service.PublishSealedBatch(context.Background(), cursor, 1)
		if err != nil || cursor != (RecoverySealedResultCursor{}) {
			t.Fatalf("fixed window did not finish during new arrivals: cursor=%+v error=%v", cursor, err)
		}
		for _, expected := range []struct {
			job   model.BackupAssetRecoveryJob
			phase WorkspacePhase
		}{
			{fixture.job, WorkspacePhaseSealed}, {second, WorkspacePhasePublished},
			{third, WorkspacePhasePublished}, {fourth, WorkspacePhasePublished},
			{fifth, WorkspacePhaseSealed}, {sixth, WorkspacePhaseSealed},
		} {
			var actual model.BackupAssetRecoveryJob
			if err := fixture.execution.serviceFixture.db.Where("id = ?", expected.job.ID).Take(&actual).Error; err != nil {
				t.Fatal(err)
			}
			if WorkspacePhase(actual.WorkspacePhase) != expected.phase {
				t.Fatalf("candidate %s phase=%s want=%s", expected.job.ID, actual.WorkspacePhase, expected.phase)
			}
		}
	})

	t.Run("DeadlineHistory", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		base := fixture.execution.serviceFixture
		published, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil {
			t.Fatal(err)
		}
		earlier := fixture.job.PlaintextDeadline.Add(-time.Hour)
		if !earlier.After(base.now) {
			t.Fatal("deadline fixture does not isolate the original deadline")
		}
		if err := base.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
			UpdateColumn("plaintext_deadline", earlier).Error; err == nil {
			t.Fatal("production migration permitted a shortened result deadline")
		}
		requested := fixture.job.PlaintextDeadline.Add(time.Hour)
		retained, err := fixture.service.Retain(context.Background(), RetainRecoveryResultsRequest{
			JobID: fixture.job.ID, ExpectedJobRevision: published.JobRevision, RequestedDeadline: requested,
			Actor:       content.DeliveryActor{UserID: base.request.RequesterID, Role: "admin"},
			Permissions: backupasset.PermissionSet{backupasset.PermissionBackupAssetsRecover: true},
			Proof:       &content.StepUpProof{Action: auth.StepUpActionRecoveryResultRetain, ID: strings.Repeat("b", 32), ExpiresAt: base.now.Add(time.Minute)},
		})
		if err != nil || !retained.PlaintextDeadline.Equal(requested) {
			t.Fatalf("retain deadline: %+v error=%v", retained, err)
		}
		again, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil || again.ResultSetID != published.ResultSetID || !again.PlaintextDeadline.Equal(requested) ||
			!again.HardDeadline.Equal(published.HardDeadline) {
			t.Fatalf("retained published set did not reconcile: %+v error=%v", again, err)
		}
	})

	t.Run("MarkerAndCleanupShapeGuards", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		db := fixture.execution.serviceFixture.db
		wrongMarker := strings.Repeat("a", 64)
		if wrongMarker == fixture.job.WorkspaceMarkerBindingDigest {
			wrongMarker = strings.Repeat("b", 64)
		}
		if err := db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
			UpdateColumn("workspace_marker_binding_digest", wrongMarker).Error; err == nil {
			t.Fatal("migration allowed replacing a sealed workspace marker")
		}
		if err := db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
			UpdateColumn("workspace_cleanup_fence", int64(1)).Error; err == nil {
			t.Fatal("migration allowed a non-neutral cleanup tuple on a sealed workspace")
		}
		if err := db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
			UpdateColumn("state", string(JobStateRunning)).Error; err == nil {
			t.Fatal("migration allowed a terminal job to resume running")
		}
		if err := db.Model(&model.BackupAssetRecoveryJobItem{}).Where("job_id = ?", fixture.job.ID).
			Updates(map[string]any{"outcome": "failed", "failure_category": "pre_write_failed"}).Error; err == nil {
			t.Fatal("migration allowed a completed item to become failed")
		}
		if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err != nil {
			t.Fatalf("rejected illegal updates damaged the valid sealed job: %v", err)
		}
	})

	t.Run("ExpiredMarkerAuthority", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		fixture.execution.serviceFixture.advanceTo(*fixture.job.PlaintextDeadline)
		original := fixture.service.workspaceKeys
		source, ok := original.(recoveryResultTestKeySource)
		if !ok {
			t.Fatalf("unexpected key source %T", original)
		}
		wrong := cloneDomainKeyMaterial(source.material)
		wrong.Key = []byte(strings.Repeat("z", 32))
		fixture.service.workspaceKeys = recoveryResultTestKeySource{material: wrong}
		if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); !errors.Is(err, ErrRecoveryResultCleanupValidationFailed) {
			t.Fatalf("wrong marker key error=%v", err)
		}
		assertSealedPublicationAbsent(t, fixture)
		fixture.service.workspaceKeys = original
		if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err != nil {
			t.Fatalf("authenticated expired workspace did not enter cleanup: %v", err)
		}
		for _, revision := range []uint64{fixture.job.TransitionRevision, fixture.job.TransitionRevision + 1} {
			if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, revision); err == nil {
				t.Fatal("cleanup-due workspace was republished")
			}
		}
	})

	t.Run("CleanedWorkspaceRejectsPublication", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		base := fixture.execution.serviceFixture
		base.advanceTo(*fixture.job.PlaintextDeadline)
		if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err != nil {
			t.Fatal(err)
		}
		claim, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{JobID: fixture.job.ID, WorkerID: "sealed-cleanup-worker"})
		if err != nil {
			t.Fatal(err)
		}
		claim, err = fixture.service.RevokeRecoveryWorkspaceCleanup(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		claim, err = fixture.service.DrainRecoveryWorkspaceCleanup(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		claim, err = fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		target, ok := fixture.service.target.(*recoveryCleanupValidationTargetFake)
		if !ok {
			t.Fatalf("unexpected cleanup target %T", fixture.service.target)
		}
		target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/sealed-cleanup-progress/v1", fixture.job.ID)}
		progress, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), claim)
		if err != nil || progress.Phase != CleanupPhaseDeleted {
			t.Fatalf("delete workspace progress=%+v error=%v", progress, err)
		}
		var job model.BackupAssetRecoveryJob
		if err := base.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		if job.WorkspaceCleanupNodeLeaseID == nil || job.WorkspaceCleanupLeaseExpiresAt == nil {
			t.Fatal("deleted workspace lost cleanup fence")
		}
		claim = RecoveryWorkspaceCleanupClaim{JobID: job.ID, WorkerID: job.WorkspaceCleanupOwner,
			CleanupFence: job.WorkspaceCleanupFence, CleanupAttempt: job.WorkspaceCleanupAttempt,
			NodeLeaseID: *job.WorkspaceCleanupNodeLeaseID, NodeFence: job.WorkspaceCleanupNodeFence,
			LeaseExpiresAt: *job.WorkspaceCleanupLeaseExpiresAt, Phase: CleanupPhaseDeleted}
		progress, err = fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), claim)
		if err != nil || progress.Phase != CleanupPhaseTombstoned {
			t.Fatalf("finish workspace cleanup progress=%+v error=%v", progress, err)
		}
		if err := base.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		if WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseCleaned {
			t.Fatalf("workspace phase=%s", job.WorkspacePhase)
		}
		if _, err := fixture.service.publishTerminalSealed(context.Background(), job.ID, job.TransitionRevision); err == nil {
			t.Fatal("cleaned workspace was republished")
		}
	})

	t.Run("CleanedResultRejectsReadback", func(t *testing.T) {
		fixture := newSealedPublicationMigrationFixture(t, postgres)
		base := fixture.execution.serviceFixture
		published, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision)
		if err != nil {
			t.Fatal(err)
		}
		base.advanceTo(*fixture.job.PlaintextDeadline)
		claim, err := fixture.service.ClaimScheduledCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{ResultSetID: published.ResultSetID, WorkerID: "sealed-cleanup-worker"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err != nil {
			t.Fatalf("claimed cleanup lost the durable publication: %v", err)
		}
		claim, err = fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		claim, err = fixture.service.DrainRecoveryResultCleanup(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		claim, err = fixture.service.ValidateRecoveryResultCleanup(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		target, ok := fixture.service.target.(*recoveryCleanupValidationTargetFake)
		if !ok {
			t.Fatalf("unexpected cleanup target %T", fixture.service.target)
		}
		target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/sealed-result-cleanup-progress/v1", fixture.job.ID)}
		progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), claim)
		if err != nil || progress.Phase != CleanupPhaseDeleted {
			t.Fatalf("delete result progress=%+v error=%v", progress, err)
		}
		var set model.BackupAssetRecoveryResultSet
		if err := base.db.Where("id = ?", published.ResultSetID).Take(&set).Error; err != nil {
			t.Fatal(err)
		}
		if set.NodeLeaseID == nil || set.CleanupLeaseExpiresAt == nil {
			t.Fatal("deleted result lost cleanup fence")
		}
		claim = RecoveryResultCleanupClaim{ResultSetID: set.ID, JobID: set.JobID, WorkerID: set.CleanupOwner,
			CleanupFence: set.CleanupFence, CleanupAttempt: set.CleanupAttempt, NodeLeaseID: *set.NodeLeaseID,
			NodeFence: set.NodeFence, LeaseExpiresAt: *set.CleanupLeaseExpiresAt, Phase: CleanupPhaseDeleted}
		progress, err = fixture.service.AdvanceRecoveryResultCleanup(context.Background(), claim)
		if err != nil || progress.Phase != CleanupPhaseTombstoned {
			t.Fatalf("finish result cleanup progress=%+v error=%v", progress, err)
		}
		if err := base.db.Where("id = ?", set.ID).Take(&set).Error; err != nil {
			t.Fatal(err)
		}
		if ResultSetState(set.State) != ResultSetStateCleaned {
			t.Fatalf("result state=%s", set.State)
		}
		if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
			t.Fatal("cleaned result set was confirmed as publishable")
		}
	})

	t.Run("RevisionCeiling", func(t *testing.T) {
		t.Run("exhausted", func(t *testing.T) {
			fixture := newSealedPublicationMigrationFixture(t, postgres)
			if err := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
				UpdateColumn("transition_revision", int64(math.MaxInt64)).Error; err != nil {
				t.Fatal(err)
			}
			fixture.job.TransitionRevision = uint64(math.MaxInt64)
			if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
				t.Fatal("publication overflowed the database revision")
			}
			fixture.execution.serviceFixture.advanceTo(*fixture.job.PlaintextDeadline)
			if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
				t.Fatal("expiry overflowed the database revision")
			}
			assertSealedPublicationAbsent(t, fixture)
		})
		t.Run("last_publish_revision", func(t *testing.T) {
			fixture := newSealedPublicationMigrationFixture(t, postgres)
			if err := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
				UpdateColumn("transition_revision", int64(math.MaxInt64-1)).Error; err != nil {
				t.Fatal(err)
			}
			published, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, uint64(math.MaxInt64-1))
			if err != nil || published.JobRevision != uint64(math.MaxInt64) {
				t.Fatalf("last revision publication=%+v error=%v", published, err)
			}
		})
		t.Run("last_expiry_revision", func(t *testing.T) {
			fixture := newSealedPublicationMigrationFixture(t, postgres)
			if err := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
				UpdateColumn("transition_revision", int64(math.MaxInt64-1)).Error; err != nil {
				t.Fatal(err)
			}
			fixture.execution.serviceFixture.advanceTo(*fixture.job.PlaintextDeadline)
			if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, uint64(math.MaxInt64-1)); err != nil {
				t.Fatal(err)
			}
			var job model.BackupAssetRecoveryJob
			if err := fixture.execution.serviceFixture.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			if job.WorkspacePhase != string(WorkspacePhaseCleanupDue) || job.TransitionRevision != uint64(math.MaxInt64) {
				t.Fatalf("last revision expiry phase=%s revision=%d", job.WorkspacePhase, job.TransitionRevision)
			}
		})
	})
}

func assertSealedPublicationAbsent(t *testing.T, fixture *sealedPublicationMigrationFixture) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := fixture.execution.serviceFixture.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
		Where("job_id = ?", fixture.job.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if job.WorkspacePhase != string(WorkspacePhaseSealed) || job.TransitionRevision != fixture.job.TransitionRevision || count != 0 {
		t.Fatalf("publication barrier partially committed: phase=%s revision=%d sets=%d", job.WorkspacePhase, job.TransitionRevision, count)
	}
}

type sealedPublicationBeginSignalPool struct {
	gorm.ConnPool
	started chan<- int
}

func (pool sealedPublicationBeginSignalPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	select {
	case pool.started <- 0:
	default:
	}
	if beginner, ok := pool.ConnPool.(gorm.ConnPoolBeginner); ok {
		return beginner.BeginTx(ctx, options)
	}
	return pool.ConnPool.(gorm.TxBeginner).BeginTx(ctx, options)
}

type sealedPublicationCommitPool struct {
	gorm.ConnPool
	armed  *atomic.Bool
	cancel context.CancelFunc
}

func (pool sealedPublicationCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var connection gorm.ConnPool
	var err error
	if beginner, ok := pool.ConnPool.(gorm.ConnPoolBeginner); ok {
		connection, err = beginner.BeginTx(ctx, options)
	} else {
		connection, err = pool.ConnPool.(gorm.TxBeginner).BeginTx(ctx, options)
	}
	if err != nil {
		return nil, err
	}
	return &sealedPublicationCommitTx{ConnPool: connection, committer: connection.(gorm.TxCommitter), armed: pool.armed, cancel: pool.cancel}, nil
}

type sealedPublicationCommitTx struct {
	gorm.ConnPool
	committer gorm.TxCommitter
	armed     *atomic.Bool
	cancel    context.CancelFunc
}

func (tx *sealedPublicationCommitTx) Commit() error {
	if err := tx.committer.Commit(); err != nil {
		return err
	}
	if tx.armed.CompareAndSwap(true, false) {
		if tx.cancel != nil {
			tx.cancel()
		}
		return errors.New("test: committed publication acknowledgement lost")
	}
	return nil
}

func (tx *sealedPublicationCommitTx) Rollback() error { return tx.committer.Rollback() }

// AutoMigrate fixtures intentionally permit contradictory rows so that the
// application readback boundary is exercised independently of migration guards.
func TestRecoverySealedPublicationRejectsContradictoryReadback(t *testing.T) {
	for _, corruption := range []string{"missing_set", "missing_result", "extra_result", "duplicate_tuple", "locator_digest", "marker", "classification", "deadline", "deadline_before_original"} {
		t.Run(corruption, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
			if err != nil || len(published.Results) < 2 {
				t.Fatalf("publish readback fixture: %+v error=%v", published, err)
			}
			var rows []model.BackupAssetRecoveryResult
			if err := fixture.db.Where("job_id = ?", fixture.job.ID).Order("id ASC").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "missing_set":
				err = fixture.db.Where("id = ?", published.ResultSetID).Delete(&model.BackupAssetRecoveryResultSet{}).Error
			case "missing_result":
				err = fixture.db.Where("id = ?", rows[0].ID).Delete(&model.BackupAssetRecoveryResult{}).Error
			case "extra_result":
				extra := rows[0]
				extra.ID = strings.Repeat("f", 32)
				extra.LocatorDigest = recoveryResultLocatorDigest(extra.JobID, extra.ResultSetID, extra.ID, extra.EncryptedRelativeLocator)
				err = fixture.db.Create(&extra).Error
			case "duplicate_tuple":
				ciphertext, encryptErr := secure.EncryptString(rows[0].EncryptedRelativeLocator)
				if encryptErr != nil {
					t.Fatal(encryptErr)
				}
				err = fixture.db.Model(&model.BackupAssetRecoveryResult{}).Where("id = ?", rows[1].ID).Updates(map[string]any{
					"encrypted_relative_locator": ciphertext, "size": rows[0].Size, "content_digest": rows[0].ContentDigest,
					"locator_digest": recoveryResultLocatorDigest(rows[1].JobID, rows[1].ResultSetID, rows[1].ID, rows[0].EncryptedRelativeLocator),
				}).Error
			case "locator_digest":
				err = fixture.db.Model(&model.BackupAssetRecoveryResult{}).Where("id = ?", rows[0].ID).
					UpdateColumn("locator_digest", strings.Repeat("f", 64)).Error
			case "marker":
				err = fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
					UpdateColumn("marker_binding_digest", strings.Repeat("f", 64)).Error
			case "classification":
				err = fixture.db.Model(&model.BackupAssetRecoveryResult{}).Where("id = ?", rows[0].ID).
					UpdateColumn("classification_source_revision", 0).Error
			case "deadline":
				err = fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
					UpdateColumn("hard_deadline", fixture.now).Error
			case "deadline_before_original":
				// The row remains otherwise valid: after creation and before the
				// immutable hard limit, but earlier than the original deadline.
				err = fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
					UpdateColumn("plaintext_deadline", fixture.now.Add(time.Hour)).Error
			}
			if err != nil {
				t.Fatalf("inject readback contradiction: %v", err)
			}
			if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
				t.Fatal("contradictory persisted publication was accepted")
			}
		})
	}
}

func TestRecoverySealedPublicationPaginationAdvancesPastFailures(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	for index := range 2 {
		bad := fixture.job
		bad.ID = fmt.Sprintf("%032x", index+100)
		bad.PlanID = fmt.Sprintf("%032x", index+200)
		bad.CreatedAt = fixture.job.CreatedAt.Add(time.Duration(index-3) * time.Minute)
		if err := fixture.db.Create(&bad).Error; err != nil {
			t.Fatal(err)
		}
	}
	cursor := RecoverySealedResultCursor{}
	var newcomers []string
	for index := range 2 {
		next, err := fixture.service.PublishSealedBatch(context.Background(), cursor, 1)
		if err == nil || next.afterJobID == "" || next == cursor {
			t.Fatalf("failed row did not advance page %d: cursor=%+v next=%+v error=%v", index, cursor, next, err)
		}
		cursor = next
		newcomer := fixture.job
		newcomer.ID, newcomer.PlanID = fmt.Sprintf("%032x", index+300), fmt.Sprintf("%032x", index+400)
		newcomer.CreatedAt = fixture.job.CreatedAt.Add(time.Duration(index+1) * time.Second)
		if err := fixture.db.Create(&newcomer).Error; err != nil {
			t.Fatal(err)
		}
		newcomers = append(newcomers, newcomer.ID)
	}
	next, err := fixture.service.PublishSealedBatch(context.Background(), cursor, 1)
	if err != nil || next != (RecoverySealedResultCursor{}) {
		t.Fatalf("later good candidate did not complete the fixed window: cursor=%+v error=%v", next, err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.WorkspacePhase != string(WorkspacePhasePublished) {
		t.Fatalf("later good job was not published: phase=%s", job.WorkspacePhase)
	}
	for _, id := range newcomers {
		var later model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", id).Take(&later).Error; err != nil {
			t.Fatal(err)
		}
		if later.WorkspacePhase != string(WorkspacePhaseSealed) {
			t.Fatalf("new candidate entered an existing window: phase=%s", later.WorkspacePhase)
		}
	}
}

func TestRecoverySealedPublicationInternalBoundaries(t *testing.T) {
	t.Run("all_skipped_can_expire", func(t *testing.T) {
		base := newAuthorizationReceiptServiceFixtureConfiguredWithKinds(t, AuthorizationReceiptExecute, false, true,
			[]RecoveryOperationKind{RecoveryOperationSkip, RecoveryOperationSkip, RecoveryOperationSkip})
		fixture := newRecoveryResultLifecycleFixtureFromAuthorizationAllowEmpty(t, base, true)
		if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
			t.Fatal("all-skipped workspace acquired an empty result set")
		}
		fixture.now = *fixture.job.PlaintextDeadline
		if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err != nil {
			t.Fatalf("all-skipped workspace was not eligible for cleanup: %v", err)
		}
	})
	for _, boundary := range []string{"active_attempt", "invalid_marker", "failed_item", "cleanup_tuple", "revision_limit"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			switch boundary {
			case "active_attempt":
				if err := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("job_id = ?", fixture.job.ID).
					Updates(map[string]any{"state": string(AttemptStateRunning), "closed_at": nil, "updated_at": fixture.now}).Error; err != nil {
					t.Fatal(err)
				}
			case "invalid_marker":
				fixture.updateJob(t, map[string]any{"workspace_marker_binding_digest": strings.Repeat("a", 64), "updated_at": fixture.now})
			case "failed_item":
				if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).Where("id = ?", fixture.publishableRows[0].ID).
					Updates(map[string]any{"outcome": "failed", "failure_category": "pre_write_failed", "updated_at": fixture.now}).Error; err != nil {
					t.Fatal(err)
				}
			case "cleanup_tuple":
				fixture.updateJob(t, map[string]any{"workspace_cleanup_fence": uint64(1), "updated_at": fixture.now})
			case "revision_limit":
				fixture.updateJob(t, map[string]any{"transition_revision": uint64(math.MaxInt64), "updated_at": fixture.now})
				fixture.job.TransitionRevision = uint64(math.MaxInt64)
			}
			if _, err := fixture.service.publishTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
				t.Fatal("invalid sealed publication was admitted")
			}
			fixture.assertNoPublishedRows(t)
			if boundary != "failed_item" {
				fixture.now = *fixture.job.PlaintextDeadline
				if err := fixture.service.expireTerminalSealed(context.Background(), fixture.job.ID, fixture.job.TransitionRevision); err == nil {
					t.Fatal("invalid sealed cleanup transition was admitted")
				}
			}
		})
	}
}

func TestRecoverySealedPublicationPreservesExplicitClassifications(t *testing.T) {
	for _, kind := range []RecoveryResultClassification{RecoveryResultClassificationNonSecret, RecoveryResultClassificationSecret, RecoveryResultClassificationUnknown} {
		t.Run(string(kind), func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			request := fixture.publishRequest()
			for index := range request.Results {
				request.Results[index].Classification = RecoveryResultClassificationBinding{Kind: kind, Revision: int64(index + 1), SourceRevision: 7}
			}
			published, err := fixture.service.Publish(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			again, err := fixture.service.Publish(context.Background(), request)
			if err != nil || again.ResultSetID != published.ResultSetID {
				t.Fatalf("classification replay error=%v", err)
			}
			request.Results[0].Classification.SourceRevision++
			if _, err := fixture.service.Publish(context.Background(), request); err == nil {
				t.Fatal("publication replay accepted a different classification tuple")
			}
		})
	}
}

type cancelOnRecoveryKeySource struct {
	RecoveryWorkspaceKeySource
	cancel context.CancelFunc
}

func (source cancelOnRecoveryKeySource) ByVersion(ctx context.Context, domain backupasset.KeyDomain, version int) (backupasset.DomainKeyMaterial, error) {
	material, err := source.RecoveryWorkspaceKeySource.ByVersion(ctx, domain, version)
	source.cancel()
	return material, err
}

func TestRecoverySealedPublicationCursorFailures(t *testing.T) {
	t.Run("query_preserves_input", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		cursor := RecoverySealedResultCursor{afterCreatedAt: fixture.job.CreatedAt.Add(-time.Second),
			afterJobID: strings.Repeat("1", 32), highWaterCreatedAt: fixture.job.CreatedAt, highWaterJobID: fixture.job.ID}
		injected := errors.New("test: candidate query failure")
		callback := "test:sealed-candidate-query-failure"
		if err := fixture.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
				_ = tx.AddError(injected)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })
		next, err := fixture.service.PublishSealedBatch(context.Background(), cursor, 1)
		if !errors.Is(err, injected) || next != cursor {
			t.Fatalf("query failure changed cursor: next=%+v error=%v", next, err)
		}
	})
	t.Run("cancellation_preserves_attempted_position", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		// Cancellation may discard the transaction connection. Keep the shared
		// in-memory database alive independently of that connection's lifetime.
		sqlDB, err := fixture.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		anchor, err := sqlDB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := anchor.Close(); err != nil {
				t.Error(err)
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		keys := fixture.service.workspaceKeys
		fixture.service.workspaceKeys = cancelOnRecoveryKeySource{RecoveryWorkspaceKeySource: keys, cancel: cancel}
		cursor, err := fixture.service.PublishSealedBatch(ctx, RecoverySealedResultCursor{}, 1)
		if !errors.Is(err, context.Canceled) || cursor.afterJobID != fixture.job.ID ||
			!cursor.afterCreatedAt.Equal(fixture.job.CreatedAt) || cursor.highWaterJobID != fixture.job.ID {
			t.Fatalf("cancellation lost attempted position: cursor=%+v error=%v", cursor, err)
		}
		fixture.service.workspaceKeys = keys
		next, err := fixture.service.PublishSealedBatch(context.Background(), cursor, 1)
		if err != nil || next != (RecoverySealedResultCursor{}) {
			t.Fatalf("finish canceled scan: cursor=%+v error=%v", next, err)
		}
		if _, err := fixture.service.PublishSealedBatch(context.Background(), next, 1); err != nil {
			t.Fatalf("new scan did not recover canceled candidate: %v", err)
		}
	})
	t.Run("invalid_limits", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		for _, limit := range []int{0, -1, 1001} {
			if _, err := fixture.service.PublishSealedBatch(context.Background(), RecoverySealedResultCursor{}, limit); !errors.Is(err, ErrInvalidResultLifecycle) {
				t.Fatalf("limit %d error=%v", limit, err)
			}
		}
	})
}
