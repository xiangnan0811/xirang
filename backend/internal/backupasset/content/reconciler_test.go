package content

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestContentStartupReconcilesReservationBeforeRevokingGrantAndReleasingLease(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	reservation := harness.reserve(t, strings.Repeat("9", 32), 40)
	lease := &reconcilerLeaseFake{}
	audit := &reconcilerAuditFake{}
	metrics := newBrokerMetricsFake()
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: audit, Lease: lease,
		Now: harness.clock.Now, BatchSize: 100, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var request model.BackupAssetDeliveryRequest
	if err := harness.db.First(&request, "id = ?", reservation.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(RequestReconciled) || request.FailureCode != string(RequestFailureReconciledCrash) {
		t.Fatalf("request=%+v", request)
	}
	grant := harness.loadGrant(t)
	if grant.State != string(DeliveryRevoked) || grant.RevocationReason != "process_restarted" ||
		grant.DeliveredBytes != 40 || grant.ReservedBytes != 0 || grant.InFlight != 0 {
		t.Fatalf("grant=%+v", grant)
	}
	if len(lease.takeovers) != 1 || lease.takeovers[0].LeaseID != grant.LeaseID || len(lease.releases) != 1 {
		t.Fatalf("lease takeover=%+v releases=%+v", lease.takeovers, lease.releases)
	}
	if len(audit.grants) != 1 || audit.grants[0] != grant.ID {
		t.Fatalf("audit flushes=%v", audit.grants)
	}
}

func TestContentStartupRunsAllCleanupStagesWhenLeaseAndAuditFail(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	if err := harness.service.RecordBlocked(context.Background(), BlockedRequest{
		RequestID: strings.Repeat("9", 32), GrantID: harness.grant.ID, Method: http.MethodGet,
		Status: http.StatusRequestedRangeNotSatisfiable, FailureCode: RequestFailureInvalidRange,
		RangeRequested: true,
	}); err != nil {
		t.Fatal(err)
	}
	lease := &reconcilerLeaseFake{takeoverErr: errors.New("lease unavailable")}
	audit := &reconcilerAuditFake{err: errors.New("audit unavailable")}
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: audit, Lease: lease,
		Now: harness.clock.Now, BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Startup(context.Background()); err == nil {
		t.Fatal("startup unexpectedly ignored cleanup failures")
	}
	grant := harness.loadGrant(t)
	if grant.State != string(DeliveryRevoked) || len(lease.takeovers) != 1 || len(audit.grants) != 1 {
		t.Fatalf("cleanup did not continue grant=%+v takeovers=%v audits=%v", grant, lease.takeovers, audit.grants)
	}
}

func TestContentStartupDefersHeldCrashLeaseAndPeriodicReconcileRetriesCleanup(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	seedReconcilerLeaseRow(t, harness)
	lease := &reconcilerLeaseFake{takeoverErr: backupasset.ErrLeaseHeld}
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: &reconcilerAuditFake{}, Lease: lease,
		Now: harness.clock.Now, BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Startup(context.Background()); err != nil {
		t.Fatalf("short held crash lease should be safely deferred: %v", err)
	}
	if grant := harness.loadGrant(t); grant.State != string(DeliveryRevoked) {
		t.Fatalf("stale grant state=%s", grant.State)
	}
	if len(lease.takeovers) != 1 || len(lease.releases) != 0 {
		t.Fatalf("startup takeovers=%v releases=%v", lease.takeovers, lease.releases)
	}

	lease.takeoverErr = nil
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lease.takeovers) != 2 || len(lease.releases) != 1 {
		t.Fatalf("periodic cleanup takeovers=%v releases=%v", lease.takeovers, lease.releases)
	}
}

func TestContentPeriodicReconcileRetriesTerminalLeaseReleaseFailure(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	seedReconcilerLeaseRow(t, harness)
	lease := &reconcilerLeaseFake{releaseErr: errors.New("release unavailable")}
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: &reconcilerAuditFake{}, Lease: lease,
		Now: harness.clock.Now, BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Startup(context.Background()); err == nil {
		t.Fatal("startup unexpectedly ignored lease release failure")
	}
	if len(lease.takeovers) != 1 || len(lease.releases) != 1 {
		t.Fatalf("startup takeovers=%v releases=%v", lease.takeovers, lease.releases)
	}

	lease.releaseErr = nil
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lease.takeovers) != 2 || len(lease.releases) != 2 {
		t.Fatalf("retried takeovers=%v releases=%v", lease.takeovers, lease.releases)
	}
}

