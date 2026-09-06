package retention

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	assetrepository "xirang/backend/internal/backupasset/repository"
	appdatabase "xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	postgresgorm "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These focused acceptance tests use a fresh schema and the real migration
// runner.  The existing lifecycle unit fixtures remain useful for fast local
// development; this helper is intentionally separate so the acceptance names
// cannot accidentally pass against an AutoMigrate-only schema.
func newLifecycleAcceptancePostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	enableRetentionTestEncryption(t)
	dsn := strings.TrimSpace(os.Getenv("XIRANG_TEST_POSTGRES_DSN"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	}
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is required for lifecycle PostgreSQL acceptance tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	admin, err := gorm.Open(postgresgorm.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL acceptance database: %v", err)
	}
	digest := sha256.Sum256([]byte(t.Name()))
	schema := fmt.Sprintf("lifecycle_accept_%x", digest[:8])
	if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
		t.Fatalf("reset acceptance schema: %v", err)
	}
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create acceptance schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgresgorm.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		t.Fatalf("open acceptance schema: %v", err)
	}
	dbSQL, err := db.DB()
	if err != nil {
		t.Fatalf("acceptance database handle: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("acceptance admin handle: %v", err)
	}
	t.Cleanup(func() {
		_ = dbSQL.Close()
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = adminSQL.Close()
	})
	if err := appdatabase.RunMigrations(db, "postgres"); err != nil {
		t.Fatalf("run v77 PostgreSQL migrations: %v", err)
	}
	return db
}

func acceptanceAdvanceToProviderDelete(t *testing.T, coordinator *Coordinator, attemptID string) LifecycleAttempt {
	t.Helper()
	current, err := coordinator.loadAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatalf("load acceptance attempt: %v", err)
	}
	for steps := 0; current.Phase != backupasset.LifecyclePhaseProviderDelete && steps < 8; steps++ {
		current, err = coordinator.Advance(context.Background(), attemptID)
		if err != nil {
			t.Fatalf("advance acceptance attempt to provider_delete: phase=%q: %v", current.Phase, err)
		}
		if current.Phase == backupasset.LifecyclePhaseBlocked {
			t.Fatalf("acceptance attempt blocked before provider_delete: %q", current.BlockedReason)
		}
	}
	if current.Phase != backupasset.LifecyclePhaseProviderDelete {
		t.Fatalf("acceptance attempt stopped at %q, want provider_delete", current.Phase)
	}
	return current
}

func acceptanceAdvanceToTombstoning(t *testing.T, coordinator *Coordinator, attemptID string) LifecycleAttempt {
	t.Helper()
	current, err := coordinator.loadAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatalf("load acceptance attempt before tombstoning: %v", err)
	}
	for steps := 0; current.Phase != backupasset.LifecyclePhaseTombstoning && steps < 10; steps++ {
		current, err = coordinator.Advance(context.Background(), attemptID)
		if err != nil {
			t.Fatalf("advance acceptance attempt to tombstoning: phase=%q: %v", current.Phase, err)
		}
	}
	if current.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("acceptance attempt stopped at %q, want tombstoning", current.Phase)
	}
	return current
}

func acceptanceLoadClaim(t *testing.T, db *gorm.DB, attemptID string) model.RecoveryPointLifecycleEffectClaim {
	t.Helper()
	var claim model.RecoveryPointLifecycleEffectClaim
	if err := db.Where("attempt_id = ?", attemptID).First(&claim).Error; err != nil {
		t.Fatalf("load acceptance effect claim: %v", err)
	}
	return claim
}

func acceptanceCountTombstones(t *testing.T, db *gorm.DB, pointID string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.RecoveryPointLifecycleTombstone{}).Where("recovery_point_id = ?", pointID).Count(&count).Error; err != nil {
		t.Fatalf("count acceptance tombstones: %v", err)
	}
	return count
}

func acceptanceCountSlots(t *testing.T, db *gorm.DB, attemptID string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.RecoveryPointLifecycleAuditSlot{}).Where("attempt_id = ?", attemptID).Count(&count).Error; err != nil {
		t.Fatalf("count acceptance audit slots: %v", err)
	}
	return count
}

func acceptanceCountClaims(t *testing.T, db *gorm.DB, attemptID string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.RecoveryPointLifecycleEffectClaim{}).Where("attempt_id = ?", attemptID).Count(&count).Error; err != nil {
		t.Fatalf("count acceptance effect claims: %v", err)
	}
	return count
}

func acceptanceCountLeases(t *testing.T, db *gorm.DB, pointID string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ?", pointID).Count(&count).Error; err != nil {
		t.Fatalf("count acceptance leases: %v", err)
	}
	return count
}

func acceptanceSetRetryDue(t *testing.T, fixture *claimedExpiryFixture, attemptID string) {
	t.Helper()
	due := fixture.clock.Add(-time.Second)
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).Update("retry_at", due).Error; err != nil {
		t.Fatalf("make acceptance retry due: %v", err)
	}
}

const (
	acceptanceLeaseShortLive                = "short-live"
	acceptanceLeaseShortLiveNearExpiry      = "short-live-near-expiry"
	acceptanceLeaseShortExpiredAbsoluteLive = "short-expired/absolute-live"
	acceptanceLeaseAbsoluteExpired          = "absolute-expired"
)

func acceptanceApplyLeaseProfile(
	t *testing.T,
	fixture *claimedExpiryFixture,
	leaseID string,
	profile string,
) model.RecoveryPointLease {
	t.Helper()
	updates := map[string]any{}
	switch profile {
	case acceptanceLeaseShortLive:
		// The fixture's initial lease is short-live and absolute-live.
	case acceptanceLeaseShortLiveNearExpiry:
		updates["lease_expires_at"] = fixture.clock.Add(time.Second)
	case acceptanceLeaseShortExpiredAbsoluteLive:
		updates["lease_expires_at"] = fixture.clock.Add(-time.Second)
		updates["absolute_deadline"] = fixture.clock.Add(time.Hour)
	case acceptanceLeaseAbsoluteExpired:
		updates["lease_expires_at"] = fixture.clock.Add(time.Hour)
		updates["absolute_deadline"] = fixture.clock.Add(-time.Second)
	default:
		t.Fatalf("unknown acceptance lease profile %q", profile)
	}
	if len(updates) != 0 {
		if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", leaseID).Updates(updates).Error; err != nil {
			t.Fatalf("apply acceptance lease profile %q: %v", profile, err)
		}
	}
	var lease model.RecoveryPointLease
	if err := fixture.db.First(&lease, "id = ?", leaseID).Error; err != nil {
		t.Fatalf("reload acceptance lease profile %q: %v", profile, err)
	}
	return lease
}

func acceptanceSetObserverRetryDue(t *testing.T, fixture *claimedExpiryFixture) {
	t.Helper()
	acceptanceSetRetryDue(t, fixture, fixture.attempt.ID)
}

func acceptanceSameTimePtr(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.UTC().Equal(right.UTC())
}

func acceptanceAssertLeaseUnchanged(
	t *testing.T,
	db *gorm.DB,
	before model.RecoveryPointLease,
) model.RecoveryPointLease {
	t.Helper()
	var after model.RecoveryPointLease
	if err := db.First(&after, "id = ?", before.ID).Error; err != nil {
		t.Fatalf("reload acceptance lease: %v", err)
	}
	if after.RecoveryPointID != before.RecoveryPointID ||
		after.HolderType != before.HolderType ||
		after.OwnerID != before.OwnerID ||
		after.AttemptID != before.AttemptID ||
		after.FenceToken != before.FenceToken ||
		after.Status != before.Status ||
		!after.LeaseExpiresAt.UTC().Equal(before.LeaseExpiresAt.UTC()) ||
		!after.AbsoluteDeadline.UTC().Equal(before.AbsoluteDeadline.UTC()) ||
		!after.LastHeartbeatAt.UTC().Equal(before.LastHeartbeatAt.UTC()) ||
		!acceptanceSameTimePtr(after.ReleasedAt, before.ReleasedAt) {
		t.Fatalf("acceptance lease changed before=%+v after=%+v", before, after)
	}
	return after
}

func acceptanceAssertClaimBindingUnchanged(
	t *testing.T,
	db *gorm.DB,
	before model.RecoveryPointLifecycleEffectClaim,
) model.RecoveryPointLifecycleEffectClaim {
	t.Helper()
	after := acceptanceLoadClaim(t, db, before.AttemptID)
	if after.ID != before.ID ||
		after.ExecutorID != before.ExecutorID ||
		after.ExecutionID != before.ExecutionID ||
		after.TransitionRevision != before.TransitionRevision ||
		after.LeaseID != before.LeaseID ||
		after.LeaseAttemptID != before.LeaseAttemptID ||
		after.LeaseFenceTokenHash != before.LeaseFenceTokenHash ||
		after.TargetIdentityDigest != before.TargetIdentityDigest ||
		after.State != before.State ||
		!after.DeadlineAt.UTC().Equal(before.DeadlineAt.UTC()) ||
		!after.HeartbeatAt.UTC().Equal(before.HeartbeatAt.UTC()) {
		t.Fatalf("acceptance claim binding changed before=%+v after=%+v", before, after)
	}
	return after
}

func acceptanceAssertObserverBlockNoAdoption(
	t *testing.T,
	fixture *claimedExpiryFixture,
	beforeAttempt LifecycleAttempt,
	beforeClaim model.RecoveryPointLifecycleEffectClaim,
	beforeLease model.RecoveryPointLease,
	got LifecycleAttempt,
	providerCalls int,
) {
	t.Helper()
	if got.Phase != backupasset.LifecyclePhaseBlocked ||
		got.TransitionRevision != beforeAttempt.TransitionRevision+1 ||
		got.RetryAt == nil || !got.RetryAt.After(fixture.clock) {
		t.Fatalf("acceptance observer block attempt=%+v before=%+v, want one blocked transition/future retry", got, beforeAttempt)
	}
	afterAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("reload acceptance observer attempt: %v", err)
	}
	if afterAttempt.TransitionRevision != got.TransitionRevision ||
		afterAttempt.Phase != got.Phase ||
		afterAttempt.BlockedReason != got.BlockedReason ||
		afterAttempt.RetryAt == nil ||
		!afterAttempt.RetryAt.UTC().Equal(got.RetryAt.UTC()) {
		t.Fatalf("acceptance observer attempt reload=%+v returned=%+v", afterAttempt, got)
	}
	acceptanceAssertLeaseUnchanged(t, fixture.db, beforeLease)
	afterClaim := acceptanceAssertClaimBindingUnchanged(t, fixture.db, beforeClaim)
	if afterClaim.State != "uncertain" ||
		providerCalls != 0 ||
		acceptanceCountClaims(t, fixture.db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, fixture.db, fixture.pointID) != 0 {
		t.Fatalf("acceptance observer no-adoption claim=%+v provider_calls=%d claims=%d tombstones=%d",
			afterClaim, providerCalls, acceptanceCountClaims(t, fixture.db, fixture.attempt.ID),
			acceptanceCountTombstones(t, fixture.db, fixture.pointID))
	}
}

// acceptanceSeedUncertainClaimWithExpiredDeadline sets the claim's expired
// deadline while it is still in_flight.  The v77 guard permits in-flight clock
// updates, then the provider error performs the only legal in_flight→uncertain
// transition.  Updating an already-uncertain row would be rejected.
func acceptanceSeedUncertainClaimWithExpiredDeadline(
	t *testing.T,
	fixture *claimedExpiryFixture,
	deleter *acceptanceRegistryDeleter,
) LifecycleAttempt {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	deleter.SetBarrier(entered, release)
	deleter.SetError(errors.New("acceptance executor stopped after provider invocation"))
	defer func() {
		deleter.SetError(nil)
		deleter.SetBarrier(nil, nil)
	}()

	done := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	go func() {
		attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		done <- struct {
			attempt LifecycleAttempt
			err     error
		}{attempt: attempt, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("expired claim seed provider did not reach registered barrier")
	}

	now := fixture.clock.UTC()
	result := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
		Where("attempt_id = ? AND state = ?", fixture.attempt.ID, "in_flight").
		Updates(map[string]any{
			"deadline_at":  now.Add(-time.Second),
			"heartbeat_at": now.Add(-time.Second),
			"updated_at":   now,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		close(release)
		t.Fatalf("expire in-flight acceptance claim rows=%d error=%v", result.RowsAffected, result.Error)
	}
	acceptanceSetRetryDue(t, fixture, fixture.attempt.ID)
	close(release)

	var failed struct {
		attempt LifecycleAttempt
		err     error
	}
	select {
	case failed = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("expired claim seed provider did not finish")
	}
	if failed.err == nil || failed.attempt.Phase != backupasset.LifecyclePhaseProviderDelete || failed.attempt.RetryAt == nil {
		t.Fatalf("seed expired acceptance claim attempt=%+v error=%v", failed.attempt, failed.err)
	}
	claim := acceptanceLoadClaim(t, fixture.db, fixture.attempt.ID)
	if claim.State != "uncertain" || !claim.DeadlineAt.Before(now) {
		t.Fatalf("seed expired acceptance claim=%+v", claim)
	}
	return failed.attempt
}

func acceptanceMakeHold(t *testing.T, fixture *claimedExpiryFixture, base uint64) string {
	t.Helper()
	holdID := testOpaqueID(base)
	if err := fixture.db.Create(&model.RecoveryPointHold{
		ID: holdID, RecoveryPointID: fixture.pointID,
		HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
		EncryptedReason: "acceptance hold", CreatedBy: 1,
		CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
	}).Error; err != nil {
		t.Fatalf("create acceptance hold: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).Updates(map[string]any{
		"hold_state": string(backupasset.HoldActive), "point_revision": gorm.Expr("point_revision + 1"), "updated_at": fixture.clock,
	}).Error; err != nil {
		t.Fatalf("project acceptance hold: %v", err)
	}
	return holdID
}

func acceptanceReleaseHold(t *testing.T, fixture *claimedExpiryFixture, holdID string) {
	t.Helper()
	if _, err := fixture.holds.Release(context.Background(), ReleaseHoldRequest{
		Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RecoveryPointID: fixture.pointID, HoldID: holdID, Reason: "acceptance hold release",
	}); err != nil {
		t.Fatalf("release acceptance hold: %v", err)
	}
}

func acceptanceCloneCoordinator(t *testing.T, fixture *claimedExpiryFixture, deleter ProviderDeletePort, executorID string) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: fixture.db, Leases: fixture.coordinator.leases, Holds: fixture.holds,
		Now: func() time.Time { return fixture.clock }, NewID: func() (string, error) { return testOpaqueID(99991), nil },
		LeaseOwnerID: fixture.coordinator.leaseOwnerID, Admissions: fixture.coordinator.admissions,
		Cleanup: fixture.cleanup, Deleter: deleter, EffectExecutorID: executorID,
		EffectClaimTTL: fixture.coordinator.effectClaimTTL, EffectClaimAfter: fixture.coordinator.effectClaimAfter,
		RetryDelay: fixture.coordinator.retryDelay, Audit: fixture.coordinator.audit,
	})
	if err != nil {
		t.Fatalf("clone acceptance coordinator: %v", err)
	}
	return coordinator
}

// acceptanceRegistryDeleter is deliberately a registered provider.PointDeleter,
// not a retention shortcut.  RegistryPointDeletion therefore exercises request
// resolution, target identity hashing, provider invocation, and Verify.
type acceptanceRegistryDeleter struct {
	mu          sync.Mutex
	kind        backupasset.ProviderKind
	result      provider.DeletePointResult
	err         error
	calls       int
	requests    []provider.DeletePointRequest
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	afterEffect func()
}

func (deleter *acceptanceRegistryDeleter) ProviderKind() backupasset.ProviderKind {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	return deleter.kind
}

func (deleter *acceptanceRegistryDeleter) DeletePoint(ctx context.Context, request provider.DeletePointRequest) (provider.DeletePointResult, error) {
	deleter.mu.Lock()
	deleter.calls++
	deleter.requests = append(deleter.requests, request)
	entered := deleter.entered
	release := deleter.release
	result, err := deleter.result, deleter.err
	afterEffect := deleter.afterEffect
	deleter.afterEffect = nil
	if entered != nil {
		deleter.enteredOnce.Do(func() { close(entered) })
	}
	deleter.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	if afterEffect != nil {
		afterEffect()
	}
	return result, err
}

func (deleter *acceptanceRegistryDeleter) SetResult(result provider.DeletePointResult) {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	deleter.result = result
}

func (deleter *acceptanceRegistryDeleter) SetError(err error) {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	deleter.err = err
}

func (deleter *acceptanceRegistryDeleter) SetBarrier(entered, release chan struct{}) {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	deleter.entered, deleter.release = entered, release
	deleter.enteredOnce = sync.Once{}
}

func (deleter *acceptanceRegistryDeleter) SetAfterEffect(afterEffect func()) {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	deleter.afterEffect = afterEffect
}

func (deleter *acceptanceRegistryDeleter) Calls() int {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	return deleter.calls
}

func (deleter *acceptanceRegistryDeleter) Request() provider.DeletePointRequest {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	if len(deleter.requests) == 0 {
		return provider.DeletePointRequest{}
	}
	return deleter.requests[len(deleter.requests)-1]
}

type acceptanceResolverBarrier struct {
	mu      sync.Mutex
	target  int
	calls   int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (barrier *acceptanceResolverBarrier) wait(ctx context.Context) error {
	if barrier == nil {
		return nil
	}
	barrier.mu.Lock()
	barrier.calls++
	shouldBlock := barrier.target > 0 && barrier.calls == barrier.target
	entered, release := barrier.entered, barrier.release
	if shouldBlock && entered != nil {
		barrier.once.Do(func() { close(entered) })
	}
	barrier.mu.Unlock()
	if !shouldBlock || release == nil {
		return nil
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (barrier *acceptanceResolverBarrier) Calls() int {
	if barrier == nil {
		return 0
	}
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return barrier.calls
}

type acceptanceProviderProber struct{ kind backupasset.ProviderKind }

func (prober *acceptanceProviderProber) Probe(context.Context, provider.AccessBinding, provider.OperationLimits) (provider.RepositoryObservation, error) {
	return provider.RepositoryObservation{Provider: prober.kind, Availability: backupasset.PhysicalOnline}, nil
}

type acceptanceRegistryResolver struct {
	mu                    sync.Mutex
	kind                  backupasset.ProviderKind
	nativeVersions        []provider.RcloneNativeExactVersion
	authority             string
	resolveErr            error
	executionPrepareErr   error
	identityDrift         bool
	providerComplete      bool
	verifyTelemetry       bool
	observerCalls         int
	executionPrepareCalls int
	verifyCalls           int
	barrier               *acceptanceResolverBarrier
}

func (resolver *acceptanceRegistryResolver) SetError(err error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.resolveErr = err
}

func (resolver *acceptanceRegistryResolver) SetExecutionPrepareError(err error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.executionPrepareErr = err
}

func (resolver *acceptanceRegistryResolver) SetIdentityDrift(enabled bool) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.identityDrift = enabled
}

func (resolver *acceptanceRegistryResolver) SetProviderEffectComplete() {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.providerComplete = true
}

func (resolver *acceptanceRegistryResolver) SetVerifyTelemetry(enabled bool) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.verifyTelemetry = enabled
}

func (resolver *acceptanceRegistryResolver) ExecutionPrepareCalls() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.executionPrepareCalls
}
func (resolver *acceptanceRegistryResolver) ObserverCalls() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.observerCalls
}

