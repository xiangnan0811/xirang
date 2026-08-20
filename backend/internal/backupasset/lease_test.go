package backupasset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var leaseTestDBSequence atomic.Uint64

func TestLeaseAcquireRenewRelease(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, LeaseConfig{
		Duration:         5 * time.Minute,
		Heartbeat:        time.Minute,
		AbsoluteDeadline: 2 * time.Hour,
	})
	ctx := context.Background()
	lease, err := service.Acquire(ctx, standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.ID == "" || lease.Fence.AttemptID == "" || len(lease.Fence.FenceToken) != 64 {
		t.Fatalf("Acquire returned incomplete identity/fence: %+v", lease)
	}
	if !lease.LeaseExpiresAt.Equal(clock.Now().Add(5*time.Minute)) || !lease.AbsoluteDeadline.Equal(clock.Now().Add(2*time.Hour)) {
		t.Fatalf("Acquire deadlines mismatch: %+v", lease)
	}

	clock.Advance(time.Minute)
	renewed, err := service.Renew(ctx, lease.Fence)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !renewed.LeaseExpiresAt.Equal(clock.Now().Add(5*time.Minute)) || !renewed.LastHeartbeatAt.Equal(clock.Now()) {
		t.Fatalf("Renew did not extend the short lease: %+v", renewed)
	}
	if err := service.ValidateFence(ctx, renewed.Fence); err != nil {
		t.Fatalf("ValidateFence after renew: %v", err)
	}
	if err := service.Release(ctx, renewed.Fence); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := service.ValidateFence(ctx, renewed.Fence); !errors.Is(err, ErrLeaseFenceLost) {
		t.Fatalf("released fence got %v, want ErrLeaseFenceLost", err)
	}
}

func TestRecoveryPointSourceLifecycleAttemptStageFence(t *testing.T) {
	_, _, db := newLeaseTestHarness(t, standardLeaseConfig())
	ctx := context.Background()
	pointID := strings.Repeat("d", 32)
	attemptID := strings.Repeat("e", 32)
	point := model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("f", 32)}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed recovery point: %v", err)
	}
	attempt := model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID,
		Operation: string(LifecycleMutableRetire), Phase: string(LifecyclePhaseRevoking),
	}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}

	request := SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: LifecycleMutableRetire, Stage: SourceLifecyclePrepare,
	}
	assertFence := func(want error) {
		t.Helper()
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return ValidateSourceLifecycleAttemptTx(ctx, tx, request)
		})
		if want == nil && err != nil {
			t.Fatalf("valid lifecycle stage fence rejected: %v", err)
		}
		if want != nil && !errors.Is(err, want) {
			t.Fatalf("lifecycle stage fence error=%v, want %v", err, want)
		}
	}
	assertFence(nil)

	request.Stage = SourceLifecycleCleanup
	assertFence(ErrConflict)
	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).
		Update("phase", LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle attempt: %v", err)
	}
	assertFence(nil)

	request.Operation = LifecycleExplicitPurge
	assertFence(ErrConflict)
	request.Operation = LifecycleMutableRetire
	request.LifecycleAttemptID = strings.Repeat("c", 32)
	assertFence(ErrConflict)
}

