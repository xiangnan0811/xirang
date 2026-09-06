package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/retention"
	appdatabase "xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	postgresgorm "gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The named PostgreSQL acceptance test deliberately runs against an isolated
// schema created by the real migration runner. It must never silently fall
// back to an AutoMigrate-only fixture.
func newRuntimeLifecycleAcceptancePostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	enableRuntimeLifecycleAcceptanceEncryption(t)
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	admin, err := gorm.Open(postgresgorm.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL runtime acceptance database: %v", err)
	}
	digest := sha256.Sum256([]byte(t.Name()))
	schema := fmt.Sprintf("runtime_lifecycle_accept_%x", digest[:8])
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create runtime acceptance schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgresgorm.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		t.Fatalf("open runtime acceptance schema: %v", err)
	}
	dbSQL, err := db.DB()
	if err != nil {
		t.Fatalf("runtime acceptance database handle: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("runtime acceptance admin handle: %v", err)
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

func newRuntimeLifecycleAcceptanceSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	enableRuntimeLifecycleAcceptanceEncryption(t)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open runtime acceptance SQLite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("runtime acceptance SQLite handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.SystemSetting{}, &model.User{}, &model.BackupRepository{}, &model.RecoveryPoint{},
		&model.RecoveryPointHold{}, &model.RecoveryPointLease{},
		&model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLifecycleTombstone{},
		&model.RecoveryPointLifecycleEffectClaim{}, &model.RecoveryPointLifecycleAuditSlot{},
		&model.BackupRetentionPolicy{}, &model.WrappedDomainKey{},
		&model.BackupAssetAuditCheckpoint{}, &model.BackupAssetAuditEvent{},
	); err != nil {
		t.Fatalf("migrate runtime lifecycle acceptance tables: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_runtime_lifecycle_acceptance_leases_active_owner
		ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatalf("create runtime lifecycle lease admission index: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_runtime_lifecycle_acceptance_attempts_active
		ON recovery_point_lifecycle_attempts(recovery_point_id) WHERE phase <> 'complete'`).Error; err != nil {
		t.Fatalf("create runtime lifecycle attempt admission index: %v", err)
	}
	return db
}

func enableRuntimeLifecycleAcceptanceEncryption(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_LIFECYCLE_AUDIT_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

type runtimeLifecycleAcceptanceClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (clock *runtimeLifecycleAcceptanceClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *runtimeLifecycleAcceptanceClock) Set(now time.Time) {
	clock.mu.Lock()
	clock.now = now.UTC()
	clock.mu.Unlock()
}

func TestLifecycleSettledAuditDetailPurgePostgres(t *testing.T) {
	runRuntimeLifecycleSettledAuditDetailPurge(t, newRuntimeLifecycleAcceptancePostgresDB(t))
}

func TestLifecycleSettledAuditDetailPurgeSQLite(t *testing.T) {
	runRuntimeLifecycleSettledAuditDetailPurge(t, newRuntimeLifecycleAcceptanceSQLiteDB(t))
}

func runRuntimeLifecycleSettledAuditDetailPurge(t *testing.T, db *gorm.DB) {
	t.Helper()
	clock := &runtimeLifecycleAcceptanceClock{now: time.Date(2026, 8, 17, 14, 56, 0, 0, time.UTC)}
	repositoryID := fmt.Sprintf("%032x", 0x5100)
	firstPointID := fmt.Sprintf("%032x", 0x5101)
	secondPointID := fmt.Sprintf("%032x", 0x5102)
	seedRuntimeLifecycleAcceptancePoints(t, db, repositoryID, []string{firstPointID, secondPointID}, clock.Now())

	keyring := backupasset.NewKeyring(db, clock.Now)
	writer, err := backupasset.NewAuditWriter(db, keyring, clock.Now, backupasset.AuditConfig{
		SegmentMaxEvents: 1,
		SegmentMaxAge:    24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	adapter := retentionAssetAuditAdapter{writer: writer}

	owners := &runtimeLifecycleAcceptanceOwners{}
	lifecycle, err := NewRetentionLifecycle(RetentionLifecycleDependencies{
		Content: owners, Catalog: owners, Search: owners, Processing: owners,
		Export: owners, Recovery: owners, Overlay: owners,
		OverlayBatchSize: 10, OverlayMaxPasses: 2,
	})
	if err != nil {
		t.Fatalf("NewRetentionLifecycle: %v", err)
	}
	leases, err := backupasset.NewLeaseService(db, clock.Now, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	holds, err := retention.NewHoldService(retention.HoldServiceDependencies{DB: db, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	deleter := &runtimeLifecycleAcceptancePointDeleter{
		kind: backupasset.ProviderRestic,
		result: provider.DeletePointResult{
			Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("d", 64),
		},
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{
		Prober:       &runtimeLifecycleAcceptanceProviderProber{kind: backupasset.ProviderRestic},
		PointDeleter: deleter,
	}); err != nil {
		t.Fatalf("register lifecycle acceptance provider: %v", err)
	}
	pointDeletion, err := retention.NewRegistryPointDeletion(
		db, registry, runtimeLifecycleAcceptanceDeleteResolver{},
	)
	if err != nil {
		t.Fatalf("NewRegistryPointDeletion: %v", err)
	}
	pointDeletion.SetNow(clock.Now)
	nextID := uint64(0x5200)
	coordinator, err := retention.NewCoordinator(retention.CoordinatorDependencies{
		DB: db, Leases: leases, Holds: holds, Now: clock.Now,
		NewID: func() (string, error) {
			nextID++
			return fmt.Sprintf("%032x", nextID), nil
		},
		LeaseOwnerID: retention.RetentionWorkerLeaseOwnerID,
		Admissions:   lifecycle, Cleanup: lifecycle, Deleter: pointDeletion,
		RetryDelay: time.Minute, Audit: adapter,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	policies, err := retention.NewPolicyService(retention.PolicyServiceDependencies{DB: db, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "false"); err != nil {
		t.Fatalf("disable retention selection for replay: %v", err)
	}
	auditRetention, err := retention.NewAuditRetention(retention.AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (retention.AuditRetentionConfig, error) {
			return retention.AuditRetentionConfig{DetailRetentionDays: 1, CheckpointRetentionDays: 30}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewAuditRetention: %v", err)
	}
	worker, err := retention.NewWorker(retention.WorkerDependencies{
		Foundation:  backupasset.NewFoundationService(settingsService),
		Coordinator: coordinator, Policies: policies, Holds: holds,
		Audit: auditRetention, ImportRebuild: runtimeRetentionNoopImportRebuild{},
		Metrics: retention.NoopMetrics{}, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	first := settleRuntimeLifecycleAcceptanceAttempt(t, coordinator, repositoryID, firstPointID, clock.Now())
	clock.Set(clock.Now().Add(time.Hour))
	second := settleRuntimeLifecycleAcceptanceAttempt(t, coordinator, repositoryID, secondPointID, clock.Now())
	if first.Phase != backupasset.LifecyclePhaseTombstoning || second.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("settled attempts phases=%q/%q, want tombstoning before worker replay", first.Phase, second.Phase)
	}
	providerCalls := deleter.Calls()
	if providerCalls != 2 {
		t.Fatalf("provider delete calls=%d, want one per scoped lifecycle attempt", providerCalls)
	}

	var events []model.BackupAssetAuditEvent
	if err := db.Order("segment_no ASC").Find(&events).Error; err != nil {
		t.Fatalf("load settled lifecycle audit events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("settled lifecycle audit events=%d, want two", len(events))
	}
	assertRuntimeLifecycleAcceptanceAuditEvent(t, events[0], firstPointID, first.ID)
	assertRuntimeLifecycleAcceptanceAuditEvent(t, events[1], secondPointID, second.ID)
	assertRuntimeLifecycleAcceptanceTombstones(t, db, firstPointID, secondPointID)

	clock.Set(clock.Now().Add(47 * time.Hour))
	purged, err := auditRetention.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("PurgeEligibleDetails=%d err=%v, want one old closed segment", purged, err)
	}
	var checkpoints []model.BackupAssetAuditCheckpoint
	if err := db.Order("segment_no ASC").Find(&checkpoints).Error; err != nil {
		t.Fatalf("load audit checkpoints: %v", err)
	}
	if len(checkpoints) != 2 || checkpoints[0].Status != string(backupasset.AuditSegmentDetailsPurged) ||
		checkpoints[1].Status != string(backupasset.AuditSegmentClosed) || checkpoints[0].CheckpointHash == "" ||
		checkpoints[0].DetailsPurgedAt == nil {
		t.Fatalf("runtime lifecycle audit checkpoints=%+v", checkpoints)
	}
	var oldDetails, currentDetails int64
	if err := db.Model(&model.BackupAssetAuditEvent{}).Where("segment_no = ?", checkpoints[0].SegmentNo).Count(&oldDetails).Error; err != nil {
		t.Fatalf("count purged lifecycle audit details: %v", err)
	}
	if err := db.Model(&model.BackupAssetAuditEvent{}).Where("segment_no = ?", checkpoints[1].SegmentNo).Count(&currentDetails).Error; err != nil {
		t.Fatalf("count current lifecycle audit details: %v", err)
	}
	if oldDetails != 0 || currentDetails != 1 {
		t.Fatalf("runtime lifecycle audit detail counts old/current=%d/%d, want 0/1", oldDetails, currentDetails)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("verify lifecycle audit continuity after detail purge: %v", err)
	}
	assertRuntimeLifecycleAcceptanceSlots(t, db, first.ID, second.ID)

	retryAt := clock.Now().Add(time.Minute)
	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ?", first.ID).Update("retry_at", retryAt).Error; err != nil {
		t.Fatalf("set settled lifecycle retry gate: %v", err)
	}
	clock.Set(retryAt.Add(time.Second))
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("retention Worker startup replay: %v", err)
	}
	var completed int64
	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("phase = ?", backupasset.LifecyclePhaseComplete).Count(&completed).Error; err != nil {
		t.Fatalf("count completed lifecycle attempts: %v", err)
	}
	if completed != 2 {
		t.Fatalf("completed lifecycle attempts=%d, want two", completed)
	}
	if got := deleter.Calls(); got != providerCalls {
		t.Fatalf("worker replay provider calls=%d, want unchanged %d", got, providerCalls)
	}
	var eventCount int64
	if err := db.Model(&model.BackupAssetAuditEvent{}).Count(&eventCount).Error; err != nil {
		t.Fatalf("count lifecycle audit events after replay: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("worker replay audit events=%d, want one retained current detail", eventCount)
	}
	assertRuntimeLifecycleAcceptanceSlots(t, db, first.ID, second.ID)
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("verify lifecycle audit continuity after worker replay: %v", err)
	}
	purged, err = auditRetention.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 0 {
		t.Fatalf("repeat PurgeEligibleDetails=%d err=%v, want idempotent zero", purged, err)
	}
}

func seedRuntimeLifecycleAcceptancePoints(
	t *testing.T,
	db *gorm.DB,
	repositoryID string,
	pointIDs []string,
	now time.Time,
) {
	t.Helper()
	if err := db.Create(&model.User{
		ID: 1, Username: "admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin",
	}).Error; err != nil {
		t.Fatalf("seed lifecycle acceptance user: %v", err)
	}
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("0", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic),
		RepositoryIdentity: &identity, DisplayName: "runtime lifecycle acceptance",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		CreatedAt:         now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle acceptance repository: %v", err)
	}
	policyID := fmt.Sprintf("%032x", 0x5300)
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":1}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle acceptance policy: %v", err)
	}
	capturedAt := now.Add(-48 * time.Hour)
	for _, pointID := range pointIDs {
		locator := fmt.Sprintf(`{"snapshot":"%s"}`, pointID)
		sourceDigest := sha256.Sum256([]byte("runtime-lifecycle-source:" + pointID))
		sourceFingerprint := fmt.Sprintf("%x", sourceDigest[:])
		if err := db.Create(&model.RecoveryPoint{
			ID: pointID, RepositoryID: repositoryID,
			Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
			CapturedAt: &capturedAt, CommittedAt: &capturedAt, SourceFingerprint: sourceFingerprint,
			EncryptedProviderLocator: locator, PointRevision: 30, CapabilityRevision: 3,
			CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
			PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
			CreatedAt: capturedAt, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed lifecycle acceptance point %s: %v", pointID, err)
		}
	}
}

func settleRuntimeLifecycleAcceptanceAttempt(
	t *testing.T,
	coordinator *retention.Coordinator,
	repositoryID, pointID string,
	evaluatedAt time.Time,
) retention.LifecycleAttempt {
	t.Helper()
	selection := retention.Selection{
		PolicyID: fmt.Sprintf("%032x", 0x5300), PolicyRevision: 1,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":1}}`, RuleDigest: strings.Repeat("6", 64),
		EvaluatedAt: evaluatedAt.UTC(),
		Points: []retention.SelectedPoint{{
			RecoveryPointID: pointID, PointRevision: 30, CapabilityRevision: 3,
		}},
	}
	attempt, err := coordinator.Claim(context.Background(), retention.ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire,
		PolicySelection: &selection,
	})
	if err != nil {
		t.Fatalf("claim lifecycle acceptance point %s: %v", pointID, err)
	}
	for steps := 0; attempt.Phase != backupasset.LifecyclePhaseTombstoning && steps < 10; steps++ {
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance lifecycle acceptance point %s at phase %q: %v", pointID, attempt.Phase, err)
		}
	}
	if attempt.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("lifecycle acceptance point %s stopped at phase %q, want tombstoning", pointID, attempt.Phase)
	}
	return attempt
}