func (resolver *acceptanceRegistryResolver) VerifyCalls() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.verifyCalls
}

func (resolver *acceptanceRegistryResolver) SetBarrier(barrier *acceptanceResolverBarrier) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.barrier = barrier
}

func (resolver *acceptanceRegistryResolver) ResolveDeletePoint(
	ctx context.Context,
	_ *gorm.DB,
	request LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if resolver == nil {
		return provider.DeletePointRequest{}, backupasset.ErrInvalidState
	}
	authorityRequest := request.authority.LeaseID != ""
	resolver.mu.Lock()
	kind, authority, resolveErr, executionPrepareErr, barrier := resolver.kind, resolver.authority, resolver.resolveErr, resolver.executionPrepareErr, resolver.barrier
	nativeVersions := append([]provider.RcloneNativeExactVersion(nil), resolver.nativeVersions...)
	identityDrift, providerComplete, verifyTelemetry := resolver.identityDrift, resolver.providerComplete, resolver.verifyTelemetry
	if authorityRequest {
		if providerComplete {
			resolver.verifyCalls++
		} else {
			resolver.executionPrepareCalls++
		}
	} else {
		resolver.observerCalls++
	}
	resolver.mu.Unlock()
	if barrierErr := barrier.wait(ctx); barrierErr != nil {
		return provider.DeletePointRequest{}, barrierErr
	}
	if resolveErr != nil {
		return provider.DeletePointRequest{}, resolveErr
	}
	if authorityRequest && !providerComplete && executionPrepareErr != nil {
		return provider.DeletePointRequest{}, executionPrepareErr
	}
	identity := ""
	if repository.RepositoryIdentity != nil {
		identity = *repository.RepositoryIdentity
	}
	endpointFacts := []string{"acceptance-endpoint"}
	if identityDrift {
		endpointFacts = []string{"acceptance-endpoint-drift"}
	}
	binding := provider.AccessBinding{
		Provider: kind, RepositoryID: repository.ID, TaskID: 1, NodeID: 1,
		IdentitySalt: []byte(strings.Repeat("s", provider.IdentitySaltBytes)), EndpointFacts: endpointFacts,
	}
	node := model.Node{
		ID: 1, Host: "localhost", Port: 22, Username: "root", AuthType: "password",
		BasePath: "/", BackupDir: "/backup", Password: "ACCEPTANCE_ONLY",
	}
	if authorityRequest && providerComplete && verifyTelemetry {
		// These are deliberately telemetry-only changes.  Verify must accept
		// them without changing the persisted target identity.
		node.Name = "acceptance-verify-telemetry"
		node.Status = "maintenance"
		node.ConnectionLatency = 17
	}
	switch kind {
	case backupasset.ProviderRestic:
		binding.AdapterData = provider.ResticRuntimeAccess{
			NativeRepositoryID: strings.Repeat("0", 64),
			Command:            &provider.RemoteCommandAccess{Node: node},
		}
	case backupasset.ProviderRclone:
		if authority == "" {
			authority = strings.Repeat("d", 64)
		}
		binding.AdapterData = provider.RcloneNativeDeletionAccess{
			Versions: nativeVersions, AuthorityDigest: authority,
			Command: &provider.RemoteCommandAccess{Node: node},
		}
	default:
		return provider.DeletePointRequest{}, provider.ErrDeletePointIdentityConflict
	}
	return provider.DeletePointRequest{
		Snapshot: provider.ReadSnapshot{
			RepositoryID: repository.ID, RepositoryIdentity: identity,
			CapabilityRevision: point.CapabilityRevision, SourceRevision: point.SourceFingerprint, Access: binding,
		},
		Point:                  provider.PointLocator{Native: point.EncryptedProviderLocator},
		ExpectedSourceRevision: point.SourceFingerprint, OperationID: request.AttemptID,
	}, nil
}

type acceptanceRepositoryPointResolver struct {
	service *assetrepository.Service
}

func (resolver *acceptanceRepositoryPointResolver) ResolveDeletePoint(
	ctx context.Context,
	tx *gorm.DB,
	request LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if resolver == nil || resolver.service == nil {
		return provider.DeletePointRequest{}, fmt.Errorf("%w: point deletion resolver is unavailable", backupasset.ErrInvalidState)
	}
	return resolver.service.ResolveLifecycleDeletePointTx(ctx, tx, request.AttemptID, point, repository)
}

type acceptancePublicationAdmission struct{}

type acceptancePublicationToken struct{}

func (acceptancePublicationAdmission) Acquire(context.Context, publication.ResticOperation) (publication.AdmissionToken, error) {
	return acceptancePublicationToken{}, nil
}

func (acceptancePublicationToken) Generation() uint64 { return 1 }
func (acceptancePublicationToken) Mode() publication.AdmissionMode {
	return publication.AdmissionManaged
}
func (acceptancePublicationToken) Operation() publication.ResticOperation {
	return publication.OperationEvidenceBackup
}
func (acceptancePublicationToken) Close() error { return nil }

type acceptanceRcloneLegacyBinding struct {
	Version       int                         `json:"version"`
	Provider      backupasset.ProviderKind    `json:"provider"`
	IdentityClass provider.IdentityClass      `json:"identity_class"`
	TaskID        uint                        `json:"task_id"`
	NodeID        uint                        `json:"node_id"`
	IdentitySalt  string                      `json:"identity_salt"`
	Locator       string                      `json:"locator"`
	EndpointFacts []string                    `json:"endpoint_facts"`
	ConfigSource  provider.RcloneConfigSource `json:"config_source"`
}

type acceptanceRcloneNativeBootstrap struct {
	Mode   string `json:"mode"`
	Static struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	} `json:"static_sts_bootstrap"`
}

type acceptanceRcloneNativeBinding struct {
	ProfileCode                    provider.RcloneNativeProfileCode           `json:"profile_code"`
	Region                         string                                     `json:"region"`
	Bucket                         string                                     `json:"bucket"`
	ManagedPrefix                  string                                     `json:"managed_prefix"`
	RegionIdentityDigest           string                                     `json:"region_identity_digest"`
	BucketIdentityDigest           string                                     `json:"bucket_identity_digest"`
	ManagedPrefixIdentityDigest    string                                     `json:"managed_prefix_identity_digest"`
	RoleARN                        string                                     `json:"role_arn"`
	ExternalID                     string                                     `json:"external_id"`
	Bootstrap                      acceptanceRcloneNativeBootstrap            `json:"bootstrap"`
	VersioningDigest               string                                     `json:"versioning_digest"`
	LifecycleDigest                string                                     `json:"lifecycle_digest"`
	CapabilityStableObservedAt     time.Time                                  `json:"capability_stable_observed_at"`
	EncryptionProfile              provider.RcloneNativeEncryptionProfileCode `json:"encryption_profile"`
	BucketEncryptionDigest         string                                     `json:"bucket_encryption_digest"`
	BucketKeyEnabled               bool                                       `json:"bucket_key_enabled"`
	CanaryEncryptionEvidenceDigest string                                     `json:"canary_encryption_evidence_digest"`
}

type acceptanceRcloneBinding struct {
	Version                   int                             `json:"version"`
	Provider                  backupasset.ProviderKind        `json:"provider"`
	IdentityClass             provider.IdentityClass          `json:"identity_class"`
	TaskID                    uint                            `json:"task_id"`
	NodeID                    uint                            `json:"node_id"`
	RepositoryID              string                          `json:"repository_id"`
	TaskRepositoryLinkID      string                          `json:"task_repository_link_id"`
	LayoutRevision            string                          `json:"layout_revision"`
	MinimumRuntimeRevision    int                             `json:"minimum_runtime_revision"`
	PublicationMode           backupasset.TaskPublicationMode `json:"publication_mode"`
	BindingRevision           uint64                          `json:"binding_revision"`
	ConfigRevision            uint64                          `json:"config_revision"`
	CapabilityRevision        uint64                          `json:"capability_revision"`
	CredentialRevision        uint64                          `json:"credential_revision"`
	PreflightID               string                          `json:"preflight_id"`
	PreflightRevision         uint64                          `json:"preflight_revision"`
	PreflightDigest           string                          `json:"preflight_digest"`
	PreflightExpiresAt        time.Time                       `json:"preflight_expires_at"`
	ManagedRootIdentityDigest string                          `json:"managed_root_identity_digest"`
	RepositoryMarkerDigest    string                          `json:"repository_marker_digest"`
	LegacyLocatorDigest       string                          `json:"legacy_locator_digest"`
	LegacyBindingV1           string                          `json:"legacy_binding_v1"`
	LegacyBindingDigest       string                          `json:"legacy_binding_digest"`
	LegacyTaskPolicy          string                          `json:"legacy_task_policy"`
	LegacyTaskPolicyDigest    string                          `json:"legacy_task_policy_digest"`
	RollbackPrepared          bool                            `json:"rollback_prepared"`
	IdentitySalt              string                          `json:"identity_salt"`
	Native                    acceptanceRcloneNativeBinding   `json:"native"`
}

type acceptanceRclonePointLocator struct {
	Version                      int                             `json:"version"`
	Provider                     backupasset.ProviderKind        `json:"provider"`
	RepositoryID                 string                          `json:"repository_id"`
	RecoveryPointID              string                          `json:"recovery_point_id"`
	AttemptID                    string                          `json:"attempt_id"`
	PublicationMode              backupasset.TaskPublicationMode `json:"publication_mode"`
	TaggedAttempt                string                          `json:"tagged_attempt"`
	TaggedCommit                 string                          `json:"tagged_commit"`
	ChildFenceDigest             string                          `json:"child_fence_digest"`
	CommitPayloadDigest          string                          `json:"commit_payload_digest"`
	FrozenNativeVersionCount     uint64                          `json:"frozen_native_version_count"`
	FrozenNativeVersionsDigest   string                          `json:"frozen_native_versions_digest"`
	FrozenNativeReferenceCount   uint64                          `json:"frozen_native_reference_count"`
	FrozenNativeReferencesDigest string                          `json:"frozen_native_references_digest"`
	PhysicalIdentityDigest       string                          `json:"physical_identity_digest"`
	ProviderCommitDigest         string                          `json:"provider_commit_digest"`
	ManifestControlIdentity      string                          `json:"manifest_control_identity"`
}

func acceptanceRcloneOwnershipDigest(key []byte, domain string, values ...string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, domain)
	for _, value := range values {
		_, _ = mac.Write([]byte{0})
		_, _ = io.WriteString(mac, value)
	}
	return mac.Sum(nil)
}

func acceptanceRcloneHexDigest(key []byte, domain string, values ...string) string {
	return hex.EncodeToString(acceptanceRcloneOwnershipDigest(key, domain, values...))
}

func acceptanceRcloneVersionAggregateDigest(markerKey []byte, role string, identities []string) string {
	values := []string{role, fmt.Sprintf("%d", len(identities))}
	for ordinal, identity := range identities {
		values = append(values, fmt.Sprintf("%d", ordinal), identity)
	}
	return acceptanceRcloneHexDigest(markerKey, "xirang/rclone/native-version-aggregate.v1", values...)
}

func acceptanceRcloneIdentitySalt() string {
	return strings.Repeat("ab", provider.IdentitySaltBytes)
}

func acceptanceRcloneBindingDigest(salt []byte, label, value string) string {
	return acceptanceRcloneHexDigest(salt, "xirang-managed-rclone-binding-v3\n"+label+"\n"+value)
}

func acceptanceRcloneTextDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type acceptanceLazyRcloneNativePointDeleter struct {
	mu          sync.Mutex
	calls       int
	requests    []provider.DeletePointRequest
	now         func() time.Time
	afterEffect func()
}

func (*acceptanceLazyRcloneNativePointDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRclone
}

func (deleter *acceptanceLazyRcloneNativePointDeleter) DeletePoint(ctx context.Context, request provider.DeletePointRequest) (provider.DeletePointResult, error) {
	deleter.mu.Lock()
	deleter.calls++
	deleter.requests = append(deleter.requests, request)
	afterEffect := deleter.afterEffect
	deleter.afterEffect = nil
	deleter.mu.Unlock()
	access, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess)
	if !ok || access.Client == nil {
		return provider.DeletePointResult{}, fmt.Errorf("%w: acceptance native lazy client missing", backupasset.ErrInvalidState)
	}
	real, err := provider.NewRcloneNativePointDeleter(access.Client, deleter.now)
	if err != nil {
		return provider.DeletePointResult{}, err
	}
	result, err := real.DeletePoint(ctx, request)
	if afterEffect != nil {
		afterEffect()
	}
	return result, err
}
func (deleter *acceptanceLazyRcloneNativePointDeleter) SetAfterEffect(afterEffect func()) {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	deleter.afterEffect = afterEffect
}

func (deleter *acceptanceLazyRcloneNativePointDeleter) Calls() int {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	return deleter.calls
}

func (deleter *acceptanceLazyRcloneNativePointDeleter) Request() provider.DeletePointRequest {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	if len(deleter.requests) == 0 {
		return provider.DeletePointRequest{}
	}
	return deleter.requests[len(deleter.requests)-1]
}

