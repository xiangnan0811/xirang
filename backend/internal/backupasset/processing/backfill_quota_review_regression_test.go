package processing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

type quotaReviewContextKey struct{}

type quotaReviewOutcome struct {
	lease LeasedAttempt
	err   error
}

func TestProcessingBackfillRejectedCandidateSQLite(t *testing.T) {
	quotaRejectedCandidate(t, openProcessingBehaviorSQLite)
}

func TestProcessingBackfillRejectedCandidatePostgres(t *testing.T) {
	dsn := sharedAuthorityDSN(t)
	quotaRejectedCandidate(t, func(t *testing.T) processingBehaviorFixture { return openProcessingBehaviorPostgres(t, dsn) })
}

func quotaRejectedCandidate(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	for _, scenario := range []string{"LifecycleRejected", "DatabaseError", "ContextCanceled"} {
		t.Run(scenario, func(t *testing.T) {
			f, _ := quotaBehaviorFixture(t, open)
			first := validWorkDescriptor()
			second := quotaSecondPoint(t, f)
			for i := range 2 {
				d := first
				d.Parameters.Quality += i
				quotaRequireWork(t, f, d, fmt.Sprintf("running-%d", i), PriorityBackground)
				if _, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants); err != nil {
					t.Fatal(err)
				}
			}
			rejected := first
			rejected.Parameters.Quality += 2
			_, err := f.coordinator.RequestWork(context.Background(), WorkRequest{Descriptor: rejected, Interest: InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "rejected", PriorityClass: PriorityBackground, Priority: 900}})
			if err != nil {
				t.Fatal(err)
			}
			healthy, err := f.coordinator.RequestWork(context.Background(), WorkRequest{Descriptor: second, Interest: InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "healthy", PriorityClass: PriorityInteractive, Priority: 100}})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "ContextCanceled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: f.workerID}, f.grants)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled context lost: %v", err)
				}
				return
			}
			if scenario == "LifecycleRejected" {
				if err := f.db.Create(&model.RecoveryPointLifecycleAttempt{ID: strings.Repeat("f", 32), RecoveryPointID: first.Source.RecoveryPointID, Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				injected := errors.New("quota candidate database failure")
				const callback = "test:quota-review-candidate-error"
				if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Context.Value(quotaReviewContextKey{}) == "db-error" && processingCallbackTable(tx) == "recovery_points" {
						_ = tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
				_, err := f.coordinator.PullAttempt(context.WithValue(context.Background(), quotaReviewContextKey{}, "db-error"), PullRequest{WorkerID: f.workerID}, f.grants)
				if !errors.Is(err, injected) {
					t.Fatalf("database error lost: %v", err)
				}
				return
			}
			lease, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
			if err != nil || lease.Lease.JobID != healthy.JobID || lease.Lease.SlotClass != SlotInteractive || lease.Grants.Input.GrantID == "" || lease.Grants.Sink.GrantID == "" {
				t.Fatalf("healthy interactive candidate: job=%s slot=%s err=%v", lease.Lease.JobID, lease.Lease.SlotClass, err)
			}
		})
	}
}