func assertRuntimeLifecycleAcceptanceAuditEvent(
	t *testing.T,
	event model.BackupAssetAuditEvent,
	pointID, attemptID string,
) {
	t.Helper()
	if event.Action != string(backupasset.AuditActionRepositoryPurge) ||
		event.Outcome != string(backupasset.AuditOutcomeSuccess) ||
		event.RecoveryPointID != pointID {
		t.Fatalf("settled lifecycle audit event=%+v", event)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(event.FieldsJSON), &fields); err != nil {
		t.Fatalf("decode settled lifecycle audit fields: %v", err)
	}
	if fields[string(backupasset.AuditFieldStage)] != "settled" ||
		fields[string(backupasset.AuditFieldStatus)] != "deleted" ||
		fields[string(backupasset.AuditFieldSource)] != attemptID {
		t.Fatalf("settled lifecycle audit fields=%v, want source %s", fields, attemptID)
	}
}

func assertRuntimeLifecycleAcceptanceTombstones(t *testing.T, db *gorm.DB, pointIDs ...string) {
	t.Helper()
	var tombstones []model.RecoveryPointLifecycleTombstone
	if err := db.Where("recovery_point_id IN ?", pointIDs).Order("recovery_point_id ASC").Find(&tombstones).Error; err != nil {
		t.Fatalf("load lifecycle terminal proofs: %v", err)
	}
	if len(tombstones) != len(pointIDs) {
		t.Fatalf("lifecycle terminal proofs=%d, want %d", len(tombstones), len(pointIDs))
	}
	expected := make(map[string]bool, len(pointIDs))
	for _, pointID := range pointIDs {
		expected[pointID] = true
	}
	for _, tombstone := range tombstones {
		if !expected[tombstone.RecoveryPointID] ||
			tombstone.TerminalOperation != string(backupasset.LifecycleRetentionExpire) ||
			tombstone.TerminalState != string(backupasset.RecoveryPointExpired) ||
			!tombstone.ManagedHistory || tombstone.CreatedAt.IsZero() ||
			tombstone.PurgedAt == nil || !tombstone.PurgedAt.Equal(tombstone.CreatedAt) ||
			tombstone.DeletionReceiptDigest == nil ||
			len(*tombstone.DeletionReceiptDigest) != 64 ||
			*tombstone.DeletionReceiptDigest != strings.ToLower(*tombstone.DeletionReceiptDigest) ||
			tombstone.ResultCode != string(retention.PointDeletionDeleted) {
			t.Fatalf("invalid lifecycle terminal proof=%+v", tombstone)
		}
		delete(expected, tombstone.RecoveryPointID)
	}
	if len(expected) != 0 {
		t.Fatalf("missing lifecycle terminal proofs for points=%v", expected)
	}
}