func acceptancePrepareRealRcloneLifecycle(
	t *testing.T,
	fixture *claimedExpiryFixture,
	native *acceptanceNativeVersionFake,
) *acceptanceLazyRcloneNativePointDeleter {
	t.Helper()
	now := fixture.clock.UTC()
	repositoryID := fixture.claim.PolicySelection.ScopeID
	pointID := fixture.pointID
	attemptID := fixture.attempt.ID
	linkID := testOpaqueID(31105)
	identitySalt := acceptanceRcloneIdentitySalt()
	salt, err := hex.DecodeString(identitySalt)
	if err != nil {
		t.Fatalf("decode acceptance Rclone identity salt: %v", err)
	}
	key := model.SSHKey{
		Name: "acceptance-rclone-key", Username: "acceptance", KeyType: "ed25519",
		PrivateKey: "ACCEPTANCE_RCLONE_PRIVATE_KEY", Fingerprint: "acceptance-rclone-fingerprint",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&key).Error; err != nil {
		t.Fatalf("seed acceptance Rclone SSH key: %v", err)
	}
	node := model.Node{
		Name: "acceptance-rclone-node", Host: "rclone.acceptance.invalid", Port: 22, Username: "acceptance",
		AuthType: "password", Password: "ACCEPTANCE_RCLONE_NODE_PASSWORD", BasePath: "/", BackupDir: "backup",
		SSHKeyID: &key.ID,
	}
	if err := fixture.db.Create(&node).Error; err != nil {
		t.Fatalf("seed acceptance Rclone node: %v", err)
	}
	task := model.Task{
		Name: "acceptance-rclone-task", NodeID: node.ID, ExecutorType: "rclone", Status: "pending", Enabled: true,
		Source: "manual", VerifyStatus: "none", CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&task).Error; err != nil {
		t.Fatalf("seed acceptance Rclone task: %v", err)
	}
	startedAt := now.Add(-time.Minute)
	taskRun := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusRunning,
		StartedAt: &startedAt, CreatedAt: startedAt, UpdatedAt: now,
	}
	if err := fixture.db.Create(&taskRun).Error; err != nil {
		t.Fatalf("seed acceptance Rclone TaskRun: %v", err)
	}
	taskID := task.ID
	link := model.TaskRepositoryLink{
		ID: linkID, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: task.Name,
		NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name,
		PublicationMode: string(backupasset.PublicationNativeObjectVersions), EncryptedLegacyLocator: "backup:acceptance",
		LinkedAt: startedAt, CreatedAt: startedAt, UpdatedAt: now,
	}
	if err := fixture.db.Create(&link).Error; err != nil {
		t.Fatalf("seed acceptance Rclone link: %v", err)
	}
	versioning := provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}
	versioningDigest, err := provider.CanonicalRcloneNativeVersioningDigest(versioning)
	if err != nil {
		t.Fatalf("digest acceptance Rclone versioning: %v", err)
	}
	lifecycle := provider.RcloneNativeLifecycleObservation{}
	lifecycleDigest, err := provider.CanonicalRcloneNativeLifecycleDigest(lifecycle)
	if err != nil {
		t.Fatalf("digest acceptance Rclone lifecycle: %v", err)
	}
	encryption := provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}
	encryptionDigest, err := provider.CanonicalRcloneNativeBucketEncryptionDigest(encryption)
	if err != nil {
		t.Fatalf("digest acceptance Rclone encryption: %v", err)
	}
	const (
		region     = "us-east-1"
		bucket     = "xirang-acceptance-bucket"
		managed    = "managed/v1/"
		roleARN    = "arn:aws:iam::123456789012:role/xirang-acceptance"
		externalID = "xirang-acceptance-external-id"
	)
	preflightDigest := strings.Repeat("1", 64)
	managedRootDigest := strings.Repeat("2", 64)
	repositoryMarkerDigest := strings.Repeat("3", 64)
	legacyLocator := "backup:acceptance"
	legacy := acceptanceRcloneLegacyBinding{
		Version: 1, Provider: backupasset.ProviderRclone, IdentityClass: provider.IdentityTaskScopedEndpoint,
		TaskID: task.ID, NodeID: node.ID, IdentitySalt: identitySalt, Locator: legacyLocator,
		EndpointFacts: []string{"backend:s3", "endpoint:acceptance"}, ConfigSource: provider.RcloneConfigNodeDefault,
	}
	legacyPayload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("encode acceptance Rclone legacy binding: %v", err)
	}
	legacyJSON := string(legacyPayload)
	legacyLocatorDigest := acceptanceRcloneBindingDigest(salt, "legacy-locator", legacyLocator)
	legacyBindingDigest := acceptanceRcloneBindingDigest(salt, "legacy-binding", legacyJSON)
	legacyPolicy := `{"version":1,"publication_mode":"legacy_mutable","bandwidth_limit":"10M","transfers":4}`
	legacyPolicyDigest := acceptanceRcloneBindingDigest(salt, "legacy-task-policy", legacyPolicy)
	preflightExpires := now.Add(3 * time.Hour)
	nativeBinding := acceptanceRcloneNativeBinding{
		ProfileCode: provider.RcloneNativeAWSS3GeneralPurposeV1, Region: region, Bucket: bucket, ManagedPrefix: managed,
		RegionIdentityDigest: strings.Repeat("4", 64), BucketIdentityDigest: strings.Repeat("5", 64),
		ManagedPrefixIdentityDigest: strings.Repeat("6", 64), RoleARN: roleARN, ExternalID: externalID,
		VersioningDigest: versioningDigest, LifecycleDigest: lifecycleDigest, CapabilityStableObservedAt: now.Add(-time.Hour),
		EncryptionProfile: provider.RcloneNativeSSES3V1, BucketEncryptionDigest: encryptionDigest,
		CanaryEncryptionEvidenceDigest: strings.Repeat("7", 64),
	}
	nativeBinding.Bootstrap.Mode = "static_sts_bootstrap"
	nativeBinding.Bootstrap.Static.AccessKeyID = "ACCEPTANCE_ACCESS_KEY_ID"
	nativeBinding.Bootstrap.Static.SecretAccessKey = "ACCEPTANCE_SECRET_ACCESS_KEY"
	binding := acceptanceRcloneBinding{
		Version: 3, Provider: backupasset.ProviderRclone, IdentityClass: provider.IdentityXirangManagedRepository,
		TaskID: task.ID, NodeID: node.ID, RepositoryID: repositoryID, TaskRepositoryLinkID: linkID,
		LayoutRevision: "rclone-publication:v1", MinimumRuntimeRevision: 1,
		PublicationMode: backupasset.PublicationNativeObjectVersions, BindingRevision: 2, ConfigRevision: 3,
		CapabilityRevision: 3, CredentialRevision: 4, PreflightID: testOpaqueID(31106), PreflightRevision: 1,
		PreflightDigest: preflightDigest, ManagedRootIdentityDigest: managedRootDigest,
		RepositoryMarkerDigest: repositoryMarkerDigest, LegacyLocatorDigest: legacyLocatorDigest,
		LegacyBindingV1: legacyJSON, LegacyBindingDigest: legacyBindingDigest, LegacyTaskPolicy: legacyPolicy,
		LegacyTaskPolicyDigest: legacyPolicyDigest, IdentitySalt: identitySalt, PreflightExpiresAt: preflightExpires,
		Native: nativeBinding,
	}
	bindingPayload, err := json.Marshal(binding)
	if err != nil {
		t.Fatalf("encode acceptance Rclone binding: %v", err)
	}
	identity, err := provider.DeriveScopedIdentity(salt, provider.ScopedIdentityDocument{
		Provider: backupasset.ProviderRclone, TaskID: task.ID, NodeID: node.ID,
		EndpointFacts: []string{
			"identity_class:xirang_managed_repository", "layout:rclone-publication:v1",
			"managed_root_identity:" + managedRootDigest, "repository:" + repositoryID,
			"publication_mode:" + string(backupasset.PublicationNativeObjectVersions),
		},
	})
	if err != nil {
		t.Fatalf("derive acceptance Rclone repository identity: %v", err)
	}
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", repositoryID).Updates(map[string]any{
		"provider_kind": string(backupasset.ProviderRclone), "repository_identity": identity,
		"version_mode": string(backupasset.VersionNativeObjectVersions), "status": string(backupasset.RepositoryOnline),
		"capability_revision": 3, "capabilities_json": `{"list":true,"open_sequential":true,"open_range":true}`,
		"immutability_level": string(backupasset.ImmutabilityBackendVersioned), "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("update acceptance Rclone repository: %v", err)
	}
	if err := fixture.db.Create(&model.RepositoryAccessBinding{
		ID: testOpaqueID(31107), RepositoryID: repositoryID, BindingKind: "managed_rclone_v3",
		EncryptedConfig: string(bindingPayload), ConfigFingerprint: strings.Repeat("8", 64), Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed acceptance Rclone access binding: %v", err)
	}
	attemptNative := &provider.RcloneNativeAttemptV1{
		ProfileCode:          provider.RcloneNativeAWSS3GeneralPurposeV1,
		RegionIdentityDigest: strings.Repeat("4", 64), BucketIdentityDigest: strings.Repeat("5", 64),
		ManagedPrefixIdentityDigest: strings.Repeat("6", 64), RoleSessionIdentityDigest: strings.Repeat("b", 64),
		SessionExpiresAt: now.Add(2 * time.Hour), VersioningDigest: versioningDigest, LifecycleDigest: lifecycleDigest,
		CapabilityStableObservedAt: now.Add(-time.Hour), EncryptionProfile: provider.RcloneNativeSSES3V1,
		BucketEncryptionDigest: encryptionDigest, B0VersionGraphDigest: strings.Repeat("9", 64),
		StartMarkerIdentityDigest: strings.Repeat("a", 64), CanaryIdentityDigest: strings.Repeat("c", 64),
	}
	pointDeadline := now.Add(time.Hour)
	attemptValue := provider.RcloneAttemptV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: 1, Provider: backupasset.ProviderRclone,
		RepositoryID: repositoryID, TaskRepositoryLinkID: linkID, RecoveryPointID: pointID, AttemptID: attemptID,
		TaskID: task.ID, TaskRunID: taskRun.ID, Trigger: "manual", PublicationMode: backupasset.PublicationNativeObjectVersions,
		CaptureStartedAt: startedAt, PreparedAt: now, PointDeadlineAt: pointDeadline,
		ExpectedTaskRevision: 1, BindingRevision: 2, ConfigRevision: 3, ConfigDigest: strings.Repeat("d", 64),
		CapabilityRevision: 3, CredentialRevision: 4, PreflightID: binding.PreflightID, PreflightRevision: 1,
		PreflightDigest: preflightDigest, ManifestSchemaRevision: 1, ManifestLimitsRevision: 1,
		ManifestLimitsDigest: strings.Repeat("e", 64), RepositoryIdentityDigest: strings.Repeat("f", 64),
		ManagedRootIdentityDigest: managedRootDigest, ChildFenceDigest: strings.Repeat("1", 64),
		LegacyOriginEvidenceDigest: strings.Repeat("2", 64), Native: attemptNative,
	}
	taggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(attemptValue))
	if err != nil {
		t.Fatalf("encode acceptance Rclone attempt: %v", err)
	}
	commitValue := provider.RcloneCommitV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: 1,
		RepositoryID: repositoryID, TaskRepositoryLinkID: linkID, RecoveryPointID: pointID, AttemptID: attemptID,
		PublicationMode: backupasset.PublicationNativeObjectVersions, PointDeadlineAt: pointDeadline,
		ProviderCommittedAt: now, ManifestIndexDigest: strings.Repeat("3", 64), ManifestChunkDigests: []string{strings.Repeat("4", 64)},
		ManifestEntryCount: 1, LogicalBytes: 5, SourceObservationDigest: strings.Repeat("5", 64),
		DestinationObservationDigest: strings.Repeat("6", 64), ContentProofDigest: strings.Repeat("7", 64),
		FidelityEvidenceDigest: strings.Repeat("8", 64), CostEvidenceDigest: strings.Repeat("9", 64),
		CapabilityEvidenceDigest: preflightDigest, ChildFenceDigest: attemptValue.ChildFenceDigest,
		Native: &provider.RcloneNativeCommitV1{
			CommitContentDigest: strings.Repeat("a", 64), ManifestControlGraphDigest: strings.Repeat("b", 64),
			PointViewDigest: strings.Repeat("c", 64), MutationLedgerDigest: strings.Repeat("d", 64),
			B0VersionGraphDigest: strings.Repeat("e", 64), B1VersionGraphDigest: strings.Repeat("f", 64),
			ExactReadProofDigest: strings.Repeat("1", 64), VersioningDigest: versioningDigest,
			LifecycleDigest: lifecycleDigest, BucketEncryptionDigest: encryptionDigest,
			EncryptionEvidenceDigest: strings.Repeat("2", 64), RoleSessionIdentityDigest: strings.Repeat("b", 64),
			CapabilityRevision: 3, CredentialRevision: 4, SessionExpiresAt: now.Add(2 * time.Hour),
		},
	}
	taggedCommit, err := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(commitValue))
	if err != nil {
		t.Fatalf("encode acceptance Rclone commit: %v", err)
	}
	commitDigest := acceptanceRcloneTextDigest(taggedCommit)
	commitKey := managed + "control/points/" + pointID + "/attempts/" + attemptID + "/commit.json"
	versionID := "acceptance-native-version-1"
	keyMaterial := backupasset.NewKeyring(fixture.db, func() time.Time { return now })
	material, err := keyMaterial.Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		t.Fatalf("ensure acceptance Rclone marker key: %v", err)
	}
	markerKey := acceptanceRcloneOwnershipDigest(material.Key, "xirang.rclone.marker-key.v1", repositoryID)
	native.mu.Lock()
	native.present[commitKey+"\x00"+versionID] = true
	native.mu.Unlock()
	ownedIdentity := acceptanceRcloneHexDigest(markerKey, "xirang/rclone/native-version-identity.v1", commitKey, versionID)
	ownedDigest := acceptanceRcloneVersionAggregateDigest(markerKey, model.RecoveryPointRcloneNativeEvidenceRoleOwned, []string{ownedIdentity})
	referenceDigest := acceptanceRcloneVersionAggregateDigest(markerKey, model.RecoveryPointRcloneNativeEvidenceRoleReference, nil)
	pointIdentity := acceptanceRcloneHexDigest(markerKey, "xirang.rclone.native-point-identity.v2", repositoryID,
		commitValue.Native.CommitContentDigest, "1", ownedDigest, "0", referenceDigest)
	locator := acceptanceRclonePointLocator{
		Version: 2, Provider: backupasset.ProviderRclone, RepositoryID: repositoryID, RecoveryPointID: pointID,
		AttemptID: attemptID, PublicationMode: backupasset.PublicationNativeObjectVersions, TaggedAttempt: taggedAttempt,
		TaggedCommit: taggedCommit, ChildFenceDigest: attemptValue.ChildFenceDigest,
		CommitPayloadDigest: commitValue.Native.CommitContentDigest, FrozenNativeVersionCount: 1,
		FrozenNativeVersionsDigest: ownedDigest, FrozenNativeReferenceCount: 0, FrozenNativeReferencesDigest: referenceDigest,
		PhysicalIdentityDigest: pointIdentity, ProviderCommitDigest: commitDigest,
		ManifestControlIdentity: commitValue.Native.ManifestControlGraphDigest,
	}
	locatorPayload, err := json.Marshal(locator)
	if err != nil {
		t.Fatalf("encode acceptance Rclone point locator: %v", err)
	}
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: linkID, TaskID: task.ID, TaskRunID: taskRun.ID, Trigger: "manual",
		PublicationMode: string(backupasset.PublicationNativeObjectVersions), PointCodecVersion: 1, TagCodecVersion: 0,
		StartedAt: startedAt, PreparedAt: now, PointDeadlineAt: pointDeadline,
	})
	if err != nil {
		t.Fatalf("encode acceptance Rclone lineage: %v", err)
	}
	captureStarted := startedAt
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{
		Version: 1, PublicationRevision: 1, AttemptCount: 1,
		CaptureStartedAt: &captureStarted, CaptureFinishedAt: &now, Provider: backupasset.ProviderRclone,
		RepositoryIdentityDigest: attemptValue.RepositoryIdentityDigest, ProviderCommitDigest: commitDigest,
		CapabilityRevision: 3,
	})
	if err != nil {
		t.Fatalf("encode acceptance Rclone consistency: %v", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load acceptance Rclone point: %v", err)
	}
	point.ProducingTaskID = &taskID
	point.ProducingTaskRunID = &taskRun.ID
	point.ProducingTaskNameSnapshot = task.Name
	point.ProducingNodeIDSnapshot = node.ID
	point.ProducingNodeNameSnapshot = node.Name
	point.LineageJSON = lineage
	point.EncryptedProviderLocator = string(locatorPayload)
	point.State = string(backupasset.RecoveryPointExpiring)
	point.SourceFingerprint = pointIdentity
	point.ManifestDigestAlgorithm = "sha256"
	point.ManifestDigest = commitValue.ManifestIndexDigest
	point.EntryCount = int64(commitValue.ManifestEntryCount)
	point.LogicalBytes = int64(commitValue.LogicalBytes)
	point.ConsistencyJSON = consistency
	point.FidelityJSON = `{}`
	point.CapabilityRevision = 3
	point.CapabilitiesJSON = `{"list":true,"open_sequential":true,"open_range":true}`
	point.ImmutabilityLevel = string(backupasset.ImmutabilityBackendVersioned)
	point.PhysicalAvailability = string(backupasset.PhysicalOnline)
	point.HoldState = string(backupasset.HoldNone)
	point.UpdatedAt = now
	if err := fixture.db.Save(&point).Error; err != nil {
		t.Fatalf("save acceptance Rclone point: %v", err)
	}
	if err := fixture.db.Create(&model.RecoveryPointRcloneNativeVersion{
		RecoveryPointID: pointID, EvidenceRole: model.RecoveryPointRcloneNativeEvidenceRoleOwned, Ordinal: 0,
		RepositoryID: repositoryID, IdentityDigest: ownedIdentity, EncryptedPhysicalKey: commitKey,
		EncryptedVersionID: versionID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed acceptance Rclone native evidence: %v", err)
	}
	registry := provider.NewRegistry()
	factory := &acceptanceRcloneNativeFactory{native: native, externalID: externalID, sessionIdentity: strings.Repeat("b", 64), expiresAt: now.Add(2 * time.Hour)}
	builder := assetrepository.RcloneNativeFactoryBuilder(func(context.Context, provider.RcloneNativeBootstrap, string, int) (assetrepository.RcloneNativeFactory, error) {
		return factory, nil
	})
	foundation := backupasset.NewFoundationService(retentionSettingsOverlay{service: settings.NewService(fixture.db), overlay: map[string]string{
		"backup_assets.enabled": "false",
	}})
	keyring := backupasset.NewKeyring(fixture.db, func() time.Time { return now })
	history, err := assetrepository.NewManagedHistoryResolver(assetrepository.ManagedHistoryResolverDependencies{DB: fixture.db})
	if err != nil {
		t.Fatalf("create acceptance Rclone history resolver: %v", err)
	}
	admission := acceptancePublicationAdmission{}
	publicationService, err := assetrepository.NewPublicationService(assetrepository.PublicationDependencies{
		DB: fixture.db, Foundation: foundation, Registry: registry, Keyring: keyring, Lease: fixture.coordinator.leases,
		Admission: admission, Metrics: publication.NoopMetrics{}, History: history, Now: func() time.Time { return fixture.clock },
		RcloneNativeFactoryBuilder: builder,
	})
	if err != nil {
		t.Fatalf("create acceptance Rclone publication service: %v", err)
	}
	repositoryService, err := assetrepository.NewService(assetrepository.Dependencies{
		DB: fixture.db, Foundation: foundation, Registry: registry, Keyring: keyring,
		Now: func() time.Time { return fixture.clock }, Admission: admission, History: history,
		Metrics: publication.NoopMetrics{}, Publication: publicationService,
	})
	if err != nil {
		t.Fatalf("create acceptance Rclone repository service: %v", err)
	}
	resolver := &acceptanceRepositoryPointResolver{service: repositoryService}
	deleter := &acceptanceLazyRcloneNativePointDeleter{
		now: func() time.Time { return fixture.clock },
	}
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{
		Prober: &acceptanceProviderProber{kind: backupasset.ProviderRclone}, PointDeleter: deleter,
	}); err != nil {
		t.Fatalf("register acceptance real Rclone lifecycle deleter: %v", err)
	}
	adapter, err := NewRegistryPointDeletion(fixture.db, registry, resolver)
	if err != nil {
		t.Fatalf("create acceptance real Rclone RegistryPointDeletion: %v", err)
	}
	adapter.SetNow(func() time.Time { return fixture.clock })
	fixture.coordinator.deleter = adapter
	return deleter
}

func acceptanceUseRegistry(
	t *testing.T,
	fixture *claimedExpiryFixture,
	kind backupasset.ProviderKind,
	pointDeleter provider.PointDeleter,
	resolver *acceptanceRegistryResolver,
) *RegistryPointDeletion {
	t.Helper()
	identity := fmt.Sprintf("%s:acceptance:%s", kind, fixture.claim.PolicySelection.ScopeID)
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.claim.PolicySelection.ScopeID).Updates(map[string]any{
		"provider_kind": string(kind), "repository_identity": identity, "capability_revision": 3,
	}).Error; err != nil {
		t.Fatalf("prepare acceptance registry repository: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).Updates(map[string]any{
		"source_fingerprint": strings.Repeat("c", 64), "capability_revision": 3, "updated_at": fixture.clock,
	}).Error; err != nil {
		t.Fatalf("prepare acceptance registry point: %v", err)
	}
	registry := provider.NewRegistry()
	if err := registry.Register(kind, provider.Registration{Prober: &acceptanceProviderProber{kind: kind}, PointDeleter: pointDeleter}); err != nil {
		t.Fatalf("register acceptance provider: %v", err)
	}
	if resolver == nil {
		resolver = &acceptanceRegistryResolver{}
	}
	resolver.mu.Lock()
	resolver.kind = kind
	resolver.mu.Unlock()
	adapter, err := NewRegistryPointDeletion(fixture.db, registry, resolver)
	if err != nil {
		t.Fatalf("create acceptance RegistryPointDeletion: %v", err)
	}
	adapter.SetNow(func() time.Time { return fixture.clock })
	fixture.coordinator.deleter = adapter
	return adapter
}

func acceptanceInstallRegistry(
	t *testing.T,
	fixture *claimedExpiryFixture,
	result provider.DeletePointResult,
) (*acceptanceRegistryDeleter, *acceptanceRegistryResolver, *RegistryPointDeletion) {
	t.Helper()
	deleter := &acceptanceRegistryDeleter{kind: backupasset.ProviderRestic, result: result}
	resolver := &acceptanceRegistryResolver{kind: backupasset.ProviderRestic}
	adapter := acceptanceUseRegistry(t, fixture, backupasset.ProviderRestic, deleter, resolver)
	return deleter, resolver, adapter
}

func newAcceptanceRegistryFixture(
	t *testing.T,
	db *gorm.DB,
	base uint64,
	result provider.DeletePointResult,
) (*claimedExpiryFixture, *acceptanceRegistryDeleter, *acceptanceRegistryResolver, *RegistryPointDeletion) {
	t.Helper()
	fixture := newClaimedExpiryFixtureWithDB(t, db, base)
	deleter, resolver, adapter := acceptanceInstallRegistry(t, fixture, result)
	return fixture, deleter, resolver, adapter
}
func newAcceptanceRegistrySecondPrepareFailureFixture(
	t *testing.T,
	db *gorm.DB,
	base uint64,
	prepareErr error,
) (*claimedExpiryFixture, *acceptanceRegistryDeleter, *acceptanceRegistryResolver, *RegistryPointDeletion) {
	t.Helper()
	fixture, deleter, resolver, adapter := newAcceptanceRegistryFixture(t, db, base,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("6", 64)})
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	acceptanceSeedUncertainClaimWithExpiredDeadline(t, fixture, deleter)
	// The first execution Prepare already succeeded while seeding the
	// uncertain claim.  This error is consumed by the next execution Prepare,
	// after the observer Prepare and takeover lease handling have started.
	resolver.SetExecutionPrepareError(prepareErr)
	return fixture, deleter, resolver, adapter
}

// --- durable effect claim acceptance scenarios ---

