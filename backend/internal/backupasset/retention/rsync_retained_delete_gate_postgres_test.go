package retention

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

type rsyncDeleteGateResult struct {
	attempt LifecycleAttempt
	err     error
}

type rsyncDeleteLockingResolver struct {
	policyID string
}

func (resolver rsyncDeleteLockingResolver) ResolveDeletePoint(ctx context.Context, tx *gorm.DB, _ LifecyclePointRequest, _ model.RecoveryPoint, _ model.BackupRepository) (provider.DeletePointRequest, error) {
	var id string
	err := tx.WithContext(ctx).Raw("SELECT id FROM backup_retention_policies WHERE id = ? FOR UPDATE /* rsync_resolver_cancel_probe */", resolver.policyID).Scan(&id).Error
	return provider.DeletePointRequest{}, err
}

// Observe the resolver's specific row-lock statement blocked by our policy row
// holder and verify that this same backend already owns the repository gate.
func waitRsyncDeleteResolverRowLock(t *testing.T, ctx context.Context, db *gorm.DB, holderPID int, repositoryID string, result <-chan rsyncDeleteGateResult) int {
	t.Helper()
	key := "xirang:recovery-authority-repository:v1:" + repositoryID
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiterPID int
		err := db.WithContext(ctx).Raw(`SELECT COALESCE((
			SELECT activity.pid FROM pg_stat_activity activity
			JOIN pg_locks gate ON gate.pid = activity.pid
			WHERE activity.wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(activity.pid))
				AND activity.query LIKE '%rsync_resolver_cancel_probe%'
				AND gate.locktype = 'advisory' AND gate.granted AND gate.mode = 'ExclusiveLock'
				AND gate.objsubid = 1
				AND gate.classid::bigint = ((hashtextextended(?, 0) >> 32) & 4294967295)
				AND gate.objid::bigint = (hashtextextended(?, 0) & 4294967295)
			LIMIT 1), 0)`, holderPID, key, key).Scan(&waiterPID).Error
		if err != nil {
			t.Fatalf("inspect resolver row-lock wait: %v", err)
		}
		if waiterPID != 0 {
			return waiterPID
		}
		select {
		case got := <-result:
			t.Fatalf("Advance completed before resolver row lock: phase=%s err=%v", got.attempt.Phase, got.err)
		case <-ctx.Done():
			t.Fatalf("waiting for resolver row lock: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

// The wait must be for this repository's v1 advisory key and the holder's
// backend, not merely another row or advisory lock acquired by the operation.
func waitRsyncDeleteRepositoryGate(t *testing.T, ctx context.Context, db *gorm.DB, holderPID int, repositoryID string, done <-chan struct{}) int {
	t.Helper()
	key := "xirang:recovery-authority-repository:v1:" + repositoryID
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
			t.Fatalf("inspect repository authority wait: %v", err)
		}
		if waiterPID != 0 {
			return waiterPID
		}
		select {
		case <-done:
			t.Fatal("Advance completed before waiting on the repository authority gate")
		case <-ctx.Done():
			t.Fatalf("waiting for repository authority gate: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func holdRsyncDeleteRepositoryGate(t *testing.T, ctx context.Context, db *gorm.DB, repositoryID string) (*gorm.DB, int) {
	t.Helper()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin repository authority holder: %v", tx.Error)
	}
	var pid int
	if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatalf("load repository authority holder pid: %v", err)
	}
	key := "xirang:recovery-authority-repository:v1:" + repositoryID
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatalf("hold repository authority gate: %v", err)
	}
	return tx, pid
}

type rsyncDeleteGateSnapshot struct {
	attempt    model.RecoveryPointLifecycleAttempt
	point      model.RecoveryPoint
	lease      model.RecoveryPointLease
	repository model.BackupRepository
	claims     int64
	tombstones int64
	slots      int64
	writes     int
}

func snapshotRsyncDeleteGate(t *testing.T, fixture *claimedExpiryFixture, audit *acceptanceAuditSink, repositoryID string) rsyncDeleteGateSnapshot {
	t.Helper()
	var snapshot rsyncDeleteGateSnapshot
	for _, row := range []struct {
		id   string
		dest any
	}{
		{fixture.attempt.ID, &snapshot.attempt},
		{fixture.pointID, &snapshot.point},
		{fixture.attempt.LeaseID, &snapshot.lease},
		{repositoryID, &snapshot.repository},
	} {
		if err := fixture.db.First(row.dest, "id = ?", row.id).Error; err != nil {
			t.Fatalf("load repository gate snapshot: %v", err)
		}
	}
	snapshot.claims = acceptanceCountClaims(t, fixture.db, fixture.attempt.ID)
	snapshot.tombstones = acceptanceCountTombstones(t, fixture.db, fixture.pointID)
	snapshot.slots = acceptanceCountSlots(t, fixture.db, fixture.attempt.ID)
	snapshot.writes = audit.Writes()
	return snapshot
}

func TestLifecycleRsyncProviderDeleteAuthorityLockPostgres(t *testing.T) {
	for index, mode := range []string{"caller_cancellation", "statement_cancellation", "receipt_deadline"} {
		t.Run(mode, func(t *testing.T) {
			db := newLifecycleAcceptancePostgresDB(t)
			base := uint64(91000 + index*100)
			repositoryID := testOpaqueID(base)
			// The fixture inserts the Rsync repository with its final identity;
			// migration 000087 forbids changing an existing provider kind.
			fixture := newClaimedExpiryFixtureWithDBAndProvider(t, db, base, backupasset.ProviderRsync)
			fixture.deleter.result = PointDeletionResult{Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("a", 64)}
			acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			audit := &acceptanceAuditSink{}
			fixture.coordinator.audit = audit
			before := snapshotRsyncDeleteGate(t, fixture, audit, repositoryID)
			if before.claims != 0 || before.tombstones != 0 || before.slots != 0 || before.writes != 0 {
				t.Fatalf("unexpected initial effect state: %+v", before)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			callerCtx, cancelCaller := context.WithCancel(ctx)
			defer cancelCaller()
			result := make(chan rsyncDeleteGateResult, 1)
			done := make(chan struct{})
			var holder *gorm.DB
			var releaseOnce sync.Once
			releaseProvider := make(chan struct{})
			defer func() {
				cancelCaller()
				releaseOnce.Do(func() { close(releaseProvider) })
				if holder != nil {
					_ = holder.Rollback().Error
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("Advance goroutine did not stop during cleanup")
				}
			}()

			var clock atomic.Int64
			clock.Store(fixture.clock.UnixNano())
			if mode == "receipt_deadline" {
				fixture.coordinator.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
				// Prevent the effect renewer from advancing the claim while the
				// provider is paused. It is stopped before receipt persistence.
				fixture.coordinator.effectClaimAfter = func(time.Duration) <-chan time.Time { return nil }
				fixture.deleter.entered = make(chan struct{})
				fixture.deleter.release = releaseProvider
			}
			var holderPID int
			if mode != "receipt_deadline" {
				holder, holderPID = holdRsyncDeleteRepositoryGate(t, ctx, db, repositoryID)
			}
			go func() {
				defer close(done)
				attempt, err := fixture.coordinator.Advance(callerCtx, fixture.attempt.ID)
				result <- rsyncDeleteGateResult{attempt: attempt, err: err}
			}()

			if mode == "receipt_deadline" {
				select {
				case <-fixture.deleter.entered:
				case <-done:
					t.Fatal("Advance finished before provider invocation")
				case <-ctx.Done():
					t.Fatalf("waiting for provider invocation: %v", ctx.Err())
				}
				holder, holderPID = holdRsyncDeleteRepositoryGate(t, ctx, db, repositoryID)
				claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
				if claim.State != "in_flight" {
					t.Fatalf("provider invocation claim state=%q, want in_flight", claim.State)
				}
				releaseOnce.Do(func() { close(releaseProvider) })
				waitRsyncDeleteRepositoryGate(t, ctx, db, holderPID, repositoryID, done)
				waitingClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
				if !reflect.DeepEqual(waitingClaim, claim) {
					t.Fatalf("claim renewed while receipt waited: before=%+v waiting=%+v", claim, waitingClaim)
				}
				clock.Store(claim.DeadlineAt.Add(time.Second).UnixNano())
				if err := holder.Rollback().Error; err != nil {
					t.Fatalf("release receipt gate: %v", err)
				}
				holder = nil
				select {
				case got := <-result:
					if !errors.Is(got.err, backupasset.ErrConflict) || got.attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
						t.Fatalf("expired receipt result=%+v error=%v, want provider-delete conflict", got.attempt, got.err)
					}
				case <-ctx.Done():
					t.Fatalf("waiting for expired receipt: %v", ctx.Err())
				}
				afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
				if afterClaim.State != "uncertain" || !afterClaim.DeadlineAt.Equal(claim.DeadlineAt) ||
					afterClaim.ExecutionID != claim.ExecutionID {
					t.Fatalf("expired receipt claim changed unexpectedly: before=%+v after=%+v", claim, afterClaim)
				}
				if fixture.deleter.calls != 1 || fixture.deleter.completed != 1 || fixture.deleter.verifyCalls != 0 ||
					acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 || acceptanceCountTombstones(t, db, fixture.pointID) != 0 ||
					acceptanceCountSlots(t, db, fixture.attempt.ID) != 0 || audit.Writes() != 0 {
					t.Fatalf("expired receipt effects: calls=%d completed=%d verify=%d tombstones=%d audit=%d",
						fixture.deleter.calls, fixture.deleter.completed, fixture.deleter.verifyCalls,
						acceptanceCountTombstones(t, db, fixture.pointID), audit.Writes())
				}
				return
			}

			waiterPID := waitRsyncDeleteRepositoryGate(t, ctx, db, holderPID, repositoryID, done)
			if mode == "caller_cancellation" {
				cancelCaller()
			} else {
				var canceled bool
				if err := db.WithContext(ctx).Raw("SELECT pg_cancel_backend(?)", waiterPID).Scan(&canceled).Error; err != nil || !canceled {
					t.Fatalf("cancel gate statement: canceled=%t err=%v", canceled, err)
				}
			}
			select {
			case got := <-result:
				if got.err == nil || (mode == "caller_cancellation" && !errors.Is(got.err, context.Canceled)) {
					t.Fatalf("gate cancellation result=%+v error=%v", got.attempt, got.err)
				}
				if mode == "statement_cancellation" {
					var pgErr *pgconn.PgError
					if callerCtx.Err() != nil || !errors.As(got.err, &pgErr) || pgErr.Code != "57014" {
						t.Fatalf("statement cancellation error=%v caller=%v, want SQLSTATE 57014 with live caller", got.err, callerCtx.Err())
					}
				}
			case <-ctx.Done():
				t.Fatalf("canceled Advance did not return while gate remained held: %v", ctx.Err())
			}
			after := snapshotRsyncDeleteGate(t, fixture, audit, repositoryID)
			if !reflect.DeepEqual(before, after) || fixture.deleter.prepareCalls != 0 || fixture.deleter.calls != 0 || fixture.deleter.verifyCalls != 0 {
				t.Fatalf("canceled gate wait mutated state or invoked provider: before=%+v after=%+v prepare=%d calls=%d verify=%d",
					before, after, fixture.deleter.prepareCalls, fixture.deleter.calls, fixture.deleter.verifyCalls)
			}
		})
	}
	t.Run("resolver_statement_cancellation", func(t *testing.T) {
		db := newLifecycleAcceptancePostgresDB(t)
		const base uint64 = 91300
		repositoryID := testOpaqueID(base)
		fixture := newClaimedExpiryFixtureWithDBAndProvider(t, db, base, backupasset.ProviderRsync)
		// Registry preparation requires the selected point and repository to
		// agree on the capability revision before it can enter the resolver.
		if err := db.Model(&model.BackupRepository{}).Where("id = ?", repositoryID).
			Update("capability_revision", fixture.claim.PolicySelection.Points[0].CapabilityRevision).Error; err != nil {
			t.Fatalf("align resolver fixture capability revision: %v", err)
		}
		acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
		audit := &acceptanceAuditSink{}
		fixture.coordinator.audit = audit

		deleter := &acceptanceRegistryDeleter{kind: backupasset.ProviderRsync}
		registry := provider.NewRegistry()
		if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
			Prober: &acceptanceProviderProber{kind: backupasset.ProviderRsync}, PointDeleter: deleter,
		}); err != nil {
			t.Fatalf("register Rsync provider: %v", err)
		}
		policyID := fixture.claim.PolicySelection.PolicyID
		adapter, err := NewRegistryPointDeletion(db, registry, rsyncDeleteLockingResolver{policyID: policyID})
		if err != nil {
			t.Fatalf("create registry point deletion: %v", err)
		}
		adapter.SetNow(func() time.Time { return fixture.clock })
		fixture.coordinator.deleter = adapter
		before := snapshotRsyncDeleteGate(t, fixture, audit, repositoryID)
		if before.claims != 0 || before.tombstones != 0 || before.slots != 0 || before.writes != 0 {
			t.Fatalf("unexpected initial effect state: %+v", before)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		callerCtx, cancelCaller := context.WithCancel(ctx)
		defer cancelCaller()
		holder := db.WithContext(ctx).Begin()
		if holder.Error != nil {
			t.Fatalf("begin resolver row holder: %v", holder.Error)
		}
		var holderPID int
		if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
			_ = holder.Rollback().Error
			t.Fatalf("load resolver row holder pid: %v", err)
		}
		var policyBefore model.BackupRetentionPolicy
		if err := holder.Raw("SELECT * FROM backup_retention_policies WHERE id = ? FOR UPDATE", policyID).Scan(&policyBefore).Error; err != nil || policyBefore.ID != policyID {
			_ = holder.Rollback().Error
			t.Fatalf("hold resolver policy row: id=%q err=%v", policyBefore.ID, err)
		}
		result := make(chan rsyncDeleteGateResult, 1)
		done := make(chan struct{})
		defer func() {
			cancelCaller()
			_ = holder.Rollback().Error
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Advance goroutine did not stop during cleanup")
			}
		}()
		go func() {
			defer close(done)
			attempt, err := fixture.coordinator.Advance(callerCtx, fixture.attempt.ID)
			result <- rsyncDeleteGateResult{attempt: attempt, err: err}
		}()

		waiterPID := waitRsyncDeleteResolverRowLock(t, ctx, db, holderPID, repositoryID, result)
		var canceled bool
		if err := db.WithContext(ctx).Raw("SELECT pg_cancel_backend(?)", waiterPID).Scan(&canceled).Error; err != nil || !canceled {
			t.Fatalf("cancel resolver statement: canceled=%t err=%v", canceled, err)
		}
		select {
		case got := <-result:
			var pgErr *pgconn.PgError
			if callerCtx.Err() != nil || !errors.As(got.err, &pgErr) || pgErr.Code != "57014" {
				t.Fatalf("resolver cancellation error=%v caller=%v, want SQLSTATE 57014 with live caller", got.err, callerCtx.Err())
			}
		case <-ctx.Done():
			t.Fatalf("canceled Advance did not return while policy row remained held: %v", ctx.Err())
		}
		after := snapshotRsyncDeleteGate(t, fixture, audit, repositoryID)
		var policyAfter model.BackupRetentionPolicy
		if err := db.WithContext(ctx).First(&policyAfter, "id = ?", policyID).Error; err != nil {
			t.Fatalf("load policy after resolver cancellation: %v", err)
		}
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(policyBefore, policyAfter) || deleter.Calls() != 0 || fixture.deleter.calls != 0 {
			t.Fatalf("canceled resolver mutated state or invoked provider: before=%+v after=%+v policy before=%+v policy after=%+v registry calls=%d fake calls=%d",
				before, after, policyBefore, policyAfter, deleter.Calls(), fixture.deleter.calls)
		}
	})
}