func TestValidateRecoveryPointWriteAdmissionTxEnforcesClosedSemanticsStateMatrix(t *testing.T) {
	_, _, db := newLeaseTestHarness(t, standardLeaseConfig())
	tests := []struct {
		name      string
		semantics PointVersionSemantics
		state     RecoveryPointState
		wantErr   bool
	}{
		{name: "mutable observed", semantics: PointMutableHead, state: RecoveryPointObserved},
		{name: "native committed", semantics: PointNativeSnapshot, state: RecoveryPointCommitted},
		{name: "native degraded", semantics: PointNativeSnapshot, state: RecoveryPointDegraded},
		{name: "manifest committed", semantics: PointXirangManifest, state: RecoveryPointCommitted},
		{name: "manifest degraded", semantics: PointXirangManifest, state: RecoveryPointDegraded},
		{name: "imported committed", semantics: PointImportedBaseline, state: RecoveryPointCommitted},
		{name: "imported degraded", semantics: PointImportedBaseline, state: RecoveryPointDegraded},
		{name: "unknown committed", semantics: PointVersionSemantics("unknown"), state: RecoveryPointCommitted, wantErr: true},
		{name: "unknown degraded", semantics: PointVersionSemantics("unknown"), state: RecoveryPointDegraded, wantErr: true},
		{name: "mutable committed", semantics: PointMutableHead, state: RecoveryPointCommitted, wantErr: true},
		{name: "mutable degraded", semantics: PointMutableHead, state: RecoveryPointDegraded, wantErr: true},
		{name: "native observed", semantics: PointNativeSnapshot, state: RecoveryPointObserved, wantErr: true},
		{name: "manifest observed", semantics: PointXirangManifest, state: RecoveryPointObserved, wantErr: true},
		{name: "imported observed", semantics: PointImportedBaseline, state: RecoveryPointObserved, wantErr: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pointID := fmt.Sprintf("%032x", index+1)
			if err := db.Create(&model.RecoveryPoint{
				ID: pointID, RepositoryID: strings.Repeat("f", 32),
				Semantics: string(test.semantics), State: string(test.state),
			}).Error; err != nil {
				t.Fatalf("seed recovery point: %v", err)
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				return ValidateRecoveryPointWriteAdmissionTx(context.Background(), tx, pointID)
			})
			if test.wantErr {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("admission error=%v, want ErrConflict", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid admission rejected: %v", err)
			}
		})
	}
}

func TestLeaseRejectsDuplicateActiveOwnerSlot(t *testing.T) {
	service, _, _ := newLeaseTestHarness(t, standardLeaseConfig())
	ctx := context.Background()
	if _, err := service.Acquire(ctx, standardAcquireLeaseRequest()); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if _, err := service.Acquire(ctx, standardAcquireLeaseRequest()); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("duplicate Acquire got %v, want ErrLeaseHeld", err)
	}
}

func TestLeaseTakeoverReplacesAttemptAndFence(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, standardLeaseConfig())
	ctx := context.Background()
	before, err := service.Acquire(ctx, standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clock.Advance(6 * time.Minute)
	after, err := service.Takeover(ctx, TakeoverLeaseRequest{LeaseID: before.ID, OwnerID: before.OwnerID})
	if err != nil {
		t.Fatalf("Takeover: %v", err)
	}
	if after.ID != before.ID || after.Fence.AttemptID == before.Fence.AttemptID || after.Fence.FenceToken == before.Fence.FenceToken {
		t.Fatalf("takeover identity/fence mismatch: before=%+v after=%+v", before, after)
	}
	if !after.AbsoluteDeadline.Equal(before.AbsoluteDeadline) {
		t.Fatalf("takeover changed absolute deadline: before=%s after=%s", before.AbsoluteDeadline, after.AbsoluteDeadline)
	}
	if err := service.ValidateFence(ctx, before.Fence); !errors.Is(err, ErrLeaseFenceLost) {
		t.Fatalf("old fence got %v, want ErrLeaseFenceLost", err)
	}
	if err := service.ValidateFence(ctx, after.Fence); err != nil {
		t.Fatalf("new fence rejected: %v", err)
	}
}

func TestSearchIndexLeaseTakeoverRejectsOldFence(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, standardLeaseConfig())
	request := standardAcquireLeaseRequest()
	request.HolderType = LeaseHolderSearchIndex
	request.OwnerID = "search-index-worker"
	before, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("Acquire search index lease: %v", err)
	}
	clock.Advance(6 * time.Minute)
	after, err := service.Takeover(context.Background(), TakeoverLeaseRequest{LeaseID: before.ID, OwnerID: before.OwnerID})
	if err != nil {
		t.Fatalf("Takeover search index lease: %v", err)
	}
	if err := service.ValidateFence(context.Background(), before.Fence); !errors.Is(err, ErrLeaseFenceLost) {
		t.Fatalf("old search index fence got %v, want ErrLeaseFenceLost", err)
	}
	if err := service.ValidateFence(context.Background(), after.Fence); err != nil {
		t.Fatalf("new search index fence rejected: %v", err)
	}
}