func assertRuntimeLifecycleAcceptanceSlots(t *testing.T, db *gorm.DB, attemptIDs ...string) {
	t.Helper()
	var slots []model.RecoveryPointLifecycleAuditSlot
	if err := db.Order("attempt_id ASC").Find(&slots).Error; err != nil {
		t.Fatalf("load settled lifecycle audit slots: %v", err)
	}
	if len(slots) != len(attemptIDs) {
		t.Fatalf("settled lifecycle audit slots=%d, want %d", len(slots), len(attemptIDs))
	}
	expected := make(map[string]bool, len(attemptIDs))
	for _, attemptID := range attemptIDs {
		expected[attemptID] = true
	}
	for _, slot := range slots {
		if !expected[slot.AttemptID] || slot.Status != "deleted" ||
			slot.EmittedAt.IsZero() || slot.CreatedAt.IsZero() {
			t.Fatalf("settled lifecycle audit slot=%+v, want one deleted slot per attempt", slot)
		}
		delete(expected, slot.AttemptID)
	}
	if len(expected) != 0 {
		t.Fatalf("missing settled lifecycle audit slots for attempts=%v", expected)
	}
}

type runtimeLifecycleAcceptanceOwners struct{}

func (*runtimeLifecycleAcceptanceOwners) RevokeAndDrainRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error {
	return nil
}
func (*runtimeLifecycleAcceptanceOwners) RetireRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error {
	return nil
}
func (*runtimeLifecycleAcceptanceOwners) RevokeRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error {
	return nil
}
func (*runtimeLifecycleAcceptanceOwners) ExpireRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error {
	return nil
}
func (*runtimeLifecycleAcceptanceOwners) CancelRecoveryPointInterests(context.Context, backupasset.SourceLifecycleRequest) error {
	return nil
}
func (*runtimeLifecycleAcceptanceOwners) ReconcileSourceLifecycle(
	context.Context,
	backupasset.SourceLifecycleRequest,
	overlay.SourceLifecycle,
	int,
) (overlay.LifecycleResult, error) {
	return overlay.LifecycleResult{}, nil
}