func runAcceptanceDualAdvance(t *testing.T, db *gorm.DB, sameExecutor bool) {
	t.Helper()
	fixture, deleter, _, adapter := newAcceptanceRegistryFixture(t, db, 30000,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("a", 64)})
	entered, release := make(chan struct{}), make(chan struct{})
	deleter.SetBarrier(entered, release)
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)

	first := fixture.coordinator
	executor := testOpaqueID(30004)
	if sameExecutor {
		first.effectExecutorID = executor
	}
	secondExecutor := testOpaqueID(30005)
	if sameExecutor {
		secondExecutor = executor
	}
	second := acceptanceCloneCoordinator(t, fixture, adapter, secondExecutor)
	firstResult := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	go func() {
		attempt, err := first.Advance(context.Background(), fixture.attempt.ID)
		firstResult <- struct {
			attempt LifecycleAttempt
			err     error
		}{attempt, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first registered provider execution did not reach the barrier")
	}
	beforeAttempt, err := first.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("load in-flight attempt: %v", err)
	}
	beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	secondResult := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	go func() {
		attempt, err := second.Advance(context.Background(), fixture.attempt.ID)
		secondResult <- struct {
			attempt LifecycleAttempt
			err     error
		}{attempt, err}
	}()
	var loser struct {
		attempt LifecycleAttempt
		err     error
	}
	select {
	case loser = <-secondResult:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight loser did not observe the durable claim")
	}
	if !errors.Is(loser.err, ErrEffectClaimInFlight) || loser.attempt.Phase != beforeAttempt.Phase {
		t.Fatalf("loser attempt=%+v error=%v, want unchanged in-flight observation", loser.attempt, loser.err)
	}
	afterLoser, err := first.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("load attempt after in-flight loser: %v", err)
	}
	afterLoserClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if afterLoser.TransitionRevision != beforeAttempt.TransitionRevision || afterLoser.Phase != beforeAttempt.Phase ||
		afterLoser.BlockedReason != beforeAttempt.BlockedReason || afterLoser.RetryAt != nil ||
		afterLoserClaim.State != beforeClaim.State || afterLoserClaim.ExecutionID != beforeClaim.ExecutionID {
		t.Fatalf("loser mutated attempt/claim: before=%+v/%+v after=%+v/%+v", beforeAttempt, beforeClaim, afterLoser, afterLoserClaim)
	}
	close(release)
	select {
	case winner := <-firstResult:
		if winner.err != nil || winner.attempt.Phase != backupasset.LifecyclePhaseTombstoning {
			t.Fatalf("winner attempt=%+v error=%v, want tombstoning", winner.attempt, winner.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not complete after provider release")
	}
	if deleter.Calls() != 1 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("registered provider calls=%d claims=%d tombstones=%d, want one/one/one",
			deleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if claim.State != "proven" || claim.ExecutionID != beforeClaim.ExecutionID {
		t.Fatalf("claim=%+v, want one proven execution", claim)
	}
	request := deleter.Request()
	if request.OperationID != fixture.attempt.ID || request.Snapshot.Access.Provider != backupasset.ProviderRestic {
		t.Fatalf("registered provider request=%+v", request)
	}
}

func runAcceptanceLateLoser(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Run("overlaps winner receipt transaction", func(t *testing.T) {
		fixture, deleter, resolver, adapter := newAcceptanceRegistryFixture(t, db, 30100,
			provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("b", 64)})
		acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
		verifyEntered, verifyRelease := make(chan struct{}), make(chan struct{})
		verifyBarrier := &acceptanceResolverBarrier{
			target: 2, entered: verifyEntered, release: verifyRelease,
		}
		resolver.SetBarrier(verifyBarrier)
		winnerDone := make(chan struct {
			attempt LifecycleAttempt
			err     error
		}, 1)
		go func() {
			attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			winnerDone <- struct {
				attempt LifecycleAttempt
				err     error
			}{attempt, err}
		}()
		select {
		case <-verifyEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("winner Tx2 Verify did not reach the deterministic barrier")
		}

		loser := acceptanceCloneCoordinator(t, fixture, adapter, testOpaqueID(30105))
		loserLoaded := make(chan struct{})
		var loserLoadOnce sync.Once
		callbackName := "lifecycle:acceptance-late-loser-load-" + fixture.attempt.ID
		if err := loser.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Table != "recovery_point_lifecycle_attempts" {
				return
			}
			if _, ok := tx.Statement.Dest.(*model.RecoveryPointLifecycleAttempt); !ok {
				return
			}
			loserLoadOnce.Do(func() { close(loserLoaded) })
		}); err != nil {
			t.Fatalf("register late-loser load barrier: %v", err)
		}
		defer func() {
			if err := loser.db.Callback().Query().Remove(callbackName); err != nil {
				t.Errorf("remove late-loser load barrier: %v", err)
			}
		}()
		loserDone := make(chan struct {
			attempt LifecycleAttempt
			err     error
		}, 1)
		go func() {
			attempt, err := loser.Advance(context.Background(), fixture.attempt.ID)
			loserDone <- struct {
				attempt LifecycleAttempt
				err     error
			}{attempt, err}
		}()
		select {
		case <-loserLoaded:
		case <-time.After(5 * time.Second):
			t.Fatal("late loser did not load provider-delete state before winner receipt release")
		}

		// Releasing Verify lets the winner commit its receipt. The loser is
		// already blocked on the same repository-first lock and therefore
		// cannot observe pre-receipt state or begin another provider call.
		close(verifyRelease)
		var winner struct {
			attempt LifecycleAttempt
			err     error
		}
		select {
		case winner = <-winnerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("winner receipt did not commit")
		}
		if winner.err != nil || winner.attempt.Phase != backupasset.LifecyclePhaseTombstoning {
			t.Fatalf("winner attempt=%+v error=%v, want tombstoning after receipt", winner.attempt, winner.err)
		}
		winnerClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		winnerAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
		if err != nil {
			t.Fatalf("load winner state after receipt commit: %v", err)
		}

		var late struct {
			attempt LifecycleAttempt
			err     error
		}
		select {
		case late = <-loserDone:
		case <-time.After(5 * time.Second):
			t.Fatal("late loser did not resume after winner receipt commit")
		}
		if late.err != nil || late.attempt.Phase != backupasset.LifecyclePhaseTombstoning {
			t.Fatalf("late receipt loser attempt=%+v error=%v", late.attempt, late.err)
		}
		afterAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
		if err != nil {
			t.Fatalf("reload state after late receipt loser: %v", err)
		}
		afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		if winnerClaim.State != "proven" || afterClaim.State != winnerClaim.State ||
			afterClaim.ExecutionID != winnerClaim.ExecutionID ||
			afterClaim.LeaseID != winnerClaim.LeaseID ||
			afterClaim.LeaseFenceTokenHash != winnerClaim.LeaseFenceTokenHash ||
			afterAttempt.Phase != winnerAttempt.Phase ||
			afterAttempt.TransitionRevision != winnerAttempt.TransitionRevision ||
			afterAttempt.RetryAt != winnerAttempt.RetryAt ||
			afterAttempt.BlockedReason != winnerAttempt.BlockedReason {
			t.Fatalf("late receipt loser mutated winner state: winner=%+v/%+v after=%+v/%+v",
				winnerAttempt, winnerClaim, afterAttempt, afterClaim)
		}
		if verifyBarrier.Calls() != 2 ||
			deleter.Calls() != 1 ||
			acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
			acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
			t.Fatalf("late receipt provider calls=%d resolver_calls=%d claims=%d tombstones=%d, want one/two/one/one",
				deleter.Calls(), verifyBarrier.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID),
				acceptanceCountTombstones(t, db, fixture.pointID))
		}
	})

	t.Run("after committed takeover winner", func(t *testing.T) {
		fixture, deleter, _, adapter := newAcceptanceRegistryFixture(t, db, 30150,
			provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("b", 64)})
		wakeRequests := make(chan chan time.Time, 4)
		fixture.coordinator.effectClaimAfter = func(time.Duration) <-chan time.Time {
			wake := make(chan time.Time, 1)
			wakeRequests <- wake
			return wake
		}
		acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
		entered, release := make(chan struct{}), make(chan struct{})
		deleter.SetBarrier(entered, release)
		oldDone := make(chan struct {
			attempt LifecycleAttempt
			err     error
		}, 1)
		go func() {
			attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			oldDone <- struct {
				attempt LifecycleAttempt
				err     error
			}{attempt, err}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("stale execution did not reach registered provider")
		}
		select {
		case <-wakeRequests:
		case <-time.After(5 * time.Second):
			t.Fatal("stale execution renewer did not reach deterministic wake barrier")
		}
		oldClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		oldAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
		if err != nil {
			t.Fatalf("load stale execution attempt: %v", err)
		}
		var oldLease model.RecoveryPointLease
		if err := db.First(&oldLease, "id = ?", oldClaim.LeaseID).Error; err != nil {
			t.Fatalf("load stale execution lease: %v", err)
		}
		if err := db.Model(&model.RecoveryPointLifecycleEffectClaim{}).Where("id = ?", oldClaim.ID).Updates(map[string]any{
			"state": "uncertain", "deadline_at": fixture.clock.Add(-time.Second),
		}).Error; err != nil {
			t.Fatalf("expire stale execution claim: %v", err)
		}
		acceptanceSetRetryDue(t, fixture, fixture.attempt.ID)
		if err := db.Model(&model.RecoveryPointLease{}).Where("id = ?", oldLease.ID).
			Update("lease_expires_at", fixture.clock.Add(-time.Second)).Error; err != nil {
			t.Fatalf("expire stale execution lease: %v", err)
		}
		deleter.SetBarrier(nil, nil)
		deleter.SetResult(provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("c", 64)})
		takenOver := acceptanceCloneCoordinator(t, fixture, adapter, testOpaqueID(30155))
		taken, err := takenOver.Advance(context.Background(), fixture.attempt.ID)
		if err != nil || taken.Phase != backupasset.LifecyclePhaseTombstoning {
			t.Fatalf("takeover winner attempt=%+v error=%v", taken, err)
		}
		finished, err := takenOver.Advance(context.Background(), fixture.attempt.ID)
		if err != nil || finished.Phase != backupasset.LifecyclePhaseComplete {
			t.Fatalf("takeover winner completion=%+v error=%v", finished, err)
		}
		winnerClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		winnerAttempt, err := takenOver.loadAttempt(context.Background(), fixture.attempt.ID)
		if err != nil {
			t.Fatalf("load committed takeover winner: %v", err)
		}
		if winnerClaim.State != "proven" || winnerClaim.ExecutionID == oldClaim.ExecutionID ||
			winnerClaim.TargetIdentityDigest != oldClaim.TargetIdentityDigest ||
			winnerClaim.LeaseFenceTokenHash == oldClaim.LeaseFenceTokenHash ||
			winnerAttempt.TransitionRevision <= oldAttempt.TransitionRevision {
			t.Fatalf("takeover winner claim/attempt=%+v/%+v old=%+v/%+v", winnerClaim, winnerAttempt, oldClaim, oldAttempt)
		}
		close(release)
		select {
		case stale := <-oldDone:
			if stale.err == nil {
				t.Fatalf("stale execution unexpectedly persisted after takeover: %+v", stale.attempt)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stale execution did not resume after takeover winner")
		}
		afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		afterAttempt, err := takenOver.loadAttempt(context.Background(), fixture.attempt.ID)
		if err != nil {
			t.Fatalf("reload takeover winner after stale execution: %v", err)
		}
		if afterClaim.State != winnerClaim.State || afterClaim.ExecutionID != winnerClaim.ExecutionID ||
			afterClaim.LeaseFenceTokenHash != winnerClaim.LeaseFenceTokenHash ||
			afterAttempt.Phase != winnerAttempt.Phase ||
			afterAttempt.TransitionRevision != winnerAttempt.TransitionRevision ||
			afterAttempt.RetryAt != winnerAttempt.RetryAt ||
			afterAttempt.BlockedReason != winnerAttempt.BlockedReason ||
			deleter.Calls() != 2 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
			acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
			t.Fatalf("stale execution mutated takeover winner: winner=%+v/%+v after=%+v/%+v calls=%d claims=%d tombstones=%d",
				winnerAttempt, winnerClaim, afterAttempt, afterClaim, deleter.Calls(),
				acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
		}
	})
}

func runAcceptanceProofFirstRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	for index, claimState := range []string{"in_flight", "uncertain"} {
		t.Run(claimState, func(t *testing.T) {
			fixture := seedProviderDeleteProofFirstFixtureWithDB(
				t, db, 30600+uint64(index)*100, claimState,
			)
			audit := &acceptanceAuditSink{}
			fixture.coordinator.audit = audit
			authorityMutation := map[string]any{"owner_id": "retention-worker-proof-restart"}
			if index == 1 {
				authorityMutation = map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)}
			}
			if err := db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", fixture.attempt.LeaseID).Updates(authorityMutation).Error; err != nil {
				t.Fatalf("rebind proof-first acceptance lease: %v", err)
			}
			restarted := newRestartedExpiryCoordinator(t, fixture, 30700+uint64(index)*100)
			restarted.leaseOwnerID = "retention-worker-proof-restart-coordinator"
			restarted.audit = audit
			fixture.coordinator = restarted

			var beforeAttempt model.RecoveryPointLifecycleAttempt
			if err := db.First(&beforeAttempt, "id = ?", fixture.attempt.ID).Error; err != nil {
				t.Fatalf("load proof-first acceptance attempt: %v", err)
			}
			if beforeAttempt.RetryAt == nil {
				t.Fatal("proof-first acceptance retry gate is nil")
			}
			beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			var beforePoint model.RecoveryPoint
			if err := db.First(&beforePoint, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load proof-first acceptance point: %v", err)
			}
			var beforeLease model.RecoveryPointLease
			if err := db.First(&beforeLease, "id = ?", beforeClaim.LeaseID).Error; err != nil {
				t.Fatalf("load proof-first acceptance lease: %v", err)
			}
			var beforeTombstone model.RecoveryPointLifecycleTombstone
			if err := db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&beforeTombstone).Error; err != nil {
				t.Fatalf("load proof-first acceptance tombstone: %v", err)
			}
			beforeRetryAt := beforeAttempt.RetryAt.UTC()
			beforeCalls := fixture.deleter.calls
			beforeWrites := audit.Writes()
			beforeSlots := acceptanceCountSlots(t, db, fixture.attempt.ID)
			if beforeSlots != 0 {
				t.Fatalf("proof-first acceptance seeded slots=%d, want zero", beforeSlots)
			}

			pending, err := fixture.coordinator.flushDueSettledAuditBeforeHeartbeat(
				context.Background(), fixture.attempt,
			)
			if err != nil || !pending {
				t.Fatalf("proof-first acceptance pre-due audit pending=%v error=%v, want pending", pending, err)
			}
			var afterAttempt model.RecoveryPointLifecycleAttempt
			if err := db.First(&afterAttempt, "id = ?", fixture.attempt.ID).Error; err != nil {
				t.Fatalf("reload proof-first acceptance attempt before due: %v", err)
			}
			var afterPoint model.RecoveryPoint
			if err := db.First(&afterPoint, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("reload proof-first acceptance point before due: %v", err)
			}
			afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			var afterLease model.RecoveryPointLease
			if err := db.First(&afterLease, "id = ?", beforeClaim.LeaseID).Error; err != nil {
				t.Fatalf("reload proof-first acceptance lease before due: %v", err)
			}
			var afterTombstone model.RecoveryPointLifecycleTombstone
			if err := db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&afterTombstone).Error; err != nil {
				t.Fatalf("reload proof-first acceptance tombstone before due: %v", err)
			}
			if !reflect.DeepEqual(beforeAttempt, afterAttempt) ||
				!reflect.DeepEqual(beforePoint, afterPoint) ||
				!reflect.DeepEqual(beforeClaim, afterClaim) ||
				!reflect.DeepEqual(beforeLease, afterLease) ||
				!reflect.DeepEqual(beforeTombstone, afterTombstone) ||
				fixture.deleter.calls != beforeCalls ||
				audit.Writes() != beforeWrites ||
				acceptanceCountSlots(t, db, fixture.attempt.ID) != beforeSlots {
				t.Fatalf("proof-first acceptance pre-due mutation attempt=%v point=%v claim=%v lease=%v tombstone=%v provider_calls=%d audit_writes=%d slots=%d",
					!reflect.DeepEqual(beforeAttempt, afterAttempt),
					!reflect.DeepEqual(beforePoint, afterPoint),
					!reflect.DeepEqual(beforeClaim, afterClaim),
					!reflect.DeepEqual(beforeLease, afterLease),
					!reflect.DeepEqual(beforeTombstone, afterTombstone),
					fixture.deleter.calls, audit.Writes(), acceptanceCountSlots(t, db, fixture.attempt.ID))
			}

			fixture.clock = beforeRetryAt
			pending, err = fixture.coordinator.flushDueSettledAuditBeforeHeartbeat(
				context.Background(), fixture.attempt,
			)
			if err != nil || pending {
				t.Fatalf("proof-first acceptance due audit pending=%v error=%v, want emitted", pending, err)
			}
			current, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
			if err != nil {
				t.Fatalf("load proof-first acceptance attempt after due: %v", err)
			}
			if current.Phase != backupasset.LifecyclePhaseTombstoning ||
				current.RetryAt != nil ||
				fixture.deleter.calls != beforeCalls ||
				audit.Writes() != beforeWrites+1 ||
				acceptanceCountSlots(t, db, fixture.attempt.ID) != beforeSlots+1 {
				t.Fatalf("proof-first acceptance due attempt=%+v provider_calls=%d audit_writes=%d slots=%d",
					current, fixture.deleter.calls, audit.Writes(), acceptanceCountSlots(t, db, fixture.attempt.ID))
			}
			afterClaim = acceptanceLoadClaim(t, db, fixture.attempt.ID)
			if afterClaim.State != beforeClaim.State ||
				afterClaim.ExecutionID != beforeClaim.ExecutionID ||
				afterClaim.LeaseID != beforeClaim.LeaseID ||
				afterClaim.LeaseFenceTokenHash != beforeClaim.LeaseFenceTokenHash {
				t.Fatalf("proof-first acceptance claim changed before=%+v after=%+v",
					beforeClaim, afterClaim)
			}
			completed, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete ||
				fixture.deleter.calls != beforeCalls ||
				audit.Writes() != beforeWrites+1 ||
				acceptanceCountSlots(t, db, fixture.attempt.ID) != beforeSlots+1 {
				t.Fatalf("proof-first acceptance completion=%+v provider_calls=%d audit_writes=%d slots=%d error=%v",
					completed, fixture.deleter.calls, audit.Writes(),
					acceptanceCountSlots(t, db, fixture.attempt.ID), err)
			}
		})
	}
	for _, testCase := range []struct {
		name   string
		base   uint64
		mutate map[string]any
	}{
		{
			name:   "rebound_owner",
			base:   30800,
			mutate: map[string]any{"owner_id": "retention-worker-active-rebound"},
		},
		{
			name:   "rebound_holder",
			base:   30900,
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
		{
			name:   "rebound_attempt",
			base:   31200,
			mutate: map[string]any{"attempt_id": testOpaqueID(31220)},
		},
		{
			name:   "rebound_fence",
			base:   31300,
			mutate: map[string]any{"fence_token": strings.Repeat("9", 64)},
		},
	} {
		t.Run("active_"+testCase.name, func(t *testing.T) {
			fixture := seedProviderDeleteProofFirstFixtureWithDB(
				t, db, testCase.base, "in_flight",
			)
			audit := &acceptanceAuditSink{}
			originalOwner := fixture.coordinator.leaseOwnerID
			now := fixture.clock.UTC()
			retryAt := now.Add(time.Minute)
			leaseExpiresAt := now.Add(5 * time.Minute)
			absoluteDeadline := now.Add(10 * time.Minute)
			if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).
				Where("id = ?", fixture.attempt.ID).Update("retry_at", retryAt).Error; err != nil {
				t.Fatalf("set active proof retry gate: %v", err)
			}
			leaseUpdates := map[string]any{
				"status":            backupasset.LeaseActive,
				"holder_type":       string(backupasset.LeaseHolderRetentionWorker),
				"owner_id":          originalOwner,
				"lease_expires_at":  leaseExpiresAt,
				"absolute_deadline": absoluteDeadline,
				"released_at":       nil,
				"updated_at":        now,
			}
			for key, value := range testCase.mutate {
				leaseUpdates[key] = value
			}
			if err := db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", fixture.attempt.LeaseID).Updates(leaseUpdates).Error; err != nil {
				t.Fatalf("rebind active proof lease: %v", err)
			}
			restarted := newRestartedExpiryCoordinator(t, fixture, testCase.base+200)
			restarted.leaseOwnerID = originalOwner
			restarted.audit = audit
			fixture.coordinator = restarted

			beforeAttempt, err := restarted.loadAttempt(context.Background(), fixture.attempt.ID)
			if err != nil {
				t.Fatalf("load active proof attempt: %v", err)
			}
			if beforeAttempt.RetryAt == nil ||
				beforeAttempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				t.Fatalf("active proof baseline attempt=%+v, want due-gated provider_delete", beforeAttempt)
			}
			beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			var beforeLease model.RecoveryPointLease
			if err := db.First(&beforeLease, "id = ?", beforeClaim.LeaseID).Error; err != nil {
				t.Fatalf("load active proof lease: %v", err)
			}
			var beforeTombstone model.RecoveryPointLifecycleTombstone
			if err := db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&beforeTombstone).Error; err != nil {
				t.Fatalf("load active proof tombstone: %v", err)
			}
			beforeCalls := fixture.deleter.calls
			fixture.clock = retryAt

			pending, err := restarted.flushDueSettledAuditBeforeHeartbeat(
				context.Background(), fixture.attempt,
			)
			if err != nil || pending {
				t.Fatalf("active proof due audit pending=%v error=%v, want emitted", pending, err)
			}
			current, err := restarted.loadAttempt(context.Background(), fixture.attempt.ID)
			if err != nil {
				t.Fatalf("load active proof attempt after audit: %v", err)
			}
			if current.Phase != backupasset.LifecyclePhaseTombstoning ||
				current.RetryAt != nil || audit.Writes() != 1 ||
				acceptanceCountSlots(t, db, fixture.attempt.ID) != 1 ||
				fixture.deleter.calls != beforeCalls {
				t.Fatalf("active proof audit attempt=%+v writes=%d slots=%d provider_calls=%d",
					current, audit.Writes(), acceptanceCountSlots(t, db, fixture.attempt.ID), fixture.deleter.calls)
			}
			afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			var afterLease model.RecoveryPointLease
			if err := db.First(&afterLease, "id = ?", beforeClaim.LeaseID).Error; err != nil {
				t.Fatalf("reload active proof lease after audit: %v", err)
			}
			var afterTombstone model.RecoveryPointLifecycleTombstone
			if err := db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&afterTombstone).Error; err != nil {
				t.Fatalf("reload active proof tombstone after audit: %v", err)
			}
			if !reflect.DeepEqual(beforeClaim, afterClaim) ||
				!reflect.DeepEqual(beforeLease, afterLease) ||
				!reflect.DeepEqual(beforeTombstone, afterTombstone) {
				t.Fatalf("active proof audit changed immutable rows claim=%t lease=%t tombstone=%t",
					!reflect.DeepEqual(beforeClaim, afterClaim),
					!reflect.DeepEqual(beforeLease, afterLease),
					!reflect.DeepEqual(beforeTombstone, afterTombstone))
			}

			completed, err := restarted.Advance(context.Background(), fixture.attempt.ID)
			if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete ||
				audit.Writes() != 1 || acceptanceCountSlots(t, db, fixture.attempt.ID) != 1 ||
				fixture.deleter.calls != beforeCalls {
				t.Fatalf("active proof completion=%+v writes=%d slots=%d provider_calls=%d error=%v",
					completed, audit.Writes(), acceptanceCountSlots(t, db, fixture.attempt.ID),
					fixture.deleter.calls, err)
			}
			afterClaim = acceptanceLoadClaim(t, db, fixture.attempt.ID)
			if err := db.First(&afterLease, "id = ?", beforeClaim.LeaseID).Error; err != nil {
				t.Fatalf("reload active proof lease after completion: %v", err)
			}
			if err := db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&afterTombstone).Error; err != nil {
				t.Fatalf("reload active proof tombstone after completion: %v", err)
			}
			if !reflect.DeepEqual(beforeClaim, afterClaim) ||
				!reflect.DeepEqual(beforeLease, afterLease) ||
				!reflect.DeepEqual(beforeTombstone, afterTombstone) {
				t.Fatalf("active proof completion changed immutable rows claim=%t lease=%t tombstone=%t",
					!reflect.DeepEqual(beforeClaim, afterClaim),
					!reflect.DeepEqual(beforeLease, afterLease),
					!reflect.DeepEqual(beforeTombstone, afterTombstone))
			}
		})
	}
}

