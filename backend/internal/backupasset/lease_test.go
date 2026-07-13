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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	clock := &leaseTestClock{now: time.Date(2026, 7, 13, 5, 6, 7, 0, time.UTC)}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc: clock.Now,
		Logger:  logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open lease database: %v", err)
	}
	if err := db.AutoMigrate(&model.RecoveryPointLease{}); err != nil {
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