func TestRetentionWorkerLeaseHolderIsValid(t *testing.T) {
	if !validLeaseHolderTypes[LeaseHolderRetentionWorker] {
		t.Fatal("retention_worker lease holder is not valid")
	}
	service, _, _ := newLeaseTestHarness(t, standardLeaseConfig())
	request := standardAcquireLeaseRequest()
	request.HolderType = LeaseHolderRetentionWorker
	request.OwnerID = "retention-worker"
	lease, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("Acquire retention worker lease: %v", err)
	}
	if lease.HolderType != LeaseHolderRetentionWorker {
		t.Fatalf("retention worker holder=%q", lease.HolderType)
	}
}

func TestLeaseOldFenceCannotPublishAfterTakeover(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, standardLeaseConfig())
	ctx := context.Background()
	oldLease, err := service.Acquire(ctx, standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clock.Advance(6 * time.Minute)
	newLease, err := service.Takeover(ctx, TakeoverLeaseRequest{LeaseID: oldLease.ID, OwnerID: oldLease.OwnerID})
	if err != nil {
		t.Fatalf("Takeover: %v", err)
	}

	var publications int
	publish := func(fence LeaseFence) error {
		if err := service.ValidateFence(ctx, fence); err != nil {
			return err
		}
		publications++
		return nil
	}
	if err := publish(oldLease.Fence); !errors.Is(err, ErrLeaseFenceLost) {
		t.Fatalf("old fence publish got %v, want ErrLeaseFenceLost", err)
	}
	if publications != 0 {
		t.Fatal("old fence reached publisher")
	}
	if err := publish(newLease.Fence); err != nil {
		t.Fatalf("new fence publish: %v", err)
	}
	if publications != 1 {
		t.Fatalf("publication count=%d, want 1", publications)
	}
}

func TestLeaseRenewCannotCrossAbsoluteDeadline(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, LeaseConfig{
		Duration:         10 * time.Minute,
		Heartbeat:        time.Minute,
		AbsoluteDeadline: 12 * time.Minute,
	})
	ctx := context.Background()
	lease, err := service.Acquire(ctx, standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clock.Advance(8 * time.Minute)
	renewed, err := service.Renew(ctx, lease.Fence)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !renewed.LeaseExpiresAt.Equal(lease.AbsoluteDeadline) || renewed.LeaseExpiresAt.After(lease.AbsoluteDeadline) {
		t.Fatalf("renew crossed absolute deadline: %+v", renewed)
	}
	clock.Advance(4 * time.Minute)
	if _, err := service.Renew(ctx, renewed.Fence); !errors.Is(err, ErrLeaseDeadlineExceeded) {
		t.Fatalf("renew at absolute deadline got %v, want ErrLeaseDeadlineExceeded", err)
	}
}

func TestLeaseTakeoverCannotExtendAbsoluteDeadline(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, LeaseConfig{
		Duration:         7 * time.Minute,
		Heartbeat:        time.Minute,
		AbsoluteDeadline: 10 * time.Minute,
	})
	ctx := context.Background()
	lease, err := service.Acquire(ctx, standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clock.Advance(8 * time.Minute)
	taken, err := service.Takeover(ctx, TakeoverLeaseRequest{LeaseID: lease.ID, OwnerID: lease.OwnerID})
	if err != nil {
		t.Fatalf("Takeover: %v", err)
	}
	if !taken.AbsoluteDeadline.Equal(lease.AbsoluteDeadline) || !taken.LeaseExpiresAt.Equal(lease.AbsoluteDeadline) {
		t.Fatalf("takeover extended/crossed absolute deadline: before=%+v after=%+v", lease, taken)
	}
	clock.Advance(2 * time.Minute)
	if _, err := service.Takeover(ctx, TakeoverLeaseRequest{LeaseID: lease.ID, OwnerID: lease.OwnerID}); !errors.Is(err, ErrLeaseDeadlineExceeded) {
		t.Fatalf("takeover at absolute deadline got %v, want ErrLeaseDeadlineExceeded", err)
	}
}