func TestProcessingBackfillHeartbeatQuotaPostgres(t *testing.T) {
	dsn := sharedAuthorityDSN(t)
	for _, bucket := range []string{"Provider", "Capability"} {
		t.Run(bucket, func(t *testing.T) {
			f, policy := quotaBehaviorFixture(t, func(t *testing.T) processingBehaviorFixture { return openProcessingBehaviorPostgres(t, dsn) })
			policy.change(func(p *BackfillPolicy) {
				if bucket == "Provider" {
					p.ProviderConcurrency = 1
				} else {
					p.CapabilityConcurrency = 1
				}
			})
			other := quotaSecondPoint(t, f)
			workerB := (&coordinatorHarness{db: f.db, clock: f.clock, coordinator: f.coordinator}).registerNoopWorker(t, "c")
			quotaReviewAssignWorkerFingerprint(t, f, workerB, &other)
			quotaRequireWork(t, f, validWorkDescriptor(), "heartbeat-a", PriorityBackground)
			quotaRequireWork(t, f, other, "heartbeat-b", PriorityBackground)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			a, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: f.workerID}, f.grants)
			if err != nil {
				t.Fatal(err)
			}
			var attempt model.BackupAssetProcessingAttempt
			var grant model.BackupAssetProcessingGrant
			var rpLease model.RecoveryPointLease
			if err := f.db.First(&attempt, "id = ?", a.Lease.AttemptID).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.First(&grant, "id = ?", a.Grants.Sink.GrantID).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.First(&rpLease, "id = ?", attempt.RecoveryPointLeaseID).Error; err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(29 * time.Second)
			reached, release := make(chan int, 1), make(chan struct{})
			var once, releaseOnce sync.Once
			var operations sync.WaitGroup
			defer func() { releaseOnce.Do(func() { close(release) }); cancel(); operations.Wait() }()
			const callback = "test:quota-review-heartbeat"
			if err := f.db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Context.Value(quotaReviewContextKey{}) != "heartbeat" || processingCallbackTable(tx) != "backup_asset_processing_grants" || tx.Error != nil || tx.RowsAffected == 0 {
					return
				}
				once.Do(func() {
					var pid int
					if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
						_ = tx.AddError(err)
						return
					}
					reached <- pid
					select {
					case <-release:
					case <-ctx.Done():
						_ = tx.AddError(ctx.Err())
					}
				})
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.db.Callback().Update().Remove(callback) })
			type heartbeatOutcome struct {
				result HeartbeatResult
				err    error
			}
			hb := make(chan heartbeatOutcome, 1)
			operations.Add(1)
			go func() {
				defer operations.Done()
				result, err := f.coordinator.HeartbeatAttempt(context.WithValue(ctx, quotaReviewContextKey{}, "heartbeat"), HeartbeatRequest{AttemptID: a.Lease.AttemptID, WorkerID: f.workerID}, f.grants)
				hb <- heartbeatOutcome{result, err}
			}()
			var holder int
			select {
			case holder = <-reached:
			case result := <-hb:
				t.Fatalf("heartbeat ended before barrier: %v", result.err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if holder <= 0 {
				t.Fatalf("invalid heartbeat backend PID: %d", holder)
			}
			f.clock.Advance(2 * time.Second)
			now := f.clock.Now()
			if !now.After(attempt.WorkerLeaseExpiresAt) || !now.After(grant.ExpiresAt) || !now.After(rpLease.LeaseExpiresAt) {
				t.Fatal("clock did not cross old authority deadlines")
			}
			b := make(chan quotaReviewOutcome, 1)
			waiter := quotaReviewWatchLedger(t, f.db, "pull-b")
			operations.Add(1)
			go func() {
				defer operations.Done()
				lease, err := f.coordinator.PullAttempt(context.WithValue(ctx, quotaReviewContextKey{}, "pull-b"), PullRequest{WorkerID: workerB}, f.grants)
				b <- quotaReviewOutcome{lease, err}
			}()
			blocked, early := quotaReviewWaitBlockedOrResult(t, ctx, f.db, holder, waiter, b)
			releaseOnce.Do(func() { close(release) })
			select {
			case result := <-hb:
				if result.err != nil || !result.result.WorkerLeaseExpiresAt.After(now) || !result.result.RecoveryPointLeaseExpiresAt.After(now) || !result.result.GrantExpiresAt.After(now) {
					t.Fatalf("renewal failed: worker_deadline=%s point_deadline=%s grant_deadline=%s err=%v", result.result.WorkerLeaseExpiresAt, result.result.RecoveryPointLeaseExpiresAt, result.result.GrantExpiresAt, result.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !blocked {
				t.Fatalf("B finished before heartbeat commit: job=%s slot=%s deadline=%s err=%v", early.lease.Lease.JobID, early.lease.Lease.SlotClass, early.lease.Lease.WorkerLeaseExpiresAt, early.err)
			}
			select {
			case result := <-b:
				if !errors.Is(result.err, ErrNoWork) {
					t.Fatalf("B after committed renewal: job=%s slot=%s deadline=%s err=%v", result.lease.Lease.JobID, result.lease.Lease.SlotClass, result.lease.Lease.WorkerLeaseExpiresAt, result.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
	for _, scenario := range []string{"PullFirst", "HeartbeatRollback"} {
		t.Run(scenario, func(t *testing.T) {
			for _, bucket := range []string{"Provider", "Capability"} {
				t.Run(bucket, func(t *testing.T) { quotaReviewHeartbeatOrdering(t, dsn, scenario, bucket) })
			}
		})
	}
}

func quotaReviewHeartbeatOrdering(t *testing.T, dsn, scenario, bucket string) {
	t.Helper()
	f, policy := quotaBehaviorFixture(t, func(t *testing.T) processingBehaviorFixture { return openProcessingBehaviorPostgres(t, dsn) })
	policy.change(func(p *BackfillPolicy) {
		if bucket == "Provider" {
			p.ProviderConcurrency = 1
		} else {
			p.CapabilityConcurrency = 1
		}
	})
	other := quotaSecondPoint(t, f)
	workerB := (&coordinatorHarness{db: f.db, clock: f.clock, coordinator: f.coordinator}).registerNoopWorker(t, "c")
	quotaReviewAssignWorkerFingerprint(t, f, workerB, &other)
	quotaRequireWork(t, f, validWorkDescriptor(), "order-a", PriorityBackground)
	workB := quotaRequireWork(t, f, other, "order-b", PriorityBackground)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: f.workerID}, f.grants)
	if err != nil {
		t.Fatal(err)
	}
	var oldAttempt model.BackupAssetProcessingAttempt
	var oldLease model.RecoveryPointLease
	var oldGrant model.BackupAssetProcessingGrant
	if err := f.db.First(&oldAttempt, "id = ?", a.Lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&oldLease, "id = ?", oldAttempt.RecoveryPointLeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&oldGrant, "id = ?", a.Grants.Sink.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(29 * time.Second)
	reached, release := make(chan int, 1), make(chan struct{})
	var releaseOnce, barrierOnce sync.Once
	var operations sync.WaitGroup
	defer func() { releaseOnce.Do(func() { close(release) }); cancel(); operations.Wait() }()
	forcedRollback := errors.New("quota heartbeat rollback injection")
	if scenario == "PullFirst" {
		f.coordinator.config.BackfillPolicySource = func(ctx context.Context, tx *gorm.DB) (BackfillPolicy, error) {
			p, err := policy.read(ctx, tx)
			if err != nil || ctx.Value(quotaReviewContextKey{}) != "ordering-pull" {
				return p, err
			}
			var barrierErr error
			barrierOnce.Do(func() {
				var pid int
				if barrierErr = tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; barrierErr != nil {
					return
				}
				if pid <= 0 {
					barrierErr = fmt.Errorf("invalid pull backend PID: %d", pid)
					return
				}
				reached <- pid
				select {
				case <-release:
				case <-ctx.Done():
					barrierErr = ctx.Err()
				}
			})
			return p, barrierErr
		}
	} else {
		const callback = "test:quota-review-heartbeat-rollback"
		if err := f.db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Context.Value(quotaReviewContextKey{}) != "ordering-heartbeat" || processingCallbackTable(tx) != "backup_asset_processing_grants" || tx.Error != nil || tx.RowsAffected == 0 {
				return
			}
			barrierOnce.Do(func() {
				var pid int
				if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					_ = tx.AddError(err)
					return
				}
				if pid <= 0 {
					_ = tx.AddError(fmt.Errorf("invalid heartbeat backend PID: %d", pid))
					return
				}
				var renewed int64
				proof := `SELECT count(*) FROM backup_asset_processing_attempts a JOIN recovery_point_leases l ON l.id = a.recovery_point_lease_id JOIN backup_asset_processing_grants g ON g.attempt_id = a.id WHERE a.id = ? AND g.id = ? AND a.worker_lease_expires_at > ? AND l.lease_expires_at > ? AND g.expires_at > ?`
				if err := tx.Session(&gorm.Session{NewDB: true}).Raw(proof, oldAttempt.ID, oldGrant.ID, oldAttempt.WorkerLeaseExpiresAt, oldLease.LeaseExpiresAt, oldGrant.ExpiresAt).Scan(&renewed).Error; err != nil {
					_ = tx.AddError(err)
					return
				}
				if renewed != 1 {
					_ = tx.AddError(fmt.Errorf("uncommitted renewed authority count=%d", renewed))
					return
				}
				reached <- pid
				select {
				case <-release:
					_ = tx.AddError(forcedRollback)
				case <-ctx.Done():
					_ = tx.AddError(ctx.Err())
				}
			})
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.db.Callback().Update().Remove(callback) })
	}
	pullDone, heartbeatDone := make(chan quotaReviewOutcome, 1), make(chan quotaReviewOutcome, 1)
	startPull := func() {
		operations.Add(1)
		go func() {
			defer operations.Done()
			lease, err := f.coordinator.PullAttempt(context.WithValue(ctx, quotaReviewContextKey{}, "ordering-pull"), PullRequest{WorkerID: workerB}, f.grants)
			pullDone <- quotaReviewOutcome{lease, err}
		}()
	}
	startHeartbeat := func() {
		operations.Add(1)
		go func() {
			defer operations.Done()
			_, err := f.coordinator.HeartbeatAttempt(context.WithValue(ctx, quotaReviewContextKey{}, "ordering-heartbeat"), HeartbeatRequest{AttemptID: a.Lease.AttemptID, WorkerID: f.workerID}, f.grants)
			heartbeatDone <- quotaReviewOutcome{err: err}
		}()
	}
	waitLabel := "ordering-pull"
	if scenario == "PullFirst" {
		waitLabel = "ordering-heartbeat"
	}
	waiter := quotaReviewWatchLedger(t, f.db, waitLabel)
	if scenario == "PullFirst" {
		startPull()
	} else {
		startHeartbeat()
	}
	var holder int
	select {
	case holder = <-reached:
	case result := <-pullDone:
		t.Fatalf("pull ended before barrier: %v", result.err)
	case result := <-heartbeatDone:
		t.Fatalf("heartbeat ended before barrier: %v", result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if holder <= 0 {
		t.Fatalf("invalid holder backend PID: %d", holder)
	}
	var blocked bool
	var early quotaReviewOutcome
	if scenario == "PullFirst" {
		startHeartbeat()
		blocked, early = quotaReviewWaitBlockedOrResult(t, ctx, f.db, holder, waiter, heartbeatDone)
	} else {
		f.clock.Advance(2 * time.Second)
		startPull()
		blocked, early = quotaReviewWaitBlockedOrResult(t, ctx, f.db, holder, waiter, pullDone)
	}
	if !blocked {
		t.Fatalf("contender ended before ledger holder: job=%s err=%v", early.lease.Lease.JobID, early.err)
	}
	if scenario == "PullFirst" {
		f.clock.Advance(2 * time.Second)
	}
	now := f.clock.Now()
	if !now.After(oldAttempt.WorkerLeaseExpiresAt) || !now.After(oldLease.LeaseExpiresAt) || !now.After(oldGrant.ExpiresAt) {
		t.Fatal("clock did not cross original deadlines")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-pullDone:
		if result.err != nil || result.lease.Lease.JobID != workB.JobID || result.lease.Lease.AttemptID == "" || result.lease.Grants.Input.GrantID == "" || result.lease.Grants.Sink.GrantID == "" {
			t.Fatalf("pull after expired authority: job=%s err=%v", result.lease.Lease.JobID, result.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case result := <-heartbeatDone:
		want := ErrAttemptLost
		if scenario == "HeartbeatRollback" {
			want = forcedRollback
		}
		if !errors.Is(result.err, want) {
			t.Fatalf("heartbeat error=%v want=%v", result.err, want)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var attempt model.BackupAssetProcessingAttempt
	var lease model.RecoveryPointLease
	var grant model.BackupAssetProcessingGrant
	if err := f.db.First(&attempt, "id = ?", oldAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&lease, "id = ?", oldLease.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&grant, "id = ?", oldGrant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !attempt.WorkerLeaseExpiresAt.Equal(oldAttempt.WorkerLeaseExpiresAt) || !lease.LeaseExpiresAt.Equal(oldLease.LeaseExpiresAt) || !grant.ExpiresAt.Equal(oldGrant.ExpiresAt) || grant.State != oldGrant.State {
		t.Fatal("expired authority was revived or failed renewal was committed")
	}
}

// Observe an actual backend blocked by the owner or an early pull result. The
// active statement must target the ledger; a different lock is not evidence.
func quotaReviewWaitBlockedOrResult(t *testing.T, ctx context.Context, db *gorm.DB, holder int, waiter <-chan int, result <-chan quotaReviewOutcome) (bool, quotaReviewOutcome) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	select {
	case pid = <-waiter:
	case got := <-result:
		return false, got
	case <-ctx.Done():
		t.Fatalf("waiting for ledger query: %v", ctx.Err())
	}
	if holder <= 0 || pid <= 0 || holder == pid {
		t.Fatalf("invalid ledger backend PIDs: holder=%d waiter=%d", holder, pid)
	}
	for {
		select {
		case got := <-result:
			return false, got
		case <-ctx.Done():
			t.Fatalf("waiting for ledger contention: %v", ctx.Err())
		default:
		}
		var blocked bool
		err := sqlDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid = $1 AND l.relation = 'backup_asset_processing_backfill_ledger'::regclass AND l.granted WHERE a.pid = $2 AND $1 = ANY(pg_blocking_pids(a.pid)) AND a.query ILIKE '%backup_asset_processing_backfill_ledger%')`, holder, pid).Scan(&blocked)
		if err != nil {
			t.Fatalf("ledger lock probe: %v", err)
		}
		if blocked {
			return true, quotaReviewOutcome{}
		}
	}
}

func quotaReviewAssignWorkerFingerprint(t *testing.T, f processingBehaviorFixture, worker string, descriptor *WorkDescriptorV1) {
	t.Helper()
	descriptor.PipelineFingerprint = strings.Repeat("9", 64)
	if err := f.db.Model(&model.BackupAssetWorkerCapability{}).Where("worker_id = ?", worker).Update("pipeline_fingerprint", descriptor.PipelineFingerprint).Error; err != nil {
		t.Fatal(err)
	}
}

func quotaReviewWatchLedger(t *testing.T, db *gorm.DB, label string) <-chan int {
	t.Helper()
	reached := make(chan int, 1)
	var once sync.Once
	name := "test:quota-review-ledger-" + label
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(quotaReviewContextKey{}) != label || processingCallbackTable(tx) != "backup_asset_processing_backfill_ledger" {
			return
		}
		once.Do(func() {
			var pid int
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				_ = tx.AddError(err)
				return
			}
			reached <- pid
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return reached
}

func quotaReviewLastSlotPostgres(t *testing.T, f processingBehaviorFixture, workerB string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reached, release := make(chan int, 1), make(chan struct{})
	var once, releaseOnce sync.Once
	var operations sync.WaitGroup
	defer func() { releaseOnce.Do(func() { close(release) }); cancel(); operations.Wait() }()
	const callback = "test:quota-review-last-slot-grants"
	if err := f.db.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(quotaReviewContextKey{}) != "slot-a" || processingCallbackTable(tx) != "backup_asset_processing_grants" || tx.Error != nil {
			return
		}
		once.Do(func() {
			var proof struct {
				PID      int `gorm:"column:pid"`
				Attempts int64
				Grants   int64
			}
			query := `SELECT pg_backend_pid() AS pid, (SELECT count(*) FROM backup_asset_processing_attempts WHERE is_current = true) AS attempts, (SELECT count(*) FROM backup_asset_processing_grants) AS grants`
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw(query).Scan(&proof).Error; err != nil {
				_ = tx.AddError(err)
				return
			}
			if proof.PID <= 0 {
				_ = tx.AddError(fmt.Errorf("invalid grant barrier backend PID: %d", proof.PID))
				return
			}
			if proof.Attempts != 1 || proof.Grants != 2 {
				_ = tx.AddError(fmt.Errorf("before commit attempts=%d grants=%d", proof.Attempts, proof.Grants))
				return
			}
			reached <- proof.PID
			select {
			case <-release:
			case <-ctx.Done():
				_ = tx.AddError(ctx.Err())
			}
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Create().Remove(callback) })
	a := make(chan quotaReviewOutcome, 1)
	operations.Add(1)
	go func() {
		defer operations.Done()
		lease, err := f.coordinator.PullAttempt(context.WithValue(ctx, quotaReviewContextKey{}, "slot-a"), PullRequest{WorkerID: f.workerID}, f.grants)
		a <- quotaReviewOutcome{lease, err}
	}()
	var holder int
	select {
	case holder = <-reached:
	case result := <-a:
		t.Fatalf("A ended before grant barrier: %v", result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if holder <= 0 {
		t.Fatalf("invalid grant barrier backend PID: %d", holder)
	}
	waiter := quotaReviewWatchLedger(t, f.db, "slot-b")
	b := make(chan quotaReviewOutcome, 1)
	operations.Add(1)
	go func() {
		defer operations.Done()
		lease, err := f.coordinator.PullAttempt(context.WithValue(ctx, quotaReviewContextKey{}, "slot-b"), PullRequest{WorkerID: workerB}, f.grants)
		b <- quotaReviewOutcome{lease, err}
	}()
	blocked, early := quotaReviewWaitBlockedOrResult(t, ctx, f.db, holder, waiter, b)
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-a:
		if result.err != nil || result.lease.Lease.AttemptID == "" || result.lease.Grants.Input.GrantID == "" || result.lease.Grants.Sink.GrantID == "" {
			t.Fatalf("A pull: job=%s slot=%s err=%v", result.lease.Lease.JobID, result.lease.Lease.SlotClass, result.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !blocked {
		t.Fatalf("B finished without waiting for A ledger: job=%s slot=%s err=%v", early.lease.Lease.JobID, early.lease.Lease.SlotClass, early.err)
	}
	select {
	case result := <-b:
		if !errors.Is(result.err, ErrNoWork) {
			t.Fatalf("B pull: job=%s slot=%s err=%v", result.lease.Lease.JobID, result.lease.Lease.SlotClass, result.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var attempts, grants int64
	if err := f.db.Model(&model.BackupAssetProcessingAttempt{}).Where("is_current = ?", true).Count(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.BackupAssetProcessingGrant{}).Count(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || grants != 2 {
		t.Fatalf("attempts=%d grants=%d", attempts, grants)
	}
}
