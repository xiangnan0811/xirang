package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type contentLeaseControllerSpy struct {
	mu               sync.Mutex
	lease            backupasset.Lease
	acquireRequests  []backupasset.AcquireLeaseRequest
	renewFences      []backupasset.LeaseFence
	validateFences   []backupasset.LeaseFence
	releaseFences    []backupasset.LeaseFence
	releaseContexts  []contentLeaseReleaseContextSnapshot
	takeoverRequests []backupasset.TakeoverLeaseRequest
	renewErr         error
	validateErr      error
	releaseErr       error
	takeoverErr      error
	renewAt          time.Time
}

type contentLeaseReleaseContextSnapshot struct {
	capturedAt  time.Time
	deadline    time.Time
	hasDeadline bool
	err         error
}

func (spy *contentLeaseControllerSpy) Acquire(_ context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	spy.acquireRequests = append(spy.acquireRequests, request)
	return spy.lease, nil
}

func (spy *contentLeaseControllerSpy) Renew(_ context.Context, fence backupasset.LeaseFence) (backupasset.Lease, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.renewFences = append(spy.renewFences, fence)
	if spy.renewErr != nil {
		return backupasset.Lease{}, spy.renewErr
	}
	spy.lease.LeaseExpiresAt = spy.lease.LeaseExpiresAt.Add(time.Minute)
	if !spy.renewAt.IsZero() {
		spy.lease.LastHeartbeatAt = spy.renewAt
	}
	return spy.lease, nil
}

func (spy *contentLeaseControllerSpy) ValidateFence(_ context.Context, fence backupasset.LeaseFence) error {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.validateFences = append(spy.validateFences, fence)
	return spy.validateErr
}

func TestContentLeaseHeartbeatCoalescesConcurrentRenewals(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("a", 32)
	grantID := strings.Repeat("b", 32)
	controller := &contentLeaseControllerSpy{renewAt: now.Add(time.Minute), lease: backupasset.Lease{
		ID: strings.Repeat("c", 32), RecoveryPointID: pointID,
		HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID, Status: backupasset.LeaseActive,
		LeaseExpiresAt: now.Add(5 * time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		Fence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("c", 32), RecoveryPointID: pointID,
			HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID,
			AttemptID: strings.Repeat("d", 32), FenceToken: strings.Repeat("f", 64),
		},
	}}
	session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)}, GrantID: grantID,
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make(chan error, callers)
	var workers sync.WaitGroup
	for range callers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			binding, heartbeatErr := session.Heartbeat(context.Background(), now.Add(time.Minute), time.Minute)
			if heartbeatErr == nil && !binding.LeaseExpiresAt.Equal(now.Add(6*time.Minute)) {
				heartbeatErr = fmt.Errorf("lease expiry=%s", binding.LeaseExpiresAt)
			}
			results <- heartbeatErr
		}()
	}
	workers.Wait()
	close(results)
	for heartbeatErr := range results {
		if heartbeatErr != nil {
			t.Fatal(heartbeatErr)
		}
	}
	controller.mu.Lock()
	renewCount := len(controller.renewFences)
	controller.mu.Unlock()
	if renewCount != 1 {
		t.Fatalf("renew calls=%d want=1", renewCount)
	}
}

func (spy *contentLeaseControllerSpy) Release(ctx context.Context, fence backupasset.LeaseFence) error {
	deadline, hasDeadline := ctx.Deadline()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.releaseFences = append(spy.releaseFences, fence)
	spy.releaseContexts = append(spy.releaseContexts, contentLeaseReleaseContextSnapshot{
		capturedAt: time.Now(), deadline: deadline, hasDeadline: hasDeadline, err: ctx.Err(),
	})
	return spy.releaseErr
}

func (spy *contentLeaseControllerSpy) Takeover(_ context.Context, request backupasset.TakeoverLeaseRequest) (backupasset.Lease, error) {
	spy.takeoverRequests = append(spy.takeoverRequests, request)
	if spy.takeoverErr != nil {
		return backupasset.Lease{}, spy.takeoverErr
	}
	return spy.lease, nil
}

