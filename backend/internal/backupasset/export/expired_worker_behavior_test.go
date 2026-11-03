package export

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type expiredWorkerBehavior struct {
	fixture     exportBehaviorFixture
	clock       time.Time
	coordinator *AttemptCoordinator
	lifecycle   *Lifecycle
	port        *PersistentLifecyclePort
	actor       SelectionActor
	selection   FrozenSelection
}

func newExpiredWorkerBehavior(t *testing.T, open func(*testing.T) exportBehaviorFixture) *expiredWorkerBehavior {
	t.Helper()
	fixture := open(t)
	behavior := &expiredWorkerBehavior{fixture: fixture, clock: fixture.service.now().UTC(), actor: SelectionActor{UserID: 41, Role: "admin"}}
	fixture.service.now = func() time.Time { return behavior.clock }
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, fixture.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	behavior.selection = selection
	leases, err := backupasset.NewLeaseService(fixture.db, func() time.Time { return behavior.clock }, backupasset.LeaseConfig{
		Duration: fixture.config.LeaseTTL, Heartbeat: fixture.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	limits := WorkerCapacityLimits{WorkerConcurrency: 4, UserActiveJobs: 4}
	behavior.coordinator, err = NewAttemptCoordinatorWithWorkerCapacity(fixture.db, func() time.Time { return behavior.clock }, limits, leases)
	if err != nil {
		t.Fatal(err)
	}
	quota, err := NewQuotaService(fixture.db, func() time.Time { return behavior.clock }, fixture.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "expired-worker")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	behavior.port, err = NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: leases, Quota: quota,
		Store: store, AttemptWork: NewAttemptWorkRegistry(), WorkerCapacity: &limits, Now: func() time.Time { return behavior.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	behavior.lifecycle, err = NewLifecycle(LifecycleDependencies{DB: fixture.db, Port: behavior.port, Now: func() time.Time { return behavior.clock }})
	if err != nil {
		t.Fatal(err)
	}
	return behavior
}

func (b *expiredWorkerBehavior) claim(t *testing.T, name string) (string, AttemptClaim) {
	t.Helper()
	created, err := b.fixture.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: b.actor, Selection: b.selection, IdempotencyKey: "expired-worker-" + name,
		ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := b.coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "expired-worker-" + name})
	if err != nil {
		t.Fatal(err)
	}
	return created.JobID, claim
}

func (b *expiredWorkerBehavior) cancel(t *testing.T, jobID string) {
	t.Helper()
	status, err := b.fixture.service.Cancel(context.Background(), b.actor, jobID)
	if err != nil || status.ExecutionState != ExecutionCancelRequested {
		t.Fatalf("cancel status=%+v err=%v", status, err)
	}
}

func (b *expiredWorkerBehavior) assertPurged(t *testing.T, jobID, attemptID string) {
	t.Helper()
	count, err := b.lifecycle.Reconcile(context.Background(), 10)
	if err != nil || count < 1 {
		t.Fatalf("lifecycle processed=%d err=%v", count, err)
	}
	var job model.BackupAssetExportJob
	var attempt model.BackupAssetExportAttempt
	var key model.BackupAssetExportKey
	if err := b.fixture.db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.fixture.db.First(&attempt, "id = ?", attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.fixture.db.Where("job_id = ?", jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionCanceled) || job.CleanupState != string(CleanupPurged) || job.CurrentAttemptID != nil ||
		attempt.State != string(AttemptCanceled) || attempt.IsCurrent || attempt.FailureCategory != "canceled" || attempt.FinishedAt == nil ||
		key.State != "destroyed" || key.DestroyedAt == nil || len(key.WrappedDEK) != 0 {
		t.Fatalf("purged cancellation job=%+v attempt=%+v key=%+v", job, attempt, key)
	}
	assertReleasedDurableWorkerReservationPair(t, b.fixture.db, jobID, attemptID, 0)
	var active int64
	if err := b.fixture.db.Model(&model.BackupAssetExportReservation{}).Where("job_id = ? AND state = ?", jobID, "active").Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("active reservations=%d err=%v", active, err)
	}
	var sources []model.BackupAssetExportSourceLease
	if err := b.fixture.db.Where("job_id = ?", jobID).Find(&sources).Error; err != nil || len(sources) == 0 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	for _, source := range sources {
		var lease model.RecoveryPointLease
		if err := b.fixture.db.First(&lease, "id = ?", source.LeaseID).Error; err != nil {
			t.Fatal(err)
		}
		if source.State != "released" || lease.Status != string(backupasset.LeaseReleased) {
			t.Fatalf("source=%+v lease=%+v", source, lease)
		}
	}
}

type expiredWorkerSweepContextKey struct{}
type expiredWorkerSweepResult struct {
	count int
	err   error
}

// Consume one selected pair row to establish the query snapshot; the other
// remains for the sweep. WAL permits the competing SQLite writer to commit.
func pauseExpiredWorkerSweep(t *testing.T, b *expiredWorkerBehavior, jobID, attemptID string) func() expiredWorkerSweepResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ctx = context.WithValue(ctx, expiredWorkerSweepContextKey{}, true)
	selected := make(chan struct{}, 1)
	resume := make(chan struct{})
	var once sync.Once
	var paused atomic.Bool
	unblock := func() { once.Do(func() { close(resume) }) }
	callback := "test:expired_worker_selection_barrier"
	if err := b.fixture.db.Callback().Row().After("gorm:row").Register(callback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(expiredWorkerSweepContextKey{}) != true ||
			!strings.Contains(tx.Statement.SQL.String(), "backup_asset_export_reservations AS reservation") || !paused.CompareAndSwap(false, true) {
			return
		}
		rows, ok := tx.Statement.Dest.(*sql.Rows)
		if !ok || !rows.Next() {
			_ = tx.AddError(errors.New("worker sweep did not select the first reservation"))
			selected <- struct{}{}
			return
		}
		var selectedJob, selectedAttempt string
		var selectedOwner uint
		if err := rows.Scan(&selectedJob, &selectedAttempt, &selectedOwner); err != nil || selectedJob != jobID || selectedAttempt != attemptID || selectedOwner != b.actor.UserID {
			_ = tx.AddError(fmt.Errorf("unexpected worker sweep selection job=%s attempt=%s owner=%d err=%v", selectedJob, selectedAttempt, selectedOwner, err))
			selected <- struct{}{}
			return
		}
		selected <- struct{}{}
		select {
		case <-resume:
		case <-ctx.Done():
			_ = tx.AddError(ctx.Err())
		}
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	result := make(chan expiredWorkerSweepResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		count, err := b.coordinator.ReconcileExpiredWorkerReservations(ctx, 1)
		result <- expiredWorkerSweepResult{count, err}
	}()
	t.Cleanup(func() {
		unblock()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("sweep did not join")
		}
		if err := b.fixture.db.Callback().Row().Remove(callback); err != nil {
			t.Errorf("remove sweep barrier: %v", err)
		}
	})
	select {
	case <-selected:
	case <-ctx.Done():
		t.Fatalf("selection barrier: %v", ctx.Err())
	}
	return func() expiredWorkerSweepResult {
		unblock()
		select {
		case got := <-result:
			return got
		case <-ctx.Done():
			t.Fatalf("sweep result: %v", ctx.Err())
			return expiredWorkerSweepResult{}
		}
	}
}