type runtimeLifecycleAcceptanceProviderProber struct {
	kind backupasset.ProviderKind
}

func (prober *runtimeLifecycleAcceptanceProviderProber) Probe(
	context.Context,
	provider.AccessBinding,
	provider.OperationLimits,
) (provider.RepositoryObservation, error) {
	return provider.RepositoryObservation{Provider: prober.kind, Availability: backupasset.PhysicalOnline}, nil
}

type runtimeLifecycleAcceptancePointDeleter struct {
	mu     sync.Mutex
	kind   backupasset.ProviderKind
	result provider.DeletePointResult
	calls  int
}

func (deleter *runtimeLifecycleAcceptancePointDeleter) ProviderKind() backupasset.ProviderKind {
	return deleter.kind
}

func (deleter *runtimeLifecycleAcceptancePointDeleter) DeletePoint(
	ctx context.Context,
	request provider.DeletePointRequest,
) (provider.DeletePointResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return provider.DeletePointResult{}, err
		}
	}
	if err := request.Validate(); err != nil {
		return provider.DeletePointResult{}, err
	}
	deleter.mu.Lock()
	deleter.calls++
	result := deleter.result
	deleter.mu.Unlock()
	return result, nil
}

func (deleter *runtimeLifecycleAcceptancePointDeleter) Calls() int {
	deleter.mu.Lock()
	defer deleter.mu.Unlock()
	return deleter.calls
}