func TestContentLeaseRequestsIndependentDeadlineAndKeepsGrantDeadlineSeparate(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("a", 32)
	grantID := strings.Repeat("b", 32)
	fenceToken := strings.Repeat("f", 64)
	lease := backupasset.Lease{
		ID: strings.Repeat("c", 32), RecoveryPointID: pointID,
		HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID, Status: backupasset.LeaseActive,
		LeaseExpiresAt: now.Add(5 * time.Minute), AbsoluteDeadline: now.Add(4 * time.Hour), LastHeartbeatAt: now,
		Fence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("c", 32), RecoveryPointID: pointID,
			HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID,
			AttemptID: strings.Repeat("d", 32), FenceToken: fenceToken,
		},
	}
	controller := &contentLeaseControllerSpy{lease: lease}
	session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{
		Ref:     backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)},
		GrantID: grantID,
	})
	if err != nil {
		t.Fatalf("AcquireContentLease: %v", err)
	}
	if len(controller.acquireRequests) != 1 {
		t.Fatalf("acquire request count=%d want=1", len(controller.acquireRequests))
	}
	request := controller.acquireRequests[0]
	if request.RecoveryPointID != pointID || request.HolderType != backupasset.LeaseHolderContentSession ||
		request.OwnerID != grantID || !request.AbsoluteDeadline.IsZero() {
		t.Fatalf("content lease request point_match=%v holder=%q owner_match=%v deadline_zero=%v",
			request.RecoveryPointID == pointID, request.HolderType, request.OwnerID == grantID, request.AbsoluteDeadline.IsZero())
	}
	requestType := reflect.TypeOf(ContentLeaseRequest{})
	if requestType.NumField() != 2 || requestType.Field(0).Name != "Ref" || requestType.Field(1).Name != "GrantID" {
		t.Fatalf("ContentLeaseRequest field count=%d expected_names=%v", requestType.NumField(), requestType.NumField() == 2 && requestType.Field(0).Name == "Ref" && requestType.Field(1).Name == "GrantID")
	}
	wantHash := sha256.Sum256([]byte(fenceToken))
	binding := session.Binding()
	if binding.LeaseID != lease.ID || binding.AttemptID != lease.Fence.AttemptID ||
		binding.FenceTokenHash != hex.EncodeToString(wantHash[:]) ||
		!binding.AbsoluteDeadline.Equal(lease.AbsoluteDeadline) || !binding.LeaseExpiresAt.Equal(lease.LeaseExpiresAt) {
		t.Fatalf("content lease binding lease_match=%v attempt_match=%v token_hash_match=%v expiry_match=%v deadline_match=%v",
			binding.LeaseID == lease.ID, binding.AttemptID == lease.Fence.AttemptID,
			binding.FenceTokenHash == hex.EncodeToString(wantHash[:]), binding.LeaseExpiresAt.Equal(lease.LeaseExpiresAt),
			binding.AbsoluteDeadline.Equal(lease.AbsoluteDeadline))
	}
	for name, value := range map[string]any{"session": session, "binding": binding} {
		payload, err := json.Marshal(value)
		if err != nil || string(payload) != "{}" || strings.Contains(string(payload), fenceToken) {
			t.Fatalf("private %s escaped: payload=%s err=%v", name, payload, err)
		}
	}
	grantDeadlines, err := ResolveGrantDeadlines(GrantDeadlineInput{
		Now: now, SessionExpiresAt: now.Add(3 * time.Hour), ProfileExpiresAt: now.Add(20 * time.Minute),
		LeaseDeadline: binding.AbsoluteDeadline, IdleTTL: 5 * time.Minute,
	})
	if err != nil || !grantDeadlines.AbsoluteExpiresAt.Equal(now.Add(20*time.Minute)) ||
		!binding.AbsoluteDeadline.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("grant deadline err=%v grant_deadline_match=%v lease_deadline_match=%v", err,
			grantDeadlines.AbsoluteExpiresAt.Equal(now.Add(20*time.Minute)), binding.AbsoluteDeadline.Equal(now.Add(4*time.Hour)))
	}
	if renewed, err := session.Renew(context.Background()); err != nil || !renewed.LeaseExpiresAt.Equal(lease.LeaseExpiresAt.Add(time.Minute)) {
		t.Fatalf("renew content lease err=%v expiry_match=%v", err, renewed.LeaseExpiresAt.Equal(lease.LeaseExpiresAt.Add(time.Minute)))
	}
	if err := session.Validate(context.Background()); err != nil {
		t.Fatalf("validate content lease: %v", err)
	}
	if err := session.Release(context.Background()); err != nil {
		t.Fatalf("release content lease: %v", err)
	}
	if err := session.Release(context.Background()); err != nil {
		t.Fatalf("second release content lease: %v", err)
	}
	if len(controller.renewFences) != 1 || len(controller.validateFences) != 1 || len(controller.releaseFences) != 1 {
		t.Fatalf("content lease lifecycle renew=%d validate=%d release=%d", len(controller.renewFences), len(controller.validateFences), len(controller.releaseFences))
	}
	if _, err := session.Renew(context.Background()); !errors.Is(err, ErrContentLeaseClosed) {
		t.Fatalf("renew after release error=%v", err)
	}
}