func TestLeaseReconcileExpiredAfterRestart(t *testing.T) {
	service, clock, db := newLeaseTestHarness(t, LeaseConfig{
		Duration:         5 * time.Minute,
		Heartbeat:        time.Minute,
		AbsoluteDeadline: 10 * time.Minute,
	})
	ctx := context.Background()
	lease, err := service.Acquire(ctx, standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clock.Advance(11 * time.Minute)
	restarted, err := NewLeaseService(db, clock.Now, service.config)
	if err != nil {
		t.Fatalf("restart LeaseService: %v", err)
	}
	count, err := restarted.ReconcileExpired(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if count != 1 {
		t.Fatalf("reconciled %d leases, want 1", count)
	}
	if err := restarted.ValidateFence(ctx, lease.Fence); !errors.Is(err, ErrLeaseDeadlineExceeded) && !errors.Is(err, ErrLeaseFenceLost) {
		t.Fatalf("expired fence unexpectedly valid: %v", err)
	}
	var row model.RecoveryPointLease
	if err := db.First(&row, "id = ?", lease.ID).Error; err != nil {
		t.Fatalf("load reconciled lease: %v", err)
	}
	if row.Status != string(LeaseExpired) {
		t.Fatalf("reconciled status=%q, want expired", row.Status)
	}
	if _, err := restarted.Acquire(ctx, standardAcquireLeaseRequest()); err != nil {
		t.Fatalf("Acquire after absolute expiry/reconcile: %v", err)
	}
}

func TestLeaseConcurrentTakeoverHasSingleWinner(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, standardLeaseConfig())
	ctx := context.Background()
	lease, err := service.Acquire(ctx, standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clock.Advance(6 * time.Minute)

	const contenders = 10
	var successes atomic.Int64
	errorsCh := make(chan error, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, takeoverErr := service.Takeover(ctx, TakeoverLeaseRequest{LeaseID: lease.ID, OwnerID: lease.OwnerID})
			if takeoverErr == nil {
				successes.Add(1)
				return
			}
			errorsCh <- takeoverErr
		}()
	}
	wg.Wait()
	close(errorsCh)
	if successes.Load() != 1 {
		t.Fatalf("takeover winners=%d, want exactly 1", successes.Load())
	}
	for err := range errorsCh {
		if !errors.Is(err, ErrLeaseHeld) && !errors.Is(err, ErrLeaseFenceLost) && !errors.Is(err, ErrConflict) {
			t.Fatalf("unexpected losing takeover error: %v", err)
		}
	}
}

func TestLeaseConstraintClassificationOnlyTreatsUniqueOwnerSlotAsHeld(t *testing.T) {
	if !isLeaseConstraintConflict(errors.New("UNIQUE constraint failed: recovery_point_leases.recovery_point_id")) {
		t.Fatal("SQLite unique owner-slot conflict was not classified")
	}
	if !isLeaseConstraintConflict(errors.New("duplicate key value violates unique constraint (SQLSTATE 23505)")) {
		t.Fatal("PostgreSQL unique owner-slot conflict was not classified")
	}
	if isLeaseConstraintConflict(errors.New("FOREIGN KEY constraint failed")) {
		t.Fatal("foreign-key failure was misclassified as an active owner-slot conflict")
	}
}

func TestLeaseAcquireTxUsesSuppliedPointDeadline(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, standardLeaseConfig())
	deadline := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	request := standardAcquireLeaseRequest()
	request.AbsoluteDeadline = deadline
	first, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire first stage: %v", err)
	}
	if !first.AbsoluteDeadline.Equal(deadline) {
		t.Fatalf("first stage deadline=%s, want %s", first.AbsoluteDeadline, deadline)
	}
	if err := service.Release(context.Background(), first.Fence); err != nil {
		t.Fatalf("release first stage: %v", err)
	}
	clock.Advance(time.Minute)
	second, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire fresh stage: %v", err)
	}
	if !second.AbsoluteDeadline.Equal(deadline) {
		t.Fatalf("fresh stage deadline=%s, want %s", second.AbsoluteDeadline, deadline)
	}
}