func runAcceptanceRenewals(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, _, _ := newAcceptanceRegistryFixture(t, db, 30200,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("c", 64)})
	fixture.coordinator.effectClaimTTL = 10 * time.Minute
	entered, release := make(chan struct{}), make(chan struct{})
	deleter.SetBarrier(entered, release)
	wakeRequests := make(chan chan time.Time, 4)
	fixture.coordinator.effectClaimAfter = func(time.Duration) <-chan time.Time {
		wake := make(chan time.Time, 1)
		wakeRequests <- wake
		return wake
	}
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	done := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("renewal provider did not start")
	}
	var wake chan time.Time
	var previous model.RecoveryPointLifecycleEffectClaim
	for cycle := range 2 {
		if wake == nil {
			select {
			case wake = <-wakeRequests:
			case <-time.After(5 * time.Second):
				t.Fatalf("renewal cycle %d did not request a wake", cycle)
			}
		}
		current := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		if current.State != "in_flight" {
			t.Fatalf("renewal cycle %d claim=%+v, want in-flight", cycle, current)
		}
		fixture.clock = current.DeadlineAt.UTC().Add(-time.Second)
		wake <- fixture.clock
		select {
		case wake = <-wakeRequests:
			renewed := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			if renewed.State != "in_flight" || !renewed.DeadlineAt.After(current.DeadlineAt) {
				t.Fatalf("renewal cycle %d claim before=%+v after=%+v, want renewed deadline", cycle, current, renewed)
			}
			previous = renewed
		case err := <-done:
			t.Fatalf("renewal cycle %d ended before renewal completed: err=%v", cycle, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("renewal cycle %d did not complete", cycle)
		}
	}
	if previous.DeadlineAt.IsZero() || !previous.DeadlineAt.After(fixture.clock) {
		t.Fatalf("renewal deadline=%v, want after injected clock %v", previous.DeadlineAt, fixture.clock)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("renewed provider execution: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renewed provider execution did not finish")
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if claim.State != "proven" || deleter.Calls() != 1 ||
		acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("renewed claim=%+v provider calls=%d claims=%d tombstones=%d, want proven/one/one/one",
			claim, deleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
	}
}

func runAcceptanceCrashTakeover(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, _, adapter := newAcceptanceRegistryFixture(t, db, 30300,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("d", 64)})
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	deleter.SetError(errors.New("simulated executor crash after provider invocation"))
	failed, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	deleter.SetError(nil)
	if err == nil || failed.Phase != backupasset.LifecyclePhaseProviderDelete || failed.RetryAt == nil {
		t.Fatalf("crashed execution attempt=%+v error=%v, want uncertain provider_delete", failed, err)
	}
	oldClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if oldClaim.State != "uncertain" || acceptanceCountTombstones(t, db, fixture.pointID) != 0 ||
		deleter.Calls() != 1 {
		t.Fatalf("crashed claim=%+v provider calls=%d tombstones=%d, want uncertain/one/zero",
			oldClaim, deleter.Calls(), acceptanceCountTombstones(t, db, fixture.pointID))
	}
	oldAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("load crashed attempt: %v", err)
	}
	var oldLease model.RecoveryPointLease
	if err := db.First(&oldLease, "id = ?", oldClaim.LeaseID).Error; err != nil {
		t.Fatalf("load crashed lease: %v", err)
	}
	fixture.clock = oldLease.LeaseExpiresAt.UTC().Add(time.Second)
	restarted := acceptanceCloneCoordinator(t, fixture, adapter, testOpaqueID(30305))
	takenOver, err := restarted.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || takenOver.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("takeover attempt=%+v error=%v, want tombstoning", takenOver, err)
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if claim.State != "proven" || claim.ExecutionID == oldClaim.ExecutionID ||
		claim.TargetIdentityDigest != oldClaim.TargetIdentityDigest ||
		claim.LeaseFenceTokenHash == oldClaim.LeaseFenceTokenHash ||
		!claim.DeadlineAt.After(oldClaim.DeadlineAt) ||
		takenOver.TransitionRevision <= oldAttempt.TransitionRevision ||
		deleter.Calls() != 2 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("takeover claim=%+v old=%+v attempt=%+v old_attempt=%+v calls=%d claims=%d tombstones=%d",
			claim, oldClaim, takenOver, oldAttempt, deleter.Calls(),
			acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
	}
	var newLease model.RecoveryPointLease
	if err := db.First(&newLease, "id = ?", claim.LeaseID).Error; err != nil {
		t.Fatalf("load refreshed takeover lease: %v", err)
	}
	if newLease.AttemptID == oldLease.AttemptID || newLease.FenceToken == oldLease.FenceToken ||
		!newLease.LeaseExpiresAt.After(oldLease.LeaseExpiresAt) {
		t.Fatalf("takeover lease old=%+v new=%+v, want refreshed fence/deadline", oldLease, newLease)
	}
	completed, err := restarted.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("takeover completion=%+v error=%v", completed, err)
	}
}

func runAcceptanceStaleFence(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, _, adapter := newAcceptanceRegistryFixture(t, db, 30400,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("e", 64)})
	wakeRequests := make(chan chan time.Time, 4)
	fixture.coordinator.effectClaimAfter = func(time.Duration) <-chan time.Time {
		wake := make(chan time.Time, 1)
		wakeRequests <- wake
		return wake
	}
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	deleter.SetBarrier(entered, release)
	oldDone := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	go func() {
		attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		oldDone <- struct {
			attempt LifecycleAttempt
			err     error
		}{attempt, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stale-fence execution did not reach registered provider")
	}
	select {
	case <-wakeRequests:
	case <-time.After(5 * time.Second):
		t.Fatal("stale-fence renewer did not reach deterministic wake barrier")
	}
	oldClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	oldAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("load stale-fence attempt: %v", err)
	}
	var oldLease model.RecoveryPointLease
	if err := db.First(&oldLease, "id = ?", oldClaim.LeaseID).Error; err != nil {
		t.Fatalf("load stale-fence lease: %v", err)
	}
	if err := db.Model(&model.RecoveryPointLease{}).Where("id = ?", oldLease.ID).
		Update("fence_token", strings.Repeat("8", 64)).Error; err != nil {
		t.Fatalf("replace stale-fence lease token: %v", err)
	}
	deleter.SetBarrier(nil, nil)
	deleter.SetResult(provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("f", 64)})
	restarted := acceptanceCloneCoordinator(t, fixture, adapter, testOpaqueID(30405))
	takenOver, err := restarted.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || takenOver.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("stale-fence takeover attempt=%+v error=%v", takenOver, err)
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	var newLease model.RecoveryPointLease
	if err := db.First(&newLease, "id = ?", claim.LeaseID).Error; err != nil {
		t.Fatalf("load stale-fence replacement lease: %v", err)
	}
	if claim.State != "proven" || claim.ExecutionID == oldClaim.ExecutionID ||
		claim.TargetIdentityDigest != oldClaim.TargetIdentityDigest ||
		claim.LeaseFenceTokenHash == oldClaim.LeaseFenceTokenHash ||
		claim.LeaseFenceTokenHash != hashFenceToken(newLease.FenceToken) ||
		takenOver.TransitionRevision <= oldAttempt.TransitionRevision ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("stale-fence winner claim=%+v lease=%+v old_claim=%+v attempt=%+v old_attempt=%+v",
			claim, newLease, oldClaim, takenOver, oldAttempt)
	}
	completed, err := restarted.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("stale-fence winner completion=%+v error=%v", completed, err)
	}
	winnerClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	winnerAttempt, err := restarted.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("reload stale-fence winner: %v", err)
	}
	close(release)
	select {
	case stale := <-oldDone:
		if stale.err == nil {
			t.Fatalf("stale-fence execution unexpectedly persisted: %+v", stale.attempt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale-fence execution did not resume")
	}
	afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	afterAttempt, err := restarted.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("reload stale-fence state after old execution: %v", err)
	}
	if afterClaim.State != winnerClaim.State || afterClaim.ExecutionID != winnerClaim.ExecutionID ||
		afterClaim.LeaseFenceTokenHash != winnerClaim.LeaseFenceTokenHash ||
		afterAttempt.Phase != winnerAttempt.Phase ||
		afterAttempt.TransitionRevision != winnerAttempt.TransitionRevision ||
		afterAttempt.RetryAt != winnerAttempt.RetryAt ||
		afterAttempt.BlockedReason != winnerAttempt.BlockedReason ||
		deleter.Calls() != 2 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("stale-fence old execution mutated winner: winner=%+v/%+v after=%+v/%+v calls=%d claims=%d tombstones=%d",
			winnerAttempt, winnerClaim, afterAttempt, afterClaim, deleter.Calls(),
			acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
	}
}

func runAcceptanceReceiptDeadlineRace(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, resolver, _ := newAcceptanceRegistryFixture(t, db, 30500,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("f", 64)})
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	var lease model.RecoveryPointLease
	if err := fixture.db.First(&lease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
		t.Fatalf("load receipt-deadline lease: %v", err)
	}
	verifyEntered, verifyRelease := make(chan struct{}), make(chan struct{})
	verifyBarrier := &acceptanceResolverBarrier{
		target: 2, entered: verifyEntered, release: verifyRelease,
	}
	resolver.SetBarrier(verifyBarrier)
	providerDone := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	go func() {
		attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		providerDone <- struct {
			attempt LifecycleAttempt
			err     error
		}{attempt, err}
	}()
	select {
	case <-verifyEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("receipt Tx2 Verify did not reach the deterministic barrier")
	}
	fixture.clock = lease.AbsoluteDeadline.UTC().Add(time.Second)
	close(verifyRelease)
	var got LifecycleAttempt
	var err error
	select {
	case result := <-providerDone:
		got, err = result.attempt, result.err
	case <-time.After(5 * time.Second):
		t.Fatal("receipt deadline race did not finish after Verify release")
	}
	if err == nil || got.Phase != backupasset.LifecyclePhaseProviderDelete || got.RetryAt == nil {
		t.Fatalf("deadline race attempt=%+v error=%v, want uncertain provider_delete retry", got, err)
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if verifyBarrier.Calls() != 2 || claim.State != "uncertain" ||
		acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 0 || deleter.Calls() != 1 {
		t.Fatalf("deadline race barrier_calls=%d claim=%+v claims=%d tombstones=%d provider_calls=%d",
			verifyBarrier.Calls(), claim, acceptanceCountClaims(t, db, fixture.attempt.ID),
			acceptanceCountTombstones(t, db, fixture.pointID), deleter.Calls())
	}
}

func runAcceptanceProvenDeadlineFenceRace(t *testing.T, db *gorm.DB) {
	t.Helper()
	cases := []struct {
		name       string
		clock      func(model.RecoveryPointLease) time.Time
		wantStatus backupasset.LeaseStatus
	}{
		{name: "both deadlines live", clock: func(lease model.RecoveryPointLease) time.Time { return lease.LeaseExpiresAt.Add(-time.Second) }, wantStatus: backupasset.LeaseReleased},
		{name: "short expired absolute live", clock: func(lease model.RecoveryPointLease) time.Time { return lease.LeaseExpiresAt.Add(time.Second) }, wantStatus: backupasset.LeaseExpired},
		{name: "absolute expired", clock: func(lease model.RecoveryPointLease) time.Time { return lease.AbsoluteDeadline.Add(time.Second) }, wantStatus: backupasset.LeaseExpired},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture, seedDeleter, _, _ := newAcceptanceRegistryFixture(t, db, 30600+uint64(index)*100,
				provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("1", 64)})
			acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			var beforeLease model.RecoveryPointLease
			if err := fixture.db.First(&beforeLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
				t.Fatalf("load proof lease before seed: %v", err)
			}
			proved, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if err != nil || proved.Phase != backupasset.LifecyclePhaseTombstoning {
				t.Fatalf("seed provider proof=%+v error=%v", proved, err)
			}
			if seedDeleter.Calls() != 1 {
				t.Fatalf("seed provider calls=%d, want one", seedDeleter.Calls())
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", beforeLease.ID).Updates(map[string]any{
				"status": backupasset.LeaseActive, "released_at": nil,
				"lease_expires_at": beforeLease.LeaseExpiresAt, "absolute_deadline": beforeLease.AbsoluteDeadline,
				"updated_at": fixture.clock,
			}).Error; err != nil {
				t.Fatalf("restore proof lease for lease-time cases: %v", err)
			}
			beforeAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
			if err != nil {
				t.Fatalf("load proof attempt: %v", err)
			}
			beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			proofDeleter, _, _ := acceptanceInstallRegistry(t, fixture, provider.DeletePointResult{
				Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("2", 64),
			})
			fixture.clock = testCase.clock(beforeLease)
			heartbeat, err := fixture.coordinator.Heartbeat(context.Background(), fixture.attempt.ID)
			if err != nil || heartbeat.Phase != backupasset.LifecyclePhaseTombstoning ||
				heartbeat.TransitionRevision != beforeAttempt.TransitionRevision {
				t.Fatalf("proven heartbeat=%+v error=%v, want unchanged tombstoning observation", heartbeat, err)
			}
			afterHeartbeatClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			if !reflect.DeepEqual(afterHeartbeatClaim, beforeClaim) || proofDeleter.Calls() != 0 {
				t.Fatalf("proof heartbeat mutated claim/provider: before=%+v after=%+v provider_calls=%d",
					beforeClaim, afterHeartbeatClaim, proofDeleter.Calls())
			}
			completed, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
				t.Fatalf("prove completion=%+v error=%v", completed, err)
			}
			if err := fixture.db.First(&beforeLease, "id = ?", completed.LeaseID).Error; err != nil {
				t.Fatalf("reload proof lease: %v", err)
			}
			if backupasset.LeaseStatus(beforeLease.Status) != testCase.wantStatus {
				t.Fatalf("proof lease status=%q, want %q", beforeLease.Status, testCase.wantStatus)
			}
			if proofDeleter.Calls() != 0 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
				acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
				t.Fatalf("proof branch provider_calls=%d claims=%d tombstones=%d, want zero/one/one",
					proofDeleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID),
					acceptanceCountTombstones(t, db, fixture.pointID))
			}
		})
	}
}

func runAcceptancePartialWORM(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, _, _ := newAcceptanceRegistryFixture(t, db, 30700,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("2", 64)})
	deleter.SetError(provider.ErrDeletePointWORM)
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	deleter.SetError(nil)
	if err == nil || blocked.Phase != backupasset.LifecyclePhaseProviderDelete {
		t.Fatalf("partial WORM attempt=%+v error=%v, want uncertain provider_delete", blocked, err)
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if claim.State != "uncertain" || blocked.RetryAt == nil ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 0 || deleter.Calls() != 1 {
		t.Fatalf("partial WORM claim=%+v attempt=%+v calls=%d tombstones=%d",
			claim, blocked, deleter.Calls(), acceptanceCountTombstones(t, db, fixture.pointID))
	}
	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	takenOver, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || takenOver.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("partial WORM retry=%+v error=%v, want tombstoning", takenOver, err)
	}
	claim = acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if claim.State != "proven" || deleter.Calls() != 2 ||
		acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("partial WORM retry claim=%+v calls=%d claims=%d tombstones=%d, want proven/two/one/one",
			claim, deleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID),
			acceptanceCountTombstones(t, db, fixture.pointID))
	}
}