func runExpiredWorkerReservationBehavior(t *testing.T, open func(*testing.T) exportBehaviorFixture) {
	t.Run("CanceledSkippedBeforeLimitAndLifecyclePurges", func(t *testing.T) {
		b := newExpiredWorkerBehavior(t, open)
		canceledID, canceled := b.claim(t, "canceled")
		b.clock = b.clock.Add(time.Second)
		liveID, live := b.claim(t, "expired")
		b.cancel(t, canceledID)
		b.clock = live.LeaseExpiresAt
		count, err := b.coordinator.ReconcileExpiredWorkerReservations(context.Background(), 1)
		if err != nil || count != 1 {
			t.Fatalf("sweep count=%d err=%v", count, err)
		}
		assertDurableWorkerReservationPair(t, b.fixture.db, canceledID, canceled.AttemptID, canceled.LeaseExpiresAt, 1)
		assertExpiredWorkerCancellationPending(t, b.fixture.db, canceledID, canceled.AttemptID)
		assertReleasedDurableWorkerReservationPair(t, b.fixture.db, liveID, live.AttemptID, 1)
		var expired model.BackupAssetExportAttempt
		if err := b.fixture.db.First(&expired, "id = ?", live.AttemptID).Error; err != nil {
			t.Fatal(err)
		}
		if expired.State != string(AttemptSuperseded) || expired.FailureCategory != "heartbeat_lost" || expired.IsCurrent {
			t.Fatalf("expired attempt=%+v", expired)
		}
		b.assertPurged(t, canceledID, canceled.AttemptID)
	})
	t.Run("CancellationWinsAfterSelection", func(t *testing.T) {
		b := newExpiredWorkerBehavior(t, open)
		jobID, claim := b.claim(t, "cancel-race")
		b.clock = claim.LeaseExpiresAt
		finish := pauseExpiredWorkerSweep(t, b, jobID, claim.AttemptID)
		b.cancel(t, jobID)
		result := finish()
		if result.err != nil || result.count != 0 {
			t.Fatalf("sweep=%+v", result)
		}
		assertDurableWorkerReservationPair(t, b.fixture.db, jobID, claim.AttemptID, claim.LeaseExpiresAt, 1)
		assertExpiredWorkerCancellationPending(t, b.fixture.db, jobID, claim.AttemptID)
		b.assertPurged(t, jobID, claim.AttemptID)
	})
	t.Run("RenewalWinsAfterSelection", func(t *testing.T) {
		b := newExpiredWorkerBehavior(t, open)
		jobID, claim := b.claim(t, "renewal-race")
		b.clock = claim.LeaseExpiresAt
		finish := pauseExpiredWorkerSweep(t, b, jobID, claim.AttemptID)
		renewAt := claim.LeaseExpiresAt.Add(-time.Second)
		leases, err := backupasset.NewLeaseService(b.fixture.db, func() time.Time { return renewAt }, backupasset.LeaseConfig{
			Duration: b.fixture.config.LeaseTTL, Heartbeat: b.fixture.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		renewer, err := NewAttemptCoordinatorWithWorkerCapacity(b.fixture.db, func() time.Time { return renewAt }, WorkerCapacityLimits{WorkerConcurrency: 4, UserActiveJobs: 4}, leases)
		if err != nil {
			t.Fatal(err)
		}
		heartbeat, err := renewer.Heartbeat(context.Background(), AttemptHeartbeatRequest{JobID: jobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken})
		if err != nil || !heartbeat.LeaseExpiresAt.After(b.clock) {
			t.Fatalf("heartbeat=%+v err=%v", heartbeat, err)
		}
		result := finish()
		if result.err != nil || result.count != 0 {
			t.Fatalf("sweep=%+v", result)
		}
		assertDurableWorkerReservationPair(t, b.fixture.db, jobID, claim.AttemptID, heartbeat.LeaseExpiresAt, 1)
		var attempt model.BackupAssetExportAttempt
		var job model.BackupAssetExportJob
		if err := b.fixture.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
			t.Fatal(err)
		}
		if err := b.fixture.db.First(&job, "id = ?", jobID).Error; err != nil {
			t.Fatal(err)
		}
		if !attempt.IsCurrent || attempt.State != string(AttemptActive) || attempt.FailureCategory != "" || job.ErrorCategory != "" || job.CurrentAttemptID == nil || *job.CurrentAttemptID != claim.AttemptID {
			t.Fatalf("renewed job=%+v attempt=%+v", job, attempt)
		}
	})
	t.Run("CompetingLifecycleSettlement", func(t *testing.T) {
		for _, phase := range []string{"fenced", "purged", "malformed-purged"} {
			t.Run(phase, func(t *testing.T) {
				b := newExpiredWorkerBehavior(t, open)
				jobID, claim := b.claim(t, "settlement-"+phase)
				b.clock = claim.LeaseExpiresAt
				finish := pauseExpiredWorkerSweep(t, b, jobID, claim.AttemptID)
				b.cancel(t, jobID)
				if phase == "fenced" {
					if err := b.port.FenceAttempts(context.Background(), jobID); err != nil {
						t.Fatal(err)
					}
				} else {
					b.assertPurged(t, jobID, claim.AttemptID)
					if phase == "malformed-purged" {
						changed := b.fixture.db.Model(&model.BackupAssetExportReservation{}).Where("job_id = ? AND attempt_id = ? AND kind = ? AND bucket_id IN (SELECT id FROM backup_asset_export_quota_buckets WHERE scope = ?)", jobID, claim.AttemptID, "worker", "user").Update("lease_owner", "wrong-owner")
						if changed.Error != nil || changed.RowsAffected != 1 {
							t.Fatalf("corrupt terminal pair rows=%d err=%v", changed.RowsAffected, changed.Error)
						}
					}
				}
				result := finish()
				if result.count != 0 || (phase == "malformed-purged" && !errors.Is(result.err, ErrAttemptFenceLost)) || (phase != "malformed-purged" && result.err != nil) {
					t.Fatalf("%s sweep=%+v", phase, result)
				}
				if phase == "fenced" {
					b.assertPurged(t, jobID, claim.AttemptID)
				}
			})
		}
	})
	t.Run("UnexplainedOwnershipMismatchFails", func(t *testing.T) {
		for _, mismatch := range []string{"owner", "current-attempt"} {
			t.Run(mismatch, func(t *testing.T) {
				b := newExpiredWorkerBehavior(t, open)
				jobID, claim := b.claim(t, "mismatch-"+mismatch)
				if mismatch == "owner" {
					if err := b.fixture.db.Create(&model.User{ID: 42, Username: "other-export-owner", PasswordHash: "hash", Role: "admin", Onboarded: true, CreatedAt: b.clock, UpdatedAt: b.clock}).Error; err != nil {
						t.Fatal(err)
					}
				}
				b.clock = claim.LeaseExpiresAt
				finish := pauseExpiredWorkerSweep(t, b, jobID, claim.AttemptID)
				updates := map[string]any{"current_attempt_id": nil}
				if mismatch == "owner" {
					updates = map[string]any{"owner_user_id": 42}
				}
				if err := b.fixture.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				result := finish()
				if result.count != 0 || !errors.Is(result.err, ErrAttemptFenceLost) {
					t.Fatalf("%s sweep=%+v", mismatch, result)
				}
				assertDurableWorkerReservationPair(t, b.fixture.db, jobID, claim.AttemptID, claim.LeaseExpiresAt, 1)
			})
		}
	})
	t.Run("MalformedStillOwnedPairFails", func(t *testing.T) {
		for _, corruption := range []string{"missing", "owner", "expiry"} {
			t.Run(corruption, func(t *testing.T) {
				b := newExpiredWorkerBehavior(t, open)
				jobID, claim := b.claim(t, fmt.Sprintf("malformed-%s", corruption))
				query := b.fixture.db.Where("job_id = ? AND attempt_id = ? AND kind = ? AND bucket_id IN (SELECT id FROM backup_asset_export_quota_buckets WHERE scope = ?)", jobID, claim.AttemptID, "worker", "user")
				var result *gorm.DB
				switch corruption {
				case "missing":
					result = query.Delete(&model.BackupAssetExportReservation{})
				case "owner":
					result = query.Model(&model.BackupAssetExportReservation{}).Update("lease_owner", "wrong-owner")
				case "expiry":
					result = query.Model(&model.BackupAssetExportReservation{}).Update("lease_expires_at", claim.LeaseExpiresAt.Add(time.Second))
				}
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("corrupt %s: rows=%d err=%v", corruption, result.RowsAffected, result.Error)
				}
				b.clock = claim.LeaseExpiresAt
				count, err := b.coordinator.ReconcileExpiredWorkerReservations(context.Background(), 1)
				if count != 0 || !errors.Is(err, ErrAttemptFenceLost) {
					t.Fatalf("corruption=%s count=%d err=%v", corruption, count, err)
				}
				var attempt model.BackupAssetExportAttempt
				if err := b.fixture.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
					t.Fatal(err)
				}
				if !attempt.IsCurrent || attempt.State != string(AttemptActive) || attempt.FailureCategory != "" {
					t.Fatalf("corrupt candidate mutated attempt=%+v", attempt)
				}
			})
		}
	})
}

func assertExpiredWorkerCancellationPending(t *testing.T, db *gorm.DB, jobID, attemptID string) {
	t.Helper()
	var job model.BackupAssetExportJob
	var attempt model.BackupAssetExportAttempt
	if err := db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attempt, "id = ?", attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionCancelRequested) || job.ErrorCategory != "" || job.CurrentAttemptID == nil ||
		*job.CurrentAttemptID != attemptID || !attempt.IsCurrent || attempt.State != string(AttemptActive) ||
		attempt.FailureCategory != "" || attempt.FinishedAt != nil {
		t.Fatalf("cancel pending job=%+v attempt=%+v", job, attempt)
	}
}