func TestContentPeriodicReconcileExpiresOnlyDueGrantsWithoutStartupRevocation(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	if err := harness.service.RecordBlocked(context.Background(), BlockedRequest{
		RequestID: strings.Repeat("9", 32), GrantID: harness.grant.ID, Method: http.MethodGet,
		Status: http.StatusRequestedRangeNotSatisfiable, FailureCode: RequestFailureInvalidRange,
		RangeRequested: true,
	}); err != nil {
		t.Fatal(err)
	}
	expired := harness.grant
	expired.ID = strings.Repeat("c", 32)
	expired.DeliveryID = strings.Repeat("d", 32)
	expired.LeaseID = strings.Repeat("e", 32)
	expired.AbsoluteExpiresAt = harness.clock.Now().Add(-time.Second)
	expired.IdleExpiresAt = harness.clock.Now().Add(-time.Second)
	expired.AuditState = "none"
	if err := harness.db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	lease := &reconcilerLeaseFake{}
	audit := &reconcilerAuditFake{}
	metrics := newBrokerMetricsFake()
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: audit, Lease: lease,
		Now: harness.clock.Now, BatchSize: 100, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	healthy := harness.loadGrant(t)
	if healthy.State != string(DeliveryActive) {
		t.Fatalf("healthy grant state=%s", healthy.State)
	}
	if err := harness.db.First(&expired, "id = ?", expired.ID).Error; err != nil {
		t.Fatal(err)
	}
	if expired.State != string(DeliveryExpired) || expired.RevocationReason != "expired" {
		t.Fatalf("expired grant state=%s reason=%s", expired.State, expired.RevocationReason)
	}
	if lease.reconcileExpiredCalls != 1 || len(lease.takeovers) != 0 {
		t.Fatalf("periodic lease reconcile=%d takeovers=%d", lease.reconcileExpiredCalls, len(lease.takeovers))
	}
	if len(audit.grants) != 0 {
		t.Fatalf("periodic audit flushes=%v", audit.grants)
	}
	if age := metrics.reconciliationAge(); age != 0 {
		t.Fatalf("reconciliation age=%s want=0", age)
	}
}

func TestContentPeriodicReconcileDefersActiveGrantAudit(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	if err := harness.service.RecordBlocked(context.Background(), BlockedRequest{
		RequestID: strings.Repeat("9", 32), GrantID: harness.grant.ID, Method: http.MethodGet,
		Status: http.StatusRequestedRangeNotSatisfiable, FailureCode: RequestFailureInvalidRange,
		RangeRequested: true,
	}); err != nil {
		t.Fatal(err)
	}
	audit := &reconcilerAuditFake{}
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: audit, Lease: &reconcilerLeaseFake{},
		Now: harness.clock.Now, BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(audit.grants) != 0 {
		t.Fatalf("active grant audit flushed before the final session summary: %v", audit.grants)
	}
}