func runAcceptanceInFlightDoesNotBlock(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, _, adapter := newAcceptanceRegistryFixture(t, db, 30800,
		provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("3", 64)})
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	deleter.SetBarrier(entered, release)
	winner := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		winner <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight registered provider did not start")
	}
	beforeAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("load in-flight attempt: %v", err)
	}
	beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	loser := acceptanceCloneCoordinator(t, fixture, adapter, testOpaqueID(30805))
	if _, err := loser.Heartbeat(context.Background(), fixture.attempt.ID); err != nil {
		t.Fatalf("in-flight heartbeat: %v", err)
	}
	if _, err := loser.Advance(context.Background(), fixture.attempt.ID); !errors.Is(err, ErrEffectClaimInFlight) {
		t.Fatalf("in-flight loser error=%v, want ErrEffectClaimInFlight", err)
	}
	afterAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("load attempt after in-flight observation: %v", err)
	}
	afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if afterAttempt.TransitionRevision != beforeAttempt.TransitionRevision ||
		afterAttempt.RetryAt != beforeAttempt.RetryAt ||
		afterAttempt.BlockedReason != beforeAttempt.BlockedReason ||
		afterClaim.ExecutionID != beforeClaim.ExecutionID || afterClaim.State != "in_flight" ||
		deleter.Calls() != 1 {
		t.Fatalf("in-flight observation mutated state before=%+v/%+v after=%+v/%+v provider_calls=%d",
			beforeAttempt, beforeClaim, afterAttempt, afterClaim, deleter.Calls())
	}
	close(release)
	select {
	case err := <-winner:
		if err != nil {
			t.Fatalf("winner after in-flight observation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight winner did not complete")
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if claim.State != "proven" || deleter.Calls() != 1 ||
		acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("in-flight final claim=%+v calls=%d claims=%d tombstones=%d, want proven/one/one/one",
			claim, deleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID),
			acceptanceCountTombstones(t, db, fixture.pointID))
	}
}

func runAcceptanceAbsoluteDeadlineAfterClaim(t *testing.T, db *gorm.DB) {
	t.Helper()
	rollbackErr := errors.New("acceptance second execution prepare failed")
	tests := []struct {
		name            string
		profile         string
		liveClaim       bool
		match           bool
		ownerMismatch   bool
		holderMismatch  bool
		identityDrift   bool
		nativeReference bool
		hold            bool
		rollback        bool
		observerError   error
		wantReason      backupasset.LifecycleBlockedReason
		wantRetry       bool
	}{
		{name: "short-live claim no adoption", profile: acceptanceLeaseShortLive, liveClaim: true},
		{name: "short-live matching observer adopts atomically", profile: acceptanceLeaseShortLive, match: true},
		{name: "short-live near-expiry matching observer renews horizon", profile: acceptanceLeaseShortLiveNearExpiry, match: true},
		{name: "short-live owner mismatch fails closed", profile: acceptanceLeaseShortLive, ownerMismatch: true},
		{name: "short-live near-expiry owner mismatch fails closed", profile: acceptanceLeaseShortLiveNearExpiry, ownerMismatch: true},
		{name: "short-expired absolute-live owner mismatch fails closed", profile: acceptanceLeaseShortExpiredAbsoluteLive, ownerMismatch: true},
		{name: "absolute-expired owner mismatch fails closed", profile: acceptanceLeaseAbsoluteExpired, ownerMismatch: true},
		{name: "short-live holder mismatch fails closed", profile: acceptanceLeaseShortLive, holderMismatch: true},
		{name: "short-live near-expiry holder mismatch fails closed", profile: acceptanceLeaseShortLiveNearExpiry, holderMismatch: true},
		{name: "short-expired absolute-live holder mismatch fails closed", profile: acceptanceLeaseShortExpiredAbsoluteLive, holderMismatch: true},
		{name: "absolute-expired holder mismatch fails closed", profile: acceptanceLeaseAbsoluteExpired, holderMismatch: true},
		{name: "short-expired absolute-live matching observer adopts atomically", profile: acceptanceLeaseShortExpiredAbsoluteLive, match: true},
		{name: "absolute-expired matching observer adopts atomically", profile: acceptanceLeaseAbsoluteExpired, match: true},
		{name: "short-live identity drift observer no adoption", profile: acceptanceLeaseShortLive, identityDrift: true, wantReason: backupasset.LifecycleBlockedProviderIdentityConflict},
		{name: "short-expired absolute-live identity drift observer no adoption", profile: acceptanceLeaseShortExpiredAbsoluteLive, identityDrift: true, wantReason: backupasset.LifecycleBlockedProviderIdentityConflict},
		{name: "absolute-expired identity drift observer no adoption", profile: acceptanceLeaseAbsoluteExpired, identityDrift: true, wantReason: backupasset.LifecycleBlockedProviderIdentityConflict},
		{name: "short-live native reference observer no adoption", profile: acceptanceLeaseShortLive, nativeReference: true, observerError: provider.ErrDeletePointNativeVersionReferenced, wantReason: backupasset.LifecycleBlockedProviderNativeVersionReferenced},
		{name: "short-expired absolute-live native reference observer no adoption", profile: acceptanceLeaseShortExpiredAbsoluteLive, nativeReference: true, observerError: provider.ErrDeletePointNativeVersionReferenced, wantReason: backupasset.LifecycleBlockedProviderNativeVersionReferenced},
		{name: "absolute-expired native reference observer no adoption", profile: acceptanceLeaseAbsoluteExpired, nativeReference: true, observerError: provider.ErrDeletePointNativeVersionReferenced, wantReason: backupasset.LifecycleBlockedProviderNativeVersionReferenced},
		{name: "short-live hold observer no adoption", profile: acceptanceLeaseShortLive, hold: true, wantReason: backupasset.LifecycleBlockedActiveHold},
		{name: "short-expired absolute-live hold observer no adoption", profile: acceptanceLeaseShortExpiredAbsoluteLive, hold: true, wantReason: backupasset.LifecycleBlockedActiveHold},
		{name: "absolute-expired hold observer no adoption", profile: acceptanceLeaseAbsoluteExpired, hold: true, wantReason: backupasset.LifecycleBlockedActiveHold},
		{name: "short-expired absolute-live second execution Prepare rollback", profile: acceptanceLeaseShortExpiredAbsoluteLive, rollback: true},
		{name: "short-expired absolute-live WORM observer retries", profile: acceptanceLeaseShortExpiredAbsoluteLive, observerError: ErrPointDeletionWORM, wantRetry: true},
		{name: "short-expired absolute-live unavailable observer retries", profile: acceptanceLeaseShortExpiredAbsoluteLive, observerError: backupasset.ErrProviderUnavailable, wantRetry: true},
		{name: "short-expired absolute-live generic observer retries", profile: acceptanceLeaseShortExpiredAbsoluteLive, observerError: errors.New("observer unavailable"), wantRetry: true},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			base := 30900 + uint64(index)*100
			var (
				fixture  *claimedExpiryFixture
				deleter  *acceptanceRegistryDeleter
				resolver *acceptanceRegistryResolver
			)
			if testCase.rollback {
				fixture, deleter, resolver, _ = newAcceptanceRegistrySecondPrepareFailureFixture(t, db, base, rollbackErr)
			} else {
				fixture, deleter, resolver, _ = newAcceptanceRegistryFixture(t, db, base,
					provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("4", 64)})
				acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			}
			if testCase.liveClaim {
				entered, release := make(chan struct{}), make(chan struct{})
				deleter.SetBarrier(entered, release)
				providerDone := make(chan error, 1)
				go func() {
					_, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
					providerDone <- err
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("live claim provider did not reach registered barrier")
				}
				beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
				got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
				if !errors.Is(err, ErrEffectClaimInFlight) || got.Phase != backupasset.LifecyclePhaseProviderDelete ||
					deleter.Calls() != 1 {
					t.Fatalf("live claim attempt=%+v error=%v provider_calls=%d, want in-flight/no mutation", got, err, deleter.Calls())
				}
				afterClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
				if afterClaim.State != beforeClaim.State || afterClaim.ExecutionID != beforeClaim.ExecutionID ||
					acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 {
					t.Fatalf("live claim changed before=%+v after=%+v", beforeClaim, afterClaim)
				}
				close(release)
				select {
				case err := <-providerDone:
					if err != nil {
						t.Fatalf("live claim winner: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("live claim winner did not finish")
				}
				if acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
					t.Fatal("live claim winner did not persist one tombstone")
				}
				return
			}
			if !testCase.rollback {
				acceptanceSeedUncertainClaimWithExpiredDeadline(t, fixture, deleter)
			}
			seedClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			beforeLease := acceptanceApplyLeaseProfile(t, fixture, seedClaim.LeaseID, testCase.profile)
			if testCase.ownerMismatch || testCase.holderMismatch {
				updates := map[string]any{}
				if testCase.ownerMismatch {
					updates["owner_id"] = "foreign-retention-owner"
					beforeLease.OwnerID = "foreign-retention-owner"
				}
				if testCase.holderMismatch {
					updates["holder_type"] = string(backupasset.LeaseHolderContentSession)
					beforeLease.HolderType = string(backupasset.LeaseHolderContentSession)
				}
				result := fixture.db.Model(&model.RecoveryPointLease{}).
					Where("id = ?", seedClaim.LeaseID).Updates(updates)
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("set acceptance lease authority mismatch rows=%d error=%v", result.RowsAffected, result.Error)
				}
			}
			beforeLeaseCount := acceptanceCountLeases(t, db, fixture.pointID)
			beforeTombstoneCount := acceptanceCountTombstones(t, db, fixture.pointID)
			acceptanceSetObserverRetryDue(t, fixture)
			beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			beforeAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
			if err != nil {
				t.Fatalf("load acceptance observer attempt: %v", err)
			}
			if testCase.hold {
				acceptanceMakeHold(t, fixture, base+90)
			}
			if testCase.identityDrift {
				resolver.SetIdentityDrift(true)
			}
			if testCase.observerError != nil {
				resolver.SetError(testCase.observerError)
			}
			providerCallsBefore := deleter.Calls()
			got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if testCase.ownerMismatch || testCase.holderMismatch {
				if err == nil || !errors.Is(err, provider.ErrDeletePointIdentityConflict) ||
					got.Phase != backupasset.LifecyclePhaseProviderDelete ||
					deleter.Calls() != providerCallsBefore {
					t.Fatalf("lease-authority-mismatch attempt=%+v error=%v provider_calls=%d, want fail-closed/no provider call",
						got, err, deleter.Calls())
				}
				if afterAttempt, loadErr := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID); loadErr != nil {
					t.Fatalf("reload lease-authority-mismatch attempt: %v", loadErr)
				} else if afterAttempt.TransitionRevision != beforeAttempt.TransitionRevision ||
					afterAttempt.Phase != beforeAttempt.Phase ||
					afterAttempt.BlockedReason != beforeAttempt.BlockedReason ||
					!acceptanceSameTimePtr(afterAttempt.RetryAt, beforeAttempt.RetryAt) {
					t.Fatalf("lease-authority-mismatch attempt mutated before=%+v after=%+v", beforeAttempt, afterAttempt)
				}
				acceptanceAssertLeaseUnchanged(t, db, beforeLease)
				acceptanceAssertClaimBindingUnchanged(t, db, beforeClaim)
				if acceptanceCountLeases(t, db, fixture.pointID) != beforeLeaseCount ||
					acceptanceCountTombstones(t, db, fixture.pointID) != beforeTombstoneCount {
					t.Fatalf("lease-authority-mismatch durable rows changed leases=%d/%d tombstones=%d/%d",
						acceptanceCountLeases(t, db, fixture.pointID), beforeLeaseCount,
						acceptanceCountTombstones(t, db, fixture.pointID), beforeTombstoneCount)
				}
				return
			}
			if testCase.match {
				if err != nil || got.Phase != backupasset.LifecyclePhaseTombstoning ||
					deleter.Calls() != providerCallsBefore+1 {
					t.Fatalf("matching adoption attempt=%+v error=%v calls=%d", got, err, deleter.Calls())
				}
				claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
				var afterLease model.RecoveryPointLease
				if err := db.First(&afterLease, "id = ?", claim.LeaseID).Error; err != nil {
					t.Fatalf("load adopted lease: %v", err)
				}
				afterAttempt, loadErr := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
				if loadErr != nil {
					t.Fatalf("load adopted attempt: %v", loadErr)
				}
				if claim.State != "proven" || claim.ExecutionID == beforeClaim.ExecutionID ||
					claim.TargetIdentityDigest != beforeClaim.TargetIdentityDigest ||
					claim.LeaseID != afterLease.ID || claim.LeaseAttemptID != afterLease.AttemptID ||
					claim.LeaseFenceTokenHash != hashFenceToken(afterLease.FenceToken) ||
					acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
					acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
					t.Fatalf("matching adoption claim=%+v before=%+v lease=%+v attempt=%+v", claim, beforeClaim, afterLease, afterAttempt)
				}
				if testCase.profile == acceptanceLeaseShortLive ||
					testCase.profile == acceptanceLeaseShortLiveNearExpiry {
					if afterLease.ID != beforeLease.ID || afterLease.AttemptID != beforeLease.AttemptID ||
						afterLease.FenceToken != beforeLease.FenceToken ||
						!afterLease.AbsoluteDeadline.UTC().Equal(beforeLease.AbsoluteDeadline.UTC()) ||
						afterAttempt.TransitionRevision != beforeAttempt.TransitionRevision+1 {
						t.Fatalf("short-live matching adoption changed lease/revision before=%+v after=%+v attempt=%+v before_attempt=%+v",
							beforeLease, afterLease, afterAttempt, beforeAttempt)
					}
					if testCase.profile == acceptanceLeaseShortLiveNearExpiry {
						horizon := fixture.clock.UTC().Add(fixture.coordinator.effectClaimTTL)
						if !afterLease.LeaseExpiresAt.UTC().After(horizon) ||
							!claim.DeadlineAt.UTC().Equal(horizon) ||
							claim.DeadlineAt.UTC().After(afterLease.LeaseExpiresAt.UTC()) {
							t.Fatalf("short-live near-expiry horizon lease=%s claim=%s horizon=%s absolute=%s",
								afterLease.LeaseExpiresAt.UTC(), claim.DeadlineAt.UTC(), horizon,
								afterLease.AbsoluteDeadline.UTC())
						}
					}
				} else if afterLease.ID == beforeLease.ID && afterLease.AttemptID == beforeLease.AttemptID &&
					afterLease.FenceToken == beforeLease.FenceToken {
					t.Fatalf("expired matching adoption did not rotate lease before=%+v after=%+v", beforeLease, afterLease)
				} else if afterAttempt.TransitionRevision < beforeAttempt.TransitionRevision+2 {
					t.Fatalf("expired matching adoption revision=%d before=%d, want lease and phase transitions",
						afterAttempt.TransitionRevision, beforeAttempt.TransitionRevision)
				}
				return
			}
			if testCase.rollback {
				if err == nil || !errors.Is(err, rollbackErr) ||
					resolver.ObserverCalls() != 1 ||
					resolver.ExecutionPrepareCalls() != 2 ||
					deleter.Calls() != providerCallsBefore {
					t.Fatalf("second execution Prepare rollback attempt=%+v error=%v observer_calls=%d execution_prepare_calls=%d provider_calls=%d",
						got, err, resolver.ObserverCalls(), resolver.ExecutionPrepareCalls(), deleter.Calls())
				}
				afterAttempt, loadErr := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
				if loadErr != nil {
					t.Fatalf("reload rollback attempt: %v", loadErr)
				}
				if afterAttempt.Phase != beforeAttempt.Phase ||
					afterAttempt.TransitionRevision != beforeAttempt.TransitionRevision ||
					afterAttempt.BlockedReason != beforeAttempt.BlockedReason ||
					!acceptanceSameTimePtr(afterAttempt.RetryAt, beforeAttempt.RetryAt) {
					t.Fatalf("second execution Prepare changed attempt before=%+v after=%+v", beforeAttempt, afterAttempt)
				}
				acceptanceAssertLeaseUnchanged(t, db, beforeLease)
				acceptanceAssertClaimBindingUnchanged(t, db, beforeClaim)
				if acceptanceCountLeases(t, db, fixture.pointID) != beforeLeaseCount ||
					acceptanceCountTombstones(t, db, fixture.pointID) != 0 {
					t.Fatalf("second execution Prepare persisted lease/tombstone rows leases=%d want=%d tombstones=%d",
						acceptanceCountLeases(t, db, fixture.pointID), beforeLeaseCount,
						acceptanceCountTombstones(t, db, fixture.pointID))
				}
				return
			}
			if testCase.wantRetry {
				if err != nil || got.Phase != backupasset.LifecyclePhaseProviderDelete || got.RetryAt == nil ||
					!got.RetryAt.After(fixture.clock) {
					t.Fatalf("generic observer attempt=%+v error=%v, want provider_delete retry", got, err)
				}
				afterAttempt, loadErr := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
				if loadErr != nil {
					t.Fatalf("reload generic observer attempt: %v", loadErr)
				}
				if afterAttempt.TransitionRevision != beforeAttempt.TransitionRevision {
					t.Fatalf("generic observer changed attempt revision before=%+v after=%+v", beforeAttempt, afterAttempt)
				}
				acceptanceAssertLeaseUnchanged(t, db, beforeLease)
				acceptanceAssertClaimBindingUnchanged(t, db, beforeClaim)
				if deleter.Calls() != providerCallsBefore || acceptanceCountTombstones(t, db, fixture.pointID) != 0 {
					t.Fatalf("generic observer provider/tombstone mutation calls=%d/%d", deleter.Calls(), acceptanceCountTombstones(t, db, fixture.pointID))
				}
				return
			}
			if err != nil || got.Phase != backupasset.LifecyclePhaseBlocked || got.BlockedReason != testCase.wantReason {
				t.Fatalf("observer case attempt=%+v error=%v, want blocked/%q", got, err, testCase.wantReason)
			}
			acceptanceAssertObserverBlockNoAdoption(t, fixture, beforeAttempt, beforeClaim, beforeLease,
				got, deleter.Calls()-providerCallsBefore)
		})
	}
}