func TestContentLeaseSessionReleaseRetriesFailureAndMemoizesOnlySuccess(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("a", 32)
	grantID := strings.Repeat("b", 32)
	leaseID := strings.Repeat("c", 32)
	transientErr := errors.New("closed transient content lease release failure")
	controller := &contentLeaseControllerSpy{releaseErr: transientErr, lease: backupasset.Lease{
		ID: leaseID, RecoveryPointID: pointID,
		HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID, Status: backupasset.LeaseActive,
		LeaseExpiresAt: now.Add(5 * time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		Fence: backupasset.LeaseFence{
			LeaseID: leaseID, RecoveryPointID: pointID,
			HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID,
			AttemptID: strings.Repeat("d", 32), FenceToken: strings.Repeat("f", 64),
		},
	}}
	session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)}, GrantID: grantID,
	})
	if err != nil {
		t.Fatalf("AcquireContentLease: %v", err)
	}
	if err := session.Release(context.Background()); !errors.Is(err, transientErr) {
		t.Fatalf("first transient Release error=%v", err)
	}
	controller.mu.Lock()
	controller.releaseErr = nil
	controller.mu.Unlock()
	if err := session.Release(context.Background()); err != nil {
		t.Errorf("retry Release after transient failure: %v", err)
	}
	if err := session.Release(context.Background()); err != nil {
		t.Errorf("memoized successful Release: %v", err)
	}
	controller.mu.Lock()
	releaseCalls := len(controller.releaseFences)
	controller.mu.Unlock()
	if releaseCalls != 2 {
		t.Errorf("Release calls=%d, want one failure plus one successful retry", releaseCalls)
	}
}

func TestContentLeaseDoesNotReuseExpiredPublicationDeadline(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc: func() time.Time { return now },
		Logger:  logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RecoveryPointLease{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	pointID := strings.Repeat("a", 32)
	publicationDeadline := now.Add(time.Hour)
	publicationLease, err := leaseService.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderPointPublication,
		OwnerID: strings.Repeat("9", 32), AbsoluteDeadline: publicationDeadline,
	})
	if err != nil {
		t.Fatalf("acquire publication lease: %v", err)
	}
	if err := leaseService.Release(context.Background(), publicationLease.Fence); err != nil {
		t.Fatalf("release publication lease: %v", err)
	}
	now = now.Add(2 * time.Hour)
	contentSession, err := AcquireContentLease(context.Background(), leaseService, ContentLeaseRequest{
		Ref:     backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)},
		GrantID: strings.Repeat("b", 32),
	})
	if err != nil {
		t.Fatalf("acquire content lease after publication deadline: %v", err)
	}
	t.Cleanup(func() { _ = contentSession.Release(context.Background()) })
	wantDeadline := now.Add(7 * 24 * time.Hour)
	if got := contentSession.Binding().AbsoluteDeadline; !got.Equal(wantDeadline) || got.Equal(publicationDeadline) {
		t.Fatalf("content deadline=%s want independent=%s publication=%s", got, wantDeadline, publicationDeadline)
	}
}

func TestContentLeaseCleanupTakeoverCannotBecomeDeliverySession(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	grantID := strings.Repeat("b", 32)
	leaseID := strings.Repeat("c", 32)
	controller := &contentLeaseControllerSpy{lease: backupasset.Lease{
		ID: leaseID, RecoveryPointID: strings.Repeat("a", 32), HolderType: backupasset.LeaseHolderContentSession,
		OwnerID: grantID, Status: backupasset.LeaseActive, LeaseExpiresAt: now.Add(5 * time.Minute),
		AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		Fence: backupasset.LeaseFence{
			LeaseID: leaseID, RecoveryPointID: strings.Repeat("a", 32), HolderType: backupasset.LeaseHolderContentSession,
			OwnerID: grantID, AttemptID: strings.Repeat("d", 32), FenceToken: strings.Repeat("f", 64),
		},
	}}
	cleanup, err := TakeoverContentLeaseForCleanup(context.Background(), controller, leaseID, grantID)
	if err != nil {
		t.Fatalf("TakeoverContentLeaseForCleanup: %v", err)
	}
	if len(controller.takeoverRequests) != 1 || controller.takeoverRequests[0].LeaseID != leaseID || controller.takeoverRequests[0].OwnerID != grantID {
		t.Fatalf("cleanup takeover count=%d exact_binding=%v", len(controller.takeoverRequests), len(controller.takeoverRequests) == 1 && controller.takeoverRequests[0].LeaseID == leaseID && controller.takeoverRequests[0].OwnerID == grantID)
	}
	if cleanup.Binding().LeaseID != leaseID {
		t.Fatalf("cleanup binding lease_match=%v", cleanup.Binding().LeaseID == leaseID)
	}
	if err := cleanup.Release(context.Background()); err != nil {
		t.Fatalf("release cleanup lease: %v", err)
	}
	if err := cleanup.Release(context.Background()); err != nil || len(controller.releaseFences) != 1 {
		t.Fatalf("second cleanup release err=%v releases=%d", err, len(controller.releaseFences))
	}
}