func TestLeaseValidateAndReleaseTxShareCallerTransaction(t *testing.T) {
	service, _, db := newLeaseTestHarness(t, standardLeaseConfig())
	lease, err := service.Acquire(context.Background(), standardAcquireLeaseRequest())
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	rolledBack := db.Begin()
	if rolledBack.Error != nil {
		t.Fatalf("begin rollback transaction: %v", rolledBack.Error)
	}
	if err := service.ValidateFenceTx(context.Background(), rolledBack, lease.Fence); err != nil {
		t.Fatalf("validate fence in transaction: %v", err)
	}
	if err := service.ReleaseTx(context.Background(), rolledBack, lease.Fence); err != nil {
		t.Fatalf("release fence in transaction: %v", err)
	}
	if err := rolledBack.Rollback().Error; err != nil {
		t.Fatalf("rollback release transaction: %v", err)
	}
	if err := service.ValidateFence(context.Background(), lease.Fence); err != nil {
		t.Fatalf("rolled-back release changed persisted lease: %v", err)
	}

	committed := db.Begin()
	if committed.Error != nil {
		t.Fatalf("begin commit transaction: %v", committed.Error)
	}
	if err := service.ValidateFenceTx(context.Background(), committed, lease.Fence); err != nil {
		t.Fatalf("validate committed fence in transaction: %v", err)
	}
	if err := service.ReleaseTx(context.Background(), committed, lease.Fence); err != nil {
		t.Fatalf("release committed fence in transaction: %v", err)
	}
	if err := committed.Commit().Error; err != nil {
		t.Fatalf("commit release transaction: %v", err)
	}
	if err := service.ValidateFence(context.Background(), lease.Fence); !errors.Is(err, ErrLeaseFenceLost) {
		t.Fatalf("committed release fence error=%v, want ErrLeaseFenceLost", err)
	}
}

func TestLeaseFreshStageCannotMovePointDeadline(t *testing.T) {
	service, clock, _ := newLeaseTestHarness(t, standardLeaseConfig())
	request := standardAcquireLeaseRequest()
	request.AbsoluteDeadline = time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	first, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire first stage: %v", err)
	}
	if err := service.Release(context.Background(), first.Fence); err != nil {
		t.Fatalf("release first stage: %v", err)
	}
	clock.Advance(time.Minute)
	request.AbsoluteDeadline = request.AbsoluteDeadline.Add(time.Hour)
	if _, err := service.Acquire(context.Background(), request); err == nil {
		t.Fatal("fresh stage moved the point-wide deadline")
	}
}

func TestLeaseTakeoverTxRotatesFenceAndPreservesDeadline(t *testing.T) {
	service, clock, db := newLeaseTestHarness(t, standardLeaseConfig())
	deadline := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	request := standardAcquireLeaseRequest()
	request.AbsoluteDeadline = deadline
	before, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	clock.Advance(6 * time.Minute)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin takeover transaction: %v", tx.Error)
	}
	after, err := service.TakeoverTx(context.Background(), tx, TakeoverLeaseRequest{LeaseID: before.ID, OwnerID: before.OwnerID})
	if err != nil {
		_ = tx.Rollback().Error
		t.Fatalf("take over lease in transaction: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit takeover transaction: %v", err)
	}
	if after.Fence.AttemptID == before.Fence.AttemptID || after.Fence.FenceToken == before.Fence.FenceToken {
		t.Fatalf("takeover did not rotate fence: before=%+v after=%+v", before.Fence, after.Fence)
	}
	if !after.AbsoluteDeadline.Equal(deadline) {
		t.Fatalf("takeover deadline=%s, want %s", after.AbsoluteDeadline, deadline)
	}
}

type leaseTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *leaseTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *leaseTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func newLeaseTestHarness(t *testing.T, config LeaseConfig) (*LeaseService, *leaseTestClock, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"), leaseTestDBSequence.Add(1))
	clock := &leaseTestClock{now: time.Date(2026, 7, 13, 5, 6, 7, 0, time.UTC)}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc: clock.Now,
		Logger:  logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open lease database: %v", err)
	}
	if err := db.AutoMigrate(&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{}); err != nil {
		t.Fatalf("migrate recovery point leases: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_point_leases_active_owner_slot ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatalf("create active owner slot index: %v", err)
	}
	service, err := NewLeaseService(db, clock.Now, config)
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	return service, clock, db
}

func standardLeaseConfig() LeaseConfig {
	return LeaseConfig{
		Duration:         5 * time.Minute,
		Heartbeat:        time.Minute,
		AbsoluteDeadline: 2 * time.Hour,
	}
}

func standardAcquireLeaseRequest() AcquireLeaseRequest {
	return AcquireLeaseRequest{
		RecoveryPointID: strings.Repeat("a", 32),
		HolderType:      LeaseHolderCatalogBuild,
		OwnerID:         "catalog-worker-a",
	}
}