func runAcceptanceObserverResume(t *testing.T, db *gorm.DB, mode string) {
	t.Helper()
	profiles := []string{
		acceptanceLeaseShortLive,
		acceptanceLeaseShortExpiredAbsoluteLive,
		acceptanceLeaseAbsoluteExpired,
	}
	modeOffset := uint64(0)
	switch mode {
	case "hold":
		modeOffset = 0
	case "identity":
		modeOffset = 500
	case "native":
		modeOffset = 1000
	default:
		t.Fatalf("unknown acceptance observer mode %q", mode)
	}
	for index, profile := range profiles {
		t.Run(profile, func(t *testing.T) {
			base := uint64(31000) + modeOffset + uint64(index)*100
			fixture, deleter, resolver, _ := newAcceptanceRegistryFixture(t, db, base,
				provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("5", 64)})
			acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			acceptanceSeedUncertainClaimWithExpiredDeadline(t, fixture, deleter)
			providerCallsBefore := deleter.Calls()
			seedClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			beforeLease := acceptanceApplyLeaseProfile(t, fixture, seedClaim.LeaseID, profile)
			acceptanceSetObserverRetryDue(t, fixture)
			beforeClaim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			beforeAttempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
			if err != nil {
				t.Fatalf("load observer %s/%s attempt: %v", mode, profile, err)
			}
			switch mode {
			case "hold":
				acceptanceMakeHold(t, fixture, base+90)
			case "identity":
				resolver.SetIdentityDrift(true)
			case "native":
				resolver.SetError(provider.ErrDeletePointNativeVersionReferenced)
			}
			blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked {
				t.Fatalf("observer %s/%s blocked attempt=%+v error=%v", mode, profile, blocked, err)
			}
			wantReason := backupasset.LifecycleBlockedActiveHold
			switch mode {
			case "identity":
				wantReason = backupasset.LifecycleBlockedProviderIdentityConflict
			case "native":
				wantReason = backupasset.LifecycleBlockedProviderNativeVersionReferenced
			}
			if blocked.BlockedReason != wantReason {
				t.Fatalf("observer %s/%s reason=%q, want %q", mode, profile, blocked.BlockedReason, wantReason)
			}
			acceptanceAssertObserverBlockNoAdoption(t, fixture, beforeAttempt, beforeClaim, beforeLease,
				blocked, deleter.Calls()-providerCallsBefore)
			fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
			switch mode {
			case "hold":
				var holds []model.RecoveryPointHold
				if err := db.Where("recovery_point_id = ?", fixture.pointID).Find(&holds).Error; err != nil || len(holds) != 1 {
					t.Fatalf("load observer hold %s/%s: %v rows=%d", mode, profile, err, len(holds))
				}
				acceptanceReleaseHold(t, fixture, holds[0].ID)
			case "identity":
				resolver.SetIdentityDrift(false)
				resolver.SetVerifyTelemetry(true)
				deleter.SetAfterEffect(resolver.SetProviderEffectComplete)
			case "native":
				resolver.SetError(nil)
			}
			resumed, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if err != nil || resumed.Phase != backupasset.LifecyclePhaseTombstoning {
				t.Fatalf("observer %s/%s resume attempt=%+v error=%v, want tombstoning", mode, profile, resumed, err)
			}
			claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			if claim.State != "proven" || deleter.Calls() != providerCallsBefore+1 ||
				acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
				acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
				t.Fatalf("observer %s/%s resumed claim=%+v calls=%d claims=%d tombstones=%d",
					mode, profile, claim, deleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID),
					acceptanceCountTombstones(t, db, fixture.pointID))
			}
			switch mode {
			case "identity":
				if resolver.VerifyCalls() != 1 {
					t.Fatalf("observer %s/%s Verify calls=%d, want one telemetry-only Verify", mode, profile, resolver.VerifyCalls())
				}
			}
		})
	}
}

func runAcceptanceRcloneNative(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture := newClaimedExpiryFixtureWithDB(t, db, 31100)
	native := &acceptanceNativeVersionFake{
		present: map[string]bool{}, entered: make(chan struct{}), release: make(chan struct{}),
		minimumDeadline: fixture.clock.Add(30 * time.Minute),
	}
	deleter := acceptancePrepareRealRcloneLifecycle(t, fixture, native)
	var node model.Node
	if err := db.Where("name = ?", "acceptance-rclone-node").First(&node).Error; err != nil {
		t.Fatalf("load acceptance Rclone telemetry node: %v", err)
	}
	if node.SSHKeyID == nil {
		t.Fatal("acceptance Rclone telemetry node has no SSH key")
	}
	nodeID, keyID := node.ID, *node.SSHKeyID
	var key model.SSHKey
	if err := db.First(&key, "id = ?", keyID).Error; err != nil {
		t.Fatalf("load acceptance Rclone telemetry SSH key: %v", err)
	}
	deleter.SetAfterEffect(func() {
		now := fixture.clock.UTC()
		if err := db.Model(&model.Node{}).Where("id = ?", nodeID).Updates(map[string]any{
			"last_seen_at": now, "last_probe_at": now, "last_backup_at": now,
			"connection_latency": 17, "consecutive_failures": 0, "updated_at": now,
		}).Error; err != nil {
			t.Errorf("persist acceptance Rclone Node telemetry: %v", err)
		}
		if err := db.Model(&model.SSHKey{}).Where("id = ?", keyID).Updates(map[string]any{
			"last_used_at": now, "updated_at": now,
		}).Error; err != nil {
			t.Errorf("persist acceptance Rclone SSH key telemetry: %v", err)
		}
	})
	wakeRequests := make(chan chan time.Time, 4)
	fixture.coordinator.effectClaimAfter = func(time.Duration) <-chan time.Time {
		wake := make(chan time.Time, 1)
		wakeRequests <- wake
		return wake
	}
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	done := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		done <- err
	}()
	select {
	case <-native.entered:
	case err := <-done:
		current, loadErr := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
		var claims []model.RecoveryPointLifecycleEffectClaim
		claimErr := fixture.db.Where("attempt_id = ?", fixture.attempt.ID).Find(&claims).Error
		t.Fatalf("Rclone native prepare/execute returned before provider barrier: err=%v attempt=%+v load=%v claims=%+v claim_err=%v", err, current, loadErr, claims, claimErr)
	case <-time.After(5 * time.Second):
		t.Fatal("Rclone native provider did not execute")
	}
	var previous model.RecoveryPointLifecycleEffectClaim
	var wake chan time.Time
	for cycle := range 2 {
		if wake == nil {
			select {
			case wake = <-wakeRequests:
			case <-time.After(5 * time.Second):
				t.Fatalf("Rclone native renewal %d did not start", cycle)
			}
		}
		current := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		if current.State != "in_flight" {
			t.Fatalf("Rclone native renewal %d claim=%+v, want in-flight", cycle, current)
		}
		fixture.clock = current.DeadlineAt.UTC().Add(-time.Second)
		wake <- fixture.clock
		select {
		case wake = <-wakeRequests:
			renewed := acceptanceLoadClaim(t, db, fixture.attempt.ID)
			if renewed.State != "in_flight" || !renewed.DeadlineAt.After(current.DeadlineAt) {
				t.Fatalf("Rclone native renewal %d before=%+v after=%+v, want renewed deadline", cycle, current, renewed)
			}
			previous = renewed
		case err := <-done:
			t.Fatalf("Rclone native renewal %d ended early: %v", cycle, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("Rclone native renewal %d did not complete", cycle)
		}
	}
	if previous.DeadlineAt.IsZero() || !previous.DeadlineAt.After(fixture.clock) {
		t.Fatalf("Rclone native final claim deadline=%v, want after injected clock=%v", previous.DeadlineAt, fixture.clock)
	}
	close(native.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Rclone native prepare/renew/execute: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Rclone native provider did not finish")
	}
	if !native.DeadlineWasBound() {
		t.Fatal("Rclone native execution did not preserve lease absolute deadline")
	}
	request := deleter.Request()
	access, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess)
	if !ok || len(access.Versions) != 1 || access.AuthorityDigest == "" || access.Client == nil ||
		request.Point.Native == "" || request.ExpectedSourceRevision == "" || request.OperationID != fixture.attempt.ID {
		t.Fatalf("Rclone native resolved request=%+v access=%+v", request, access)
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	if claim.State != "proven" || deleter.Calls() != 1 || native.DeleteCalls() != 1 ||
		native.S3Calls() == 0 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("Rclone native claim=%+v point_deletes=%d exact_deletes=%d s3_materializations=%d claims=%d tombstones=%d",
			claim, deleter.Calls(), native.DeleteCalls(), native.S3Calls(),
			acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
	}
	var telemetryNode model.Node
	if err := db.First(&telemetryNode, "id = ?", nodeID).Error; err != nil {
		t.Fatalf("load persisted Rclone Node telemetry: %v", err)
	}
	var telemetryKey model.SSHKey
	if err := db.First(&telemetryKey, "id = ?", keyID).Error; err != nil {
		t.Fatalf("load persisted Rclone SSH key telemetry: %v", err)
	}
	if telemetryNode.LastSeenAt == nil || telemetryNode.LastProbeAt == nil ||
		telemetryNode.LastBackupAt == nil || telemetryNode.ConnectionLatency != 17 ||
		telemetryKey.LastUsedAt == nil ||
		!telemetryNode.UpdatedAt.UTC().Equal(fixture.clock.UTC()) ||
		!telemetryKey.UpdatedAt.UTC().Equal(fixture.clock.UTC()) {
		t.Fatalf("Rclone telemetry Node=%+v SSHKey=%+v, want Execute updates", telemetryNode, telemetryKey)
	}
}

type acceptanceNativeVersionFake struct {
	mu              sync.Mutex
	present         map[string]bool
	entered         chan struct{}
	release         chan struct{}
	enteredOnce     sync.Once
	deleteCalls     int
	probes          int
	s3Calls         int
	minimumDeadline time.Time
	deadline        time.Time
	deadlineValid   bool
}

func acceptanceNativeVersionKey(version provider.RcloneNativeExactVersion) string {
	return version.PhysicalKey + "\x00" + version.VersionID
}

func (fake *acceptanceNativeVersionFake) ProbeExactVersion(_ context.Context, version provider.RcloneNativeExactVersion) (provider.RcloneNativeVersionProbe, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.probes++
	return provider.RcloneNativeVersionProbe{Present: fake.present[acceptanceNativeVersionKey(version)]}, nil
}

func (fake *acceptanceNativeVersionFake) DeleteExactVersion(ctx context.Context, version provider.RcloneNativeExactVersion) error {
	deadline, hasDeadline := ctx.Deadline()
	fake.mu.Lock()
	fake.deadline = deadline
	fake.deadlineValid = hasDeadline && deadline.After(fake.minimumDeadline)
	deadlineValid := fake.deadlineValid
	fake.deleteCalls++
	entered, release := fake.entered, fake.release
	fake.mu.Unlock()
	if !deadlineValid {
		return fmt.Errorf("acceptance native delete requires future lease deadline")
	}
	if entered != nil {
		fake.enteredOnce.Do(func() { close(entered) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fake.mu.Lock()
	fake.present[acceptanceNativeVersionKey(version)] = false
	fake.mu.Unlock()
	return nil
}

func (fake *acceptanceNativeVersionFake) BucketIdentity(_ context.Context, profile provider.RcloneNativeProfile) (provider.RcloneNativeBucketIdentity, error) {
	return provider.RcloneNativeBucketIdentity{AccountID: "123456789012", Region: profile.Region, Kind: provider.RcloneNativeBucketGeneralPurpose}, nil
}

func (*acceptanceNativeVersionFake) GetVersioning(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeVersioningObservation, error) {
	return provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}, nil
}

func (*acceptanceNativeVersionFake) GetLifecycle(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeLifecycleObservation, error) {
	return provider.RcloneNativeLifecycleObservation{}, nil
}

func (*acceptanceNativeVersionFake) GetEncryption(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeBucketEncryption, error) {
	return provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}, nil
}

func (*acceptanceNativeVersionFake) ListVersionPage(context.Context, provider.RcloneNativeVersionPageRequest) (provider.RcloneNativeVersionPage, error) {
	return provider.RcloneNativeVersionPage{}, nil
}

func (*acceptanceNativeVersionFake) HeadVersion(context.Context, provider.RcloneNativeExactReadRequest) (provider.RcloneNativeExactObjectHead, error) {
	return provider.RcloneNativeExactObjectHead{}, errors.New("acceptance exact read is not used")
}

func (*acceptanceNativeVersionFake) OpenVersion(context.Context, provider.RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (*acceptanceNativeVersionFake) OpenVersionRange(context.Context, provider.RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (*acceptanceNativeVersionFake) PutControlVersion(context.Context, provider.RcloneNativeControlWriteRequest) (provider.RcloneNativeControlWriteResult, error) {
	return provider.RcloneNativeControlWriteResult{VersionID: "acceptance-control-version"}, nil
}

func (fake *acceptanceNativeVersionFake) DeleteCalls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.deleteCalls
}

func (fake *acceptanceNativeVersionFake) DeadlineWasBound() bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.deadlineValid
}

func (fake *acceptanceNativeVersionFake) S3Calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.s3Calls
}

type acceptanceRcloneNativeFactory struct {
	native          *acceptanceNativeVersionFake
	externalID      string
	sessionIdentity string
	expiresAt       time.Time
}

func (factory *acceptanceRcloneNativeFactory) AssumeRole(ctx context.Context, request provider.RcloneNativeAssumeRoleRequest) (provider.RcloneNativeAssumeRoleResult, error) {
	if err := ctx.Err(); err != nil {
		return provider.RcloneNativeAssumeRoleResult{}, err
	}
	if request.ExternalID == nil || *request.ExternalID != factory.externalID {
		return provider.RcloneNativeAssumeRoleResult{}, provider.ErrRcloneNativeAssumeRoleDenied
	}
	session, err := provider.NewRcloneNativeSession(
		"ACCEPTANCE_ACCESS_KEY_ID", "ACCEPTANCE_SECRET_ACCESS_KEY", "ACCEPTANCE_SESSION_TOKEN",
		"123456789012", factory.sessionIdentity, factory.expiresAt,
	)
	if err != nil {
		return provider.RcloneNativeAssumeRoleResult{}, err
	}
	return provider.RcloneNativeAssumeRoleResult{Session: session}, nil
}

func (*acceptanceRcloneNativeFactory) Probe(context.Context, provider.RcloneNativeDenyProbeRequest) (provider.RcloneNativeDenyProbeResult, error) {
	return provider.RcloneNativeDenyProbeResult{Denied: true}, nil
}

func (factory *acceptanceRcloneNativeFactory) S3(provider.RcloneNativeSession, provider.RcloneNativeProfile, []provider.RcloneNativeKMSKeyDigestBinding) (provider.S3Native, error) {
	factory.native.mu.Lock()
	factory.native.s3Calls++
	factory.native.mu.Unlock()
	return factory.native, nil
}

func (*acceptanceRcloneNativeFactory) KMS(provider.RcloneNativeSession, string) (provider.KMSKeyInspector, error) {
	return nil, nil
}

func (factory *acceptanceRcloneNativeFactory) BaselineS3(provider.RcloneNativeSession, provider.RcloneNativeProfile, []string) (provider.RcloneNativeBaselineS3, error) {
	return nil, errors.New("acceptance baseline source is not used")
}

func (*acceptanceRcloneNativeFactory) BootstrapCredentialsExpire(context.Context) (bool, error) {
	return false, nil
}

// --- settled-audit acceptance scenarios ---

type acceptanceAuditSink struct {
	mu          sync.Mutex
	calls       int
	writes      int
	events      []backupasset.AuditEventInput
	failLeft    int
	failErr     error
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func (sink *acceptanceAuditSink) Write(_ context.Context, event backupasset.AuditEventInput) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.calls++
	if sink.failLeft > 0 {
		sink.failLeft--
		if sink.failErr != nil {
			return sink.failErr
		}
		return errors.New("acceptance audit sink failure")
	}
	sink.writes++
	sink.events = append(sink.events, event)
	return nil
}

func (sink *acceptanceAuditSink) WriteTx(ctx context.Context, _ *gorm.DB, event backupasset.AuditEventInput) error {
	sink.mu.Lock()
	sink.calls++
	fail := sink.failLeft > 0
	if fail {
		sink.failLeft--
	}
	failErr := sink.failErr
	entered, release := sink.entered, sink.release
	if entered != nil {
		sink.enteredOnce.Do(func() { close(entered) })
	}
	sink.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		if failErr != nil {
			return failErr
		}
		return errors.New("acceptance audit sink failure")
	}
	sink.mu.Lock()
	sink.writes++
	sink.events = append(sink.events, event)
	sink.mu.Unlock()
	return nil
}

func (sink *acceptanceAuditSink) Writes() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.writes
}

func (sink *acceptanceAuditSink) Events() []backupasset.AuditEventInput {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]backupasset.AuditEventInput(nil), sink.events...)
}

func acceptanceBlockedCandidate(t *testing.T, db *gorm.DB, base uint64, reason backupasset.LifecycleBlockedReason) (*claimedExpiryFixture, LifecycleAttempt) {
	t.Helper()
	fixture := newClaimedExpiryFixtureWithDB(t, db, base)
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	blocked, err := fixture.coordinator.block(context.Background(), fixture.attempt.ID, reason)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("seed settled-audit blocked candidate=%+v error=%v", blocked, err)
	}
	acceptanceSetRetryDue(t, fixture, fixture.attempt.ID)
	return fixture, blocked
}

func acceptanceStatuses(t *testing.T, db *gorm.DB, attemptID string) []string {
	t.Helper()
	var slots []model.RecoveryPointLifecycleAuditSlot
	if err := db.Where("attempt_id = ?", attemptID).Order("emitted_at ASC, id ASC").Find(&slots).Error; err != nil {
		t.Fatalf("load settled-audit slots: %v", err)
	}
	statuses := make([]string, 0, len(slots))
	for _, slot := range slots {
		statuses = append(statuses, slot.Status)
	}
	return statuses
}

func runAcceptanceLateHoldRegistryAudit(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture := newClaimedExpiryFixtureWithDB(t, db, 31200)
	deleter := &acceptanceRegistryDeleter{
		kind:   backupasset.ProviderRestic,
		result: provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("a", 64)},
	}
	resolver := &acceptanceRegistryResolver{kind: backupasset.ProviderRestic}
	acceptanceUseRegistry(t, fixture, backupasset.ProviderRestic, deleter, resolver)
	sink := &acceptanceAuditSink{}
	fixture.coordinator.audit = sink
	deleter.SetAfterEffect(func() {
		acceptanceMakeHold(t, fixture, 31290)
	})
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked || blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
		t.Fatalf("late-hold Registry attempt=%+v error=%v", blocked, err)
	}
	if deleter.Calls() != 1 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 {
		t.Fatalf("late-hold Registry provider calls=%d claims=%d tombstones=%d",
			deleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
	}
	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	acceptanceReleaseHold(t, fixture, testOpaqueID(31290))
	resumed, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || resumed.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("late-hold Registry resume=%+v error=%v", resumed, err)
	}
	completed, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("late-hold Registry complete=%+v error=%v", completed, err)
	}
	events := sink.Events()
	if len(events) != 1 || events[0].Fields[backupasset.AuditFieldStatus] != "deleted" || sink.Writes() != 1 || acceptanceCountSlots(t, db, fixture.attempt.ID) != 1 {
		t.Fatalf("late-hold Registry audit events=%+v writes=%d slots=%d, want one deleted", events, sink.Writes(), acceptanceCountSlots(t, db, fixture.attempt.ID))
	}
}

func runAcceptanceConcurrentBlockedAudit(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, blocked := acceptanceBlockedCandidate(t, db, 31300, backupasset.LifecycleBlockedProviderWORM)
	sink := &acceptanceAuditSink{entered: make(chan struct{}), release: make(chan struct{})}
	fixture.coordinator.audit = sink
	start := make(chan struct{})
	started := make(chan struct{}, 2)
	results := make(chan struct {
		result settledAuditWriteResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			started <- struct{}{}
			result, err := fixture.coordinator.emitSettledDeletionAuditTx(context.Background(), blocked.ID)
			results <- struct {
				result settledAuditWriteResult
				err    error
			}{result, err}
		}()
	}
	close(start)
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("blocked audit caller did not start")
		}
	}
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first blocked audit caller did not enter WriteTx")
	}
	close(sink.release)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("concurrent blocked audit result=%+v", result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent blocked audit caller did not finish")
		}
	}
	if sink.Writes() != 1 || acceptanceCountSlots(t, db, blocked.ID) != 1 {
		t.Fatalf("concurrent blocked audit writes=%d slots=%d, want one/one", sink.Writes(), acceptanceCountSlots(t, db, blocked.ID))
	}
}