func TestContentLeaseDetachedCleanupHasFiniteDeadline(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("a", 32)
	grantID := strings.Repeat("b", 32)
	validLease := backupasset.Lease{
		ID: strings.Repeat("c", 32), RecoveryPointID: pointID,
		HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID, Status: backupasset.LeaseActive,
		LeaseExpiresAt: now.Add(5 * time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		Fence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("c", 32), RecoveryPointID: pointID,
			HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID,
			AttemptID: strings.Repeat("d", 32), FenceToken: strings.Repeat("f", 64),
		},
	}
	assertBoundedDetached := func(t *testing.T, controller *contentLeaseControllerSpy) {
		t.Helper()
		controller.mu.Lock()
		defer controller.mu.Unlock()
		if len(controller.releaseContexts) != 1 {
			t.Fatalf("release contexts=%d want=1", len(controller.releaseContexts))
		}
		snapshot := controller.releaseContexts[0]
		if snapshot.err != nil || !snapshot.hasDeadline || !snapshot.deadline.After(snapshot.capturedAt) ||
			snapshot.deadline.After(snapshot.capturedAt.Add(6*time.Second)) {
			t.Fatalf("cleanup context err=%v has_deadline=%v deadline_future=%v deadline_bounded=%v",
				snapshot.err, snapshot.hasDeadline, snapshot.deadline.After(snapshot.capturedAt), snapshot.deadline.Before(snapshot.capturedAt.Add(6*time.Second)))
		}
	}

	t.Run("invalid acquire rollback", func(t *testing.T) {
		lease := validLease
		lease.Status = backupasset.LeaseReleased
		controller := &contentLeaseControllerSpy{lease: lease}
		parent, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := AcquireContentLease(parent, controller, ContentLeaseRequest{
			Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)}, GrantID: grantID,
		})
		if !errors.Is(err, ErrInvalidContentLease) {
			t.Fatalf("AcquireContentLease error=%v", err)
		}
		assertBoundedDetached(t, controller)
	})

	t.Run("invalid takeover rollback", func(t *testing.T) {
		controller := &contentLeaseControllerSpy{lease: validLease}
		parent, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := TakeoverContentLeaseForCleanup(parent, controller, strings.Repeat("1", 32), grantID)
		if !errors.Is(err, ErrInvalidContentLease) {
			t.Fatalf("TakeoverContentLeaseForCleanup error=%v", err)
		}
		assertBoundedDetached(t, controller)
	})

	t.Run("close", func(t *testing.T) {
		controller := &contentLeaseControllerSpy{lease: validLease}
		session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{
			Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)}, GrantID: grantID,
		})
		if err != nil {
			t.Fatalf("AcquireContentLease: %v", err)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		assertBoundedDetached(t, controller)
	})
}

func TestContentLeaseInvalidCleanupReturnsReleaseFailure(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("a", 32)
	grantID := strings.Repeat("b", 32)
	releaseErr := errors.New("release failed")
	lease := backupasset.Lease{
		ID: strings.Repeat("c", 32), RecoveryPointID: pointID,
		HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID, Status: backupasset.LeaseReleased,
		LeaseExpiresAt: now.Add(5 * time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		Fence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("c", 32), RecoveryPointID: pointID,
			HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID,
			AttemptID: strings.Repeat("d", 32), FenceToken: strings.Repeat("f", 64),
		},
	}

	t.Run("acquire", func(t *testing.T) {
		controller := &contentLeaseControllerSpy{lease: lease, releaseErr: releaseErr}
		_, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{
			Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)}, GrantID: grantID,
		})
		if !errors.Is(err, ErrInvalidContentLease) || !errors.Is(err, releaseErr) {
			t.Fatalf("AcquireContentLease error=%v", err)
		}
	})

	t.Run("takeover", func(t *testing.T) {
		lease.Status = backupasset.LeaseActive
		controller := &contentLeaseControllerSpy{lease: lease, releaseErr: releaseErr}
		_, err := TakeoverContentLeaseForCleanup(context.Background(), controller, strings.Repeat("1", 32), grantID)
		if !errors.Is(err, ErrInvalidContentLease) || !errors.Is(err, releaseErr) {
			t.Fatalf("TakeoverContentLeaseForCleanup error=%v", err)
		}
	})
}