func TestContentPeriodicReconcileHonorsAuditRetryBackoff(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	now := harness.clock.Now()
	nextAttemptAt := now.Add(time.Minute)
	if err := harness.db.Model(&model.BackupAssetDeliveryGrant{}).Where("id = ?", harness.grant.ID).
		Updates(map[string]any{
			"state": DeliveryRevoked, "revocation_reason": "process_restarted", "revoked_at": now,
			"request_count": 1,
			"audit_state":   "retry_wait", "audit_request_count": 1, "audit_failure_count": 1,
			"audit_failure_code": "audit_write_failed", "audit_attempt_count": 1,
			"audit_next_attempt_at": nextAttemptAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	audit := &reconcilerAuditFake{}
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: audit, Lease: &reconcilerLeaseFake{},
		Now: harness.clock.Now, BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(audit.grants) != 0 {
		t.Fatalf("audit retried before audit_next_attempt_at: %v", audit.grants)
	}

	harness.clock.Advance(time.Minute)
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(audit.grants) != 1 || audit.grants[0] != harness.grant.ID {
		t.Fatalf("due audit retry was not flushed exactly once: %v", audit.grants)
	}
}

func TestContentStartupPersistsCrashAuditBeforeFlush(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	reservation := harness.reserve(t, strings.Repeat("9", 32), 40)
	audit := &reconcilerAuditFake{}
	reconciler, err := NewReconciler(ReconcilerDependencies{
		DB: harness.db, Budget: harness.service, Audit: audit, Lease: &reconcilerLeaseFake{},
		Now: harness.clock.Now, BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var request model.BackupAssetDeliveryRequest
	if err := harness.db.First(&request, "id = ?", reservation.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(RequestReconciled) {
		t.Fatalf("crashed request state=%s", request.State)
	}
	grant := harness.loadGrant(t)
	if grant.AuditState != "pending" || grant.AuditRequestCount != 1 || grant.AuditFailureCount != 1 {
		t.Fatalf("crash audit summary was not persisted atomically: %+v", grant)
	}
	if len(audit.grants) != 1 || audit.grants[0] != grant.ID {
		t.Fatalf("crash audit flushes=%v", audit.grants)
	}
}

func TestBrokerShutdownStopsIssuanceRevokesGrantsAndReleasesEveryLease(t *testing.T) {
	harness := newBrokerTestHarness(t)
	if _, err := harness.broker.Issue(context.Background(), harness.issueRequest()); err != nil {
		t.Fatal(err)
	}
	if err := harness.broker.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(harness.lease.releaseFences) != 1 {
		t.Fatalf("shutdown releases=%d", len(harness.lease.releaseFences))
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != string(DeliveryRevoked) || grant.RevocationReason != "shutdown" {
		t.Fatalf("shutdown grant=%+v", grant)
	}
	if _, err := harness.broker.Issue(context.Background(), harness.issueRequest()); !errors.Is(err, ErrBrokerClosed) {
		t.Fatalf("post-shutdown issue error=%v", err)
	}
}

func TestBrokerRevokeSessionCancelsActiveReadBeforeReleasingLease(t *testing.T) {
	harness := newBrokerTestHarness(t)
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	harness.source.blockReads = true
	harness.source.readStarted = make(chan struct{})
	harness.source.readCanceled = make(chan struct{})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- harness.broker.Serve(context.Background(), GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
			RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
		}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()})
	}()
	<-harness.source.readStarted
	if err := harness.broker.RevokeSession(context.Background(), harness.session.JTI, "logout"); err != nil {
		t.Fatal(err)
	}
	<-harness.source.readCanceled
	if err := <-serveDone; err == nil {
		t.Fatal("revoked active read unexpectedly succeeded")
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != string(DeliveryRevoked) || grant.RevocationReason != "logout" || len(harness.lease.releaseFences) != 1 {
		t.Fatalf("revoked grant=%+v releases=%d", grant, len(harness.lease.releaseFences))
	}
}

type reconcilerAuditFake struct {
	grants []string
	err    error
}

func seedReconcilerLeaseRow(t *testing.T, harness *budgetTestHarness) {
	t.Helper()
	if harness == nil || harness.grant.RecoveryPointID == nil {
		t.Fatal("invalid reconciler lease harness")
	}
	now := harness.clock.Now()
	lease := model.RecoveryPointLease{
		ID: harness.grant.LeaseID, RecoveryPointID: *harness.grant.RecoveryPointID,
		HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: harness.grant.ID,
		AttemptID: harness.grant.LeaseAttemptID, FenceToken: strings.Repeat("c", 64),
		Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Minute),
		AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := harness.db.Create(&lease).Error; err != nil {
		t.Fatal(err)
	}
}

func (fake *reconcilerAuditFake) FlushGrant(_ context.Context, grantID string) error {
	fake.grants = append(fake.grants, grantID)
	return fake.err
}

type reconcilerLeaseFake struct {
	takeovers             []backupasset.TakeoverLeaseRequest
	releases              []backupasset.LeaseFence
	takeoverErr           error
	releaseErr            error
	reconcileExpiredCalls int
}

func (fake *reconcilerLeaseFake) ReconcileExpired(context.Context) (int64, error) {
	fake.reconcileExpiredCalls++
	return 0, nil
}

func (*reconcilerLeaseFake) Acquire(context.Context, backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	return backupasset.Lease{}, nil
}

func (*reconcilerLeaseFake) Renew(context.Context, backupasset.LeaseFence) (backupasset.Lease, error) {
	return backupasset.Lease{}, nil
}

func (*reconcilerLeaseFake) ValidateFence(context.Context, backupasset.LeaseFence) error { return nil }

func (fake *reconcilerLeaseFake) Release(_ context.Context, fence backupasset.LeaseFence) error {
	fake.releases = append(fake.releases, fence)
	return fake.releaseErr
}

func (fake *reconcilerLeaseFake) Takeover(_ context.Context, request backupasset.TakeoverLeaseRequest) (backupasset.Lease, error) {
	fake.takeovers = append(fake.takeovers, request)
	if fake.takeoverErr != nil {
		return backupasset.Lease{}, fake.takeoverErr
	}
	now := time.Now().UTC()
	fence := backupasset.LeaseFence{
		LeaseID: request.LeaseID, RecoveryPointID: strings.Repeat("a", 32),
		HolderType: backupasset.LeaseHolderContentSession, OwnerID: request.OwnerID,
		AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("c", 64),
	}
	return backupasset.Lease{
		ID: request.LeaseID, RecoveryPointID: fence.RecoveryPointID, HolderType: fence.HolderType,
		OwnerID: request.OwnerID, Status: backupasset.LeaseActive,
		LeaseExpiresAt: now.Add(time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now, Fence: fence,
	}, nil
}