type runtimeLifecycleAcceptanceDeleteResolver struct{}

func (runtimeLifecycleAcceptanceDeleteResolver) ResolveDeletePoint(
	_ context.Context,
	_ *gorm.DB,
	request retention.LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if point.ID != request.RecoveryPointID || point.RepositoryID != repository.ID ||
		backupasset.ValidateOpaqueID(request.AttemptID) != nil {
		return provider.DeletePointRequest{}, provider.ErrDeletePointIdentityConflict
	}
	identity := ""
	if repository.RepositoryIdentity != nil {
		identity = *repository.RepositoryIdentity
	}
	binding := provider.AccessBinding{
		Provider:     repositoryProviderKind(repository),
		RepositoryID: repository.ID, TaskID: 1, NodeID: 1,
		IdentitySalt:  []byte(strings.Repeat("s", provider.IdentitySaltBytes)),
		EndpointFacts: []string{"runtime-lifecycle-acceptance"},
		AdapterData: provider.ResticRuntimeAccess{
			NativeRepositoryID: strings.Repeat("0", 64),
			Command: &provider.RemoteCommandAccess{Node: model.Node{
				ID: 1, Host: "localhost", Port: 22, Username: "root",
				AuthType: "password", BasePath: "/", BackupDir: "/backup",
				Password: "ACCEPTANCE_ONLY",
			}},
		},
	}
	return provider.DeletePointRequest{
		Snapshot: provider.ReadSnapshot{
			RepositoryID: repository.ID, CapabilityRevision: point.CapabilityRevision,
			SourceRevision: point.SourceFingerprint, RepositoryIdentity: identity,
			Access: binding,
		},
		Point:                  provider.PointLocator{Native: point.EncryptedProviderLocator},
		ExpectedSourceRevision: point.SourceFingerprint, OperationID: request.AttemptID,
	}, nil
}

func repositoryProviderKind(repository model.BackupRepository) backupasset.ProviderKind {
	return backupasset.ProviderKind(repository.ProviderKind)
}