func runAcceptanceSuccessAndBlockedAudit(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, blocked := acceptanceBlockedCandidate(t, db, 31400, backupasset.LifecycleBlockedActiveHold)
	sink := &acceptanceAuditSink{}
	fixture.coordinator.audit = sink
	if err := fixture.coordinator.writeSettledDeletionAudit(context.Background(), blocked); err != nil {
		t.Fatalf("write initial blocked audit: %v", err)
	}
	fixture.deleter.result = PointDeletionResult{Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("6", 64)}
	fixture.clock = fixture.clock.Add(time.Second)
	resumed := acceptanceAdvanceToTombstoning(t, fixture.coordinator, fixture.attempt.ID)
	if resumed.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("resumed phase=%q", resumed.Phase)
	}
	if sink.Writes() != 2 || acceptanceCountSlots(t, db, fixture.attempt.ID) != 2 {
		t.Fatalf("success+blocked audit writes=%d slots=%d, want two/two", sink.Writes(), acceptanceCountSlots(t, db, fixture.attempt.ID))
	}
	statuses := acceptanceStatuses(t, db, fixture.attempt.ID)
	if len(statuses) != 2 || statuses[0] != "blocked" || statuses[1] != "deleted" {
		t.Fatalf("success+blocked statuses=%v, want blocked/deleted", statuses)
	}
}

func runAcceptanceAuditRollback(t *testing.T, db *gorm.DB) {
	t.Helper()
	workerFixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 2, eligiblePoints: 2, db: db,
	})
	ctx := context.Background()
	if err := workerFixture.worker.selectAndClaim(ctx, 2); err != nil {
		t.Fatalf("worker select and claim for audit rollback: %v", err)
	}
	attempts, err := workerFixture.worker.coordinator.ListIncompleteAttemptsAfter(ctx, 2, "")
	if err != nil || len(attempts) != 2 {
		t.Fatalf("worker claimed attempts=%+v error=%v, want two", attempts, err)
	}
	for _, attempt := range attempts {
		current := attempt
		for current.Phase != backupasset.LifecyclePhaseProviderDelete {
			current, err = workerFixture.worker.coordinator.Advance(ctx, current.ID)
			if err != nil {
				t.Fatalf("advance worker audit attempt %s: %v", attempt.ID, err)
			}
		}
	}
	blocked, err := workerFixture.worker.coordinator.block(ctx, attempts[0].ID, backupasset.LifecycleBlockedProviderWORM)
	if err != nil {
		t.Fatalf("block first worker audit attempt: %v", err)
	}
	due := workerFixture.clock.Add(-time.Second)
	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", blocked.ID).
		Update("retry_at", due).Error; err != nil {
		t.Fatalf("make first worker audit retry due: %v", err)
	}
	secondBefore, err := workerFixture.worker.coordinator.loadAttempt(ctx, attempts[1].ID)
	if err != nil {
		t.Fatalf("load second worker audit attempt: %v", err)
	}
	sink := &acceptanceAuditSink{failLeft: 1, failErr: errors.New("transactional audit failure")}
	workerFixture.worker.attemptAfterID = ""
	workerFixture.worker.coordinator.audit = sink
	if err := workerFixture.worker.settleClaimed(ctx, 2); err == nil {
		t.Fatal("worker settle unexpectedly ignored failed pre-heartbeat audit")
	}
	firstAfterFailure, err := workerFixture.worker.coordinator.loadAttempt(ctx, attempts[0].ID)
	if err != nil {
		t.Fatalf("load first worker audit attempt after failure: %v", err)
	}
	secondAfterFailure, err := workerFixture.worker.coordinator.loadAttempt(ctx, attempts[1].ID)
	if err != nil {
		t.Fatalf("load second worker audit attempt after failure: %v", err)
	}
	if firstAfterFailure.RetryAt == nil || firstAfterFailure.RetryAt.Before(workerFixture.clock) ||
		reflect.DeepEqual(secondAfterFailure, secondBefore) {
		t.Fatalf("worker audit failure did not isolate later sibling: first=%+v second_before=%+v second_after=%+v",
			firstAfterFailure, secondBefore, secondAfterFailure)
	}
	firstPending := firstAfterFailure
	secondPending := secondAfterFailure
	if err := workerFixture.worker.settleClaimed(ctx, 2); err != nil {
		t.Fatalf("worker pending audit revisit: %v", err)
	}
	firstAfterPending, err := workerFixture.worker.coordinator.loadAttempt(ctx, attempts[0].ID)
	if err != nil {
		t.Fatalf("load first pending worker audit attempt: %v", err)
	}
	secondAfterPending, err := workerFixture.worker.coordinator.loadAttempt(ctx, attempts[1].ID)
	if err != nil {
		t.Fatalf("load second pending worker audit attempt: %v", err)
	}
	if !reflect.DeepEqual(firstAfterPending, firstPending) || !reflect.DeepEqual(secondAfterPending, secondPending) {
		t.Fatalf("worker pending audit revisit mutated attempts: first=%+v/%+v second=%+v/%+v",
			firstPending, firstAfterPending, secondPending, secondAfterPending)
	}
	workerFixture.clock = firstAfterFailure.RetryAt.UTC().Add(time.Second)
	if err := workerFixture.worker.settleClaimed(ctx, 2); err != nil {
		t.Fatalf("worker due audit retry: %v", err)
	}
	if sink.Writes() != 1 || acceptanceCountSlots(t, db, attempts[0].ID) != 1 {
		t.Fatalf("worker due audit writes=%d slots=%d, want one/one", sink.Writes(), acceptanceCountSlots(t, db, attempts[0].ID))
	}
}

func acceptanceAdditionalClaim(t *testing.T, fixture *claimedExpiryFixture, base uint64) LifecycleAttempt {
	t.Helper()
	repositoryID := testOpaqueID(base)
	identity := "restic:native:" + strings.Repeat("1", 64)
	if err := fixture.db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &identity,
		DisplayName: "acceptance-secondary", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
	}).Error; err != nil {
		t.Fatalf("seed secondary acceptance repository: %v", err)
	}
	pointID := testOpaqueID(base + 1)
	point := newSelectionPoint(pointID, repositoryID, nil, fixture.clock.Add(-96*time.Hour), 3)
	point.PointRevision = 30
	point.EncryptedProviderLocator = `{"snapshot":"secondary-acceptance"}`
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatalf("seed secondary acceptance point: %v", err)
	}
	policyID := testOpaqueID(base + 2)
	if err := fixture.db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID, Revision: 1,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1,
		CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
	}).Error; err != nil {
		t.Fatalf("seed secondary acceptance policy: %v", err)
	}
	selection := &Selection{PolicyID: policyID, PolicyRevision: 1, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("7", 64), EvaluatedAt: fixture.clock,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 30, CapabilityRevision: 3}}}
	previousNewID := fixture.coordinator.newID
	fixture.coordinator.newID = func() (string, error) { return testOpaqueID(base + 3), nil }
	attempt, err := fixture.coordinator.Claim(context.Background(), ClaimRequest{RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: selection})
	fixture.coordinator.newID = previousNewID
	if err != nil {
		t.Fatalf("claim secondary acceptance point: %v", err)
	}
	return attempt
}

func runAcceptanceAuditAttemptIsolation(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture := newClaimedExpiryFixtureWithDB(t, db, 31600)
	first := acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	blockedFirst, err := fixture.coordinator.block(context.Background(), first.ID, backupasset.LifecycleBlockedProviderWORM)
	if err != nil {
		t.Fatalf("block first audit attempt: %v", err)
	}
	acceptanceSetRetryDue(t, fixture, first.ID)
	second := acceptanceAdditionalClaim(t, fixture, 31700)
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, second.ID)
	blockedSecond, err := fixture.coordinator.block(context.Background(), second.ID, backupasset.LifecycleBlockedProviderWORM)
	if err != nil {
		t.Fatalf("block second audit attempt: %v", err)
	}
	acceptanceSetRetryDue(t, fixture, second.ID)
	sink := &acceptanceAuditSink{}
	fixture.coordinator.audit = sink
	if err := fixture.coordinator.writeSettledDeletionAudit(context.Background(), blockedFirst); err != nil {
		t.Fatalf("write first isolated audit: %v", err)
	}
	if err := fixture.coordinator.writeSettledDeletionAudit(context.Background(), blockedSecond); err != nil {
		t.Fatalf("write second isolated audit: %v", err)
	}
	if sink.Writes() != 2 || acceptanceCountSlots(t, db, first.ID) != 1 || acceptanceCountSlots(t, db, second.ID) != 1 {
		t.Fatalf("isolated audit writes=%d first_slots=%d second_slots=%d", sink.Writes(), acceptanceCountSlots(t, db, first.ID), acceptanceCountSlots(t, db, second.ID))
	}
	if got := acceptanceStatuses(t, db, first.ID); len(got) != 1 || got[0] != "blocked" {
		t.Fatalf("first isolated statuses=%v", got)
	}
	if got := acceptanceStatuses(t, db, second.ID); len(got) != 1 || got[0] != "blocked" {
		t.Fatalf("second isolated statuses=%v", got)
	}
}

func runAcceptanceAuditStatusMatrix(t *testing.T, db *gorm.DB) {
	t.Helper()
	for index, firstStatus := range []string{"blocked", "identity_conflict"} {
		t.Run(firstStatus+"-then-observation", func(t *testing.T) {
			fixture := newClaimedExpiryFixtureWithDB(t, db, 31800+uint64(index)*100)
			acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			firstReason := backupasset.LifecycleBlockedProviderWORM
			secondReason := backupasset.LifecycleBlockedProviderIdentityConflict
			if firstStatus == "identity_conflict" {
				firstReason, secondReason = secondReason, firstReason
			}
			first, err := fixture.coordinator.block(context.Background(), fixture.attempt.ID, firstReason)
			if err != nil {
				t.Fatalf("block first status: %v", err)
			}
			acceptanceSetRetryDue(t, fixture, first.ID)
			sink := &acceptanceAuditSink{}
			fixture.coordinator.audit = sink
			if err := fixture.coordinator.writeSettledDeletionAudit(context.Background(), first); err != nil {
				t.Fatalf("write first status: %v", err)
			}
			fixture.clock = fixture.clock.Add(time.Second)
			if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", first.ID).Updates(map[string]any{
				"blocked_reason": string(secondReason), "retry_at": fixture.clock.Add(-time.Second),
			}).Error; err != nil {
				t.Fatalf("change observational status: %v", err)
			}
			second, err := fixture.coordinator.loadAttempt(context.Background(), first.ID)
			if err != nil {
				t.Fatalf("reload observational status: %v", err)
			}
			if err := fixture.coordinator.writeSettledDeletionAudit(context.Background(), second); err != nil {
				t.Fatalf("write second status: %v", err)
			}
			fixture.clock = fixture.clock.Add(time.Second)
			fixture.deleter.result = PointDeletionResult{Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("8", 64)}
			acceptanceAdvanceToTombstoning(t, fixture.coordinator, first.ID)
			statuses := acceptanceStatuses(t, db, first.ID)
			if len(statuses) != 3 || statuses[0] != firstStatus || statuses[1] == statuses[0] || statuses[2] != "deleted" {
				t.Fatalf("status matrix first=%q statuses=%v", firstStatus, statuses)
			}
			if sink.Writes() != 3 {
				t.Fatalf("status matrix first=%q writes=%d, want three", firstStatus, sink.Writes())
			}
			if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_point_lifecycle_audit_slots_terminal_acceptance
				ON recovery_point_lifecycle_audit_slots(attempt_id) WHERE status IN ('deleted', 'already_absent')`).Error; err != nil {
				t.Fatalf("create terminal audit uniqueness guard: %v", err)
			}
			bad := model.RecoveryPointLifecycleAuditSlot{ID: testOpaqueID(31890 + uint64(index)), AttemptID: first.ID, Status: "already_absent", EmittedAt: fixture.clock, CreatedAt: fixture.clock}
			if err := db.Create(&bad).Error; err == nil {
				t.Fatal("terminal status matrix accepted a second terminal slot")
			}
			if acceptanceCountSlots(t, db, first.ID) != 3 {
				t.Fatal("conflicting terminal insertion changed slot count")
			}
		})
	}
}

func runAcceptanceStaleBlockedReceipt(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, blocked := acceptanceBlockedCandidate(t, db, 32000, backupasset.LifecycleBlockedActiveHold)
	point, err := func() (model.RecoveryPoint, error) {
		var point model.RecoveryPoint
		err := db.First(&point, "id = ?", fixture.pointID).Error
		return point, err
	}()
	if err != nil {
		t.Fatalf("load stale blocked point: %v", err)
	}
	now := fixture.clock
	digest := strings.Repeat("9", 64)
	if err := db.Create(&model.RecoveryPointLifecycleTombstone{
		RecoveryPointID: point.ID, RepositoryID: point.RepositoryID, OriginalSemantics: point.Semantics,
		TerminalOperation: string(backupasset.LifecycleRetentionExpire), TerminalState: string(backupasset.RecoveryPointExpired), ManagedHistory: true,
		DeletionReceiptDigest: &digest, PurgedAt: &now, ResultCode: string(PointDeletionDeleted), CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("insert stale blocked provider receipt: %v", err)
	}
	sink := &acceptanceAuditSink{}
	fixture.coordinator.audit = sink
	if err := fixture.coordinator.writeSettledDeletionAudit(context.Background(), blocked); err != nil {
		t.Fatalf("stale blocked receipt audit: %v", err)
	}
	if sink.Writes() != 1 || acceptanceCountSlots(t, db, blocked.ID) != 1 {
		t.Fatalf("stale blocked receipt writes=%d slots=%d", sink.Writes(), acceptanceCountSlots(t, db, blocked.ID))
	}
	events := sink.Events()
	if len(events) != 1 || events[0].Fields[backupasset.AuditFieldStatus] != "deleted" {
		t.Fatalf("stale blocked receipt event=%+v, want terminal deleted", events)
	}
}

// Exact PostgreSQL acceptance names required by the lifecycle PRD.
func TestLifecycleEffectClaimDualAdvancePostgres(t *testing.T) {
	runAcceptanceDualAdvance(t, newLifecycleAcceptancePostgresDB(t), false)
}
func TestLifecycleEffectClaimLateLoserCannotMutatePostgres(t *testing.T) {
	runAcceptanceLateLoser(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimProofFirstRecoveryPostgres(t *testing.T) {
	runAcceptanceProofFirstRecovery(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimSameExecutorConcurrentPostgres(t *testing.T) {
	runAcceptanceDualAdvance(t, newLifecycleAcceptancePostgresDB(t), true)
}
func TestLifecycleEffectClaimRenewsAcrossMultipleDeadlinesPostgres(t *testing.T) {
	runAcceptanceRenewals(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimCrashTakeoverPostgres(t *testing.T) {
	runAcceptanceCrashTakeover(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimStaleFencePostgres(t *testing.T) {
	runAcceptanceStaleFence(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimReceiptDeadlineRacePostgres(t *testing.T) {
	runAcceptanceReceiptDeadlineRace(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimProvenDeadlineFenceRacePostgres(t *testing.T) {
	runAcceptanceProvenDeadlineFenceRace(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimPartialWORMPostgres(t *testing.T) {
	runAcceptancePartialWORM(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimInFlightDoesNotBlockPostgres(t *testing.T) {
	runAcceptanceInFlightDoesNotBlock(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimAbsoluteDeadlineAfterClaimPostgres(t *testing.T) {
	runAcceptanceAbsoluteDeadlineAfterClaim(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleEffectClaimObserverHoldResumePostgres(t *testing.T) {
	runAcceptanceObserverResume(t, newLifecycleAcceptancePostgresDB(t), "hold")
}
func TestLifecycleEffectClaimIdentityConflictResumePostgres(t *testing.T) {
	runAcceptanceObserverResume(t, newLifecycleAcceptancePostgresDB(t), "identity")
}
func TestLifecycleEffectClaimNativeVersionReferencedResumePostgres(t *testing.T) {
	runAcceptanceObserverResume(t, newLifecycleAcceptancePostgresDB(t), "native")
}
func TestLifecycleRcloneNativePrepareRenewExecutePostgres(t *testing.T) {
	runAcceptanceRcloneNative(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleLateHoldReceiptSettledAuditUsesRegistryPointDeletionPostgres(t *testing.T) {
	runAcceptanceLateHoldRegistryAudit(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleSettledAuditConcurrentBlockedTicksPostgres(t *testing.T) {
	runAcceptanceConcurrentBlockedAudit(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleSettledAuditSuccessAndBlockedShareSlotWriterPostgres(t *testing.T) {
	runAcceptanceSuccessAndBlockedAudit(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleSettledAuditWriteTxRollbackPostgres(t *testing.T) {
	runAcceptanceAuditRollback(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleSettledAuditAttemptIsolationPostgres(t *testing.T) {
	runAcceptanceAuditAttemptIsolation(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleSettledAuditStatusMatrixPostgres(t *testing.T) {
	runAcceptanceAuditStatusMatrix(t, newLifecycleAcceptancePostgresDB(t))
}
func TestLifecycleSettledAuditStaleBlockedCallerReDerivesReceiptPostgres(t *testing.T) {
	runAcceptanceStaleBlockedReceipt(t, newLifecycleAcceptancePostgresDB(t))
}

// Local semantic coverage keeps the acceptance scenarios runnable without a
// PostgreSQL service. The exact Postgres names above remain the migration and
// row-lock proof used by CI.
func TestLifecycleEffectClaimAcceptanceSQLite(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *gorm.DB)
	}{
		{"dual advance", func(t *testing.T, db *gorm.DB) { runAcceptanceDualAdvance(t, db, false) }},
		{"same executor", func(t *testing.T, db *gorm.DB) { runAcceptanceDualAdvance(t, db, true) }},
		{"renewals", runAcceptanceRenewals},
		{"crash takeover", runAcceptanceCrashTakeover},
		{"stale fence", runAcceptanceStaleFence},
		{"receipt deadline", runAcceptanceReceiptDeadlineRace},
		{"proven deadline", runAcceptanceProvenDeadlineFenceRace},
		{"partial WORM", runAcceptancePartialWORM},
		{"in-flight", runAcceptanceInFlightDoesNotBlock},
		{"absolute deadline", runAcceptanceAbsoluteDeadlineAfterClaim},
		{"observer hold", func(t *testing.T, db *gorm.DB) { runAcceptanceObserverResume(t, db, "hold") }},
		{"proof first recovery", runAcceptanceProofFirstRecovery},
		{"observer identity", func(t *testing.T, db *gorm.DB) { runAcceptanceObserverResume(t, db, "identity") }},
		{"observer native", func(t *testing.T, db *gorm.DB) { runAcceptanceObserverResume(t, db, "native") }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) { testCase.run(t, newLifecycleCoordinatorTestDB(t)) })
	}
}

func TestLifecycleSettledAuditAcceptanceSQLite(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *gorm.DB)
	}{
		{"late hold registry", runAcceptanceLateHoldRegistryAudit},
		{"success and blocked", runAcceptanceSuccessAndBlockedAudit},
		{"rollback", runAcceptanceAuditRollback},
		{"attempt isolation", runAcceptanceAuditAttemptIsolation},
		{"status matrix", runAcceptanceAuditStatusMatrix},
		{"stale receipt", runAcceptanceStaleBlockedReceipt},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) { testCase.run(t, newLifecycleCoordinatorTestDB(t)) })
	}
}
