package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPrepareSchemaDownRejectsEveryActiveOperation(t *testing.T) {
	for _, operation := range allResticOperations {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newAdmissionControllerFixture(t, false, nil)
			fixture.initialize(t)
			token, err := fixture.controller.Acquire(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			var callbacks atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- fixture.controller.PrepareSchemaDown(context.Background(), func() error { callbacks.Add(1); return nil })
			}()
			fixture.assertNoCallbackBeforeDrain(t, &callbacks)
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if callbacks.Load() != 1 {
				t.Fatalf("schema-down callbacks=%d, want one", callbacks.Load())
			}
		})
	}
}

func TestPrepareSchemaDownRejectsActivePublicationLease(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, false, nil)
	seedRuntimePublicationLease(t, fixture.db)
	fixture.initialize(t)
	var callbacks atomic.Int32
	err := fixture.controller.PrepareSchemaDown(context.Background(), func() error { callbacks.Add(1); return nil })
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("schema-down error=%v, want conflict", err)
	}
	if callbacks.Load() != 0 {
		t.Fatalf("schema-down callback ran %d times", callbacks.Load())
	}
}

func TestPrepareSchemaDownRejectsRetainedTombstone(t *testing.T) {
	tombstones := &runtimeTombstoneSource{installation: true}
	fixture := newAdmissionControllerFixture(t, false, tombstones)
	fixture.initialize(t)
	var callbacks atomic.Int32
	err := fixture.controller.PrepareSchemaDown(context.Background(), func() error { callbacks.Add(1); return nil })
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("schema-down tombstone error=%v, want conflict", err)
	}
	if callbacks.Load() != 0 {
		t.Fatalf("schema-down tombstone callback ran %d times", callbacks.Load())
	}
}

func TestPrepareSchemaDownInvokesMigrationOnlyAfterCleanDrain(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, false, nil)
	fixture.initialize(t)
	token, err := fixture.controller.Acquire(context.Background(), publication.OperationLegacyRetention)
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- fixture.controller.PrepareSchemaDown(context.Background(), func() error { callbacks.Add(1); return nil })
	}()
	fixture.assertNoCallbackBeforeDrain(t, &callbacks)
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("schema-down callback count=%d, want one", callbacks.Load())
	}
}

func TestPrepareApplicationDowngradeRejectsHistoryLeaseAndEveryActiveOperation(t *testing.T) {
	for _, operation := range allResticOperations {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newAdmissionControllerFixture(t, false, nil)
			seedRuntimePublicationLease(t, fixture.db)
			fixture.initialize(t)
			token, err := fixture.controller.Acquire(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			var callbacks atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- fixture.controller.PrepareApplicationDowngrade(context.Background(), func() error { callbacks.Add(1); return nil })
			}()
			fixture.assertNoCallbackBeforeDrain(t, &callbacks)
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("application downgrade error=%v, want conflict", err)
			}
			if callbacks.Load() != 0 {
				t.Fatalf("application downgrade callback ran %d times", callbacks.Load())
			}
		})
	}
}

func TestTransitionDisableRechecksHistoryAndLeaseAfterDrain(t *testing.T) {
	for _, mutation := range []struct {
		name string
		seed func(*testing.T, *gorm.DB)
	}{
		{"history", seedRuntimeNativePoint},
		{"lease", seedRuntimePublicationLease},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			fixture := newAdmissionControllerFixture(t, true, nil)
			fixture.initialize(t)
			token, err := fixture.controller.Acquire(context.Background(), publication.OperationLegacyBackup)
			if err != nil {
				t.Fatal(err)
			}
			var callbacks atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- fixture.controller.TransitionFeature(context.Background(), false, func() error { callbacks.Add(1); return nil })
			}()
			fixture.waitTransitioning(t)
			mutation.seed(t, fixture.db)
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if callbacks.Load() != 1 {
				t.Fatalf("disable callback count=%d, want one", callbacks.Load())
			}
			if mode, err := fixture.controller.CurrentMode(); err != nil || mode != publication.AdmissionRollbackSafe {
				t.Fatalf("disable mode=%s err=%v, want rollback-safe/nil", mode, err)
			}
		})
	}
}

func TestPrepareSchemaDownRechecksTombstoneAfterDrain(t *testing.T) {
	tombstones := &runtimeTombstoneSource{}
	fixture := newAdmissionControllerFixture(t, false, tombstones)
	fixture.initialize(t)
	token, err := fixture.controller.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- fixture.controller.PrepareSchemaDown(context.Background(), func() error { callbacks.Add(1); return nil })
	}()
	fixture.waitTransitioning(t)
	tombstones.setInstallation(true)
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("schema-down tombstone race error=%v, want conflict", err)
	}
	if callbacks.Load() != 0 {
		t.Fatalf("schema-down tombstone race callback ran %d times", callbacks.Load())
	}
}

func TestAdmissionInitializeUsesEnvironmentFallbackAndRollbackSafeHistory(t *testing.T) {
	t.Run("environment enables managed mode", func(t *testing.T) {
		t.Setenv("BACKUP_ASSETS_ENABLED", "true")
		fixture := newAdmissionControllerFixtureWithSettings(t, runtimeFoundationSettingsFromEnvironment(), nil)
		if err := fixture.controller.Initialize(context.Background()); err != nil {
			t.Fatal(err)
		}
		if mode, err := fixture.controller.CurrentMode(); err != nil || mode != publication.AdmissionManaged {
			t.Fatalf("initialized mode=%s err=%v, want managed/nil", mode, err)
		}
	})
	t.Run("environment disabled with history remains rollback safe", func(t *testing.T) {
		t.Setenv("BACKUP_ASSETS_ENABLED", "false")
		fixture := newAdmissionControllerFixtureWithSettings(t, runtimeFoundationSettingsFromEnvironment(), nil)
		seedRuntimeNativePoint(t, fixture.db)
		if err := fixture.controller.Initialize(context.Background()); err != nil {
			t.Fatal(err)
		}
		if mode, err := fixture.controller.CurrentMode(); err != nil || mode != publication.AdmissionRollbackSafe {
			t.Fatalf("initialized mode=%s err=%v, want rollback-safe/nil", mode, err)
		}
	})
}

func TestAdmissionInitializeActiveLeaseSelectsRollbackSafe(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, false, nil)
	seedRuntimePublicationLease(t, fixture.db)
	fixture.initialize(t)
	if mode, err := fixture.controller.CurrentMode(); err != nil || mode != publication.AdmissionRollbackSafe {
		t.Fatalf("initialized mode=%s err=%v, want rollback-safe/nil", mode, err)
	}
}

func TestAdmissionInitializeHistoryFailureLeavesControllerUninitialized(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, false, nil)
	if err := fixture.db.Exec("DROP TABLE recovery_points").Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.Initialize(context.Background()); err == nil {
		t.Fatal("history query failure initialized controller")
	}
	if _, err := fixture.controller.CurrentMode(); !errors.Is(err, ErrAdmissionNotInitialized) {
		t.Fatalf("current mode after history failure=%v, want uninitialized", err)
	}
}

func TestAdmissionInitializeLeaseQueryFailureLeavesControllerUninitialized(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, false, nil)
	if err := fixture.db.Exec("DROP TABLE recovery_point_leases").Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.Initialize(context.Background()); err == nil {
		t.Fatal("lease query failure initialized controller")
	}
	if _, err := fixture.controller.CurrentMode(); !errors.Is(err, ErrAdmissionNotInitialized) {
		t.Fatalf("current mode after lease failure=%v, want uninitialized", err)
	}
}

func TestAdmissionCurrentModeRequiresSuccessfulInitialize(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, false, nil)
	if _, err := fixture.controller.CurrentMode(); !errors.Is(err, ErrAdmissionNotInitialized) {
		t.Fatalf("current mode before initialize=%v, want uninitialized", err)
	}
	if _, err := fixture.controller.Acquire(context.Background(), publication.OperationLegacyBackup); !errors.Is(err, ErrAdmissionNotInitialized) {
		t.Fatalf("acquire before initialize=%v, want uninitialized", err)
	}
}

type admissionControllerFixture struct {
	db         *gorm.DB
	controller *AdmissionController
}

func newAdmissionControllerFixture(t *testing.T, enabled bool, tombstones backuprepository.ManagedHistoryTombstoneSource) *admissionControllerFixture {
	t.Helper()
	return newAdmissionControllerFixtureWithSettings(t, runtimeFoundationSettings(enabled), tombstones)
}

func newAdmissionControllerFixtureWithSettings(t *testing.T, settings runtimeSettings, tombstones backuprepository.ManagedHistoryTombstoneSource) *admissionControllerFixture {
	t.Helper()
	db := newRuntimeAdmissionDB(t)
	history, err := backuprepository.NewManagedHistoryResolver(backuprepository.ManagedHistoryResolverDependencies{DB: db, Tombstones: tombstones})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewAdmissionController(AdmissionControllerDependencies{Foundation: backupasset.NewFoundationService(settings), History: history})
	if err != nil {
		t.Fatal(err)
	}
	return &admissionControllerFixture{db: db, controller: controller}
}

func (fixture *admissionControllerFixture) initialize(t *testing.T) {
	t.Helper()
	if err := fixture.controller.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (fixture *admissionControllerFixture) assertNoCallbackBeforeDrain(t *testing.T, callbacks *atomic.Int32) {
	t.Helper()
	time.Sleep(25 * time.Millisecond)
	if callbacks.Load() != 0 {
		t.Fatalf("transition callback ran before drain: %d", callbacks.Load())
	}
}

func (fixture *admissionControllerFixture) waitTransitioning(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fixture.controller.barrier.mu.Lock()
		transitioning := fixture.controller.barrier.transitioning
		fixture.controller.barrier.mu.Unlock()
		if transitioning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("admission transition did not start")
}

type runtimeTombstoneSource struct {
	mu           sync.Mutex
	repository   bool
	installation bool
}

func (source *runtimeTombstoneSource) HasRepositoryManagedHistory(_ context.Context, _ string) (bool, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.repository, nil
}

func (source *runtimeTombstoneSource) HasInstallationManagedHistory(_ context.Context) (bool, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.installation, nil
}

func (source *runtimeTombstoneSource) setInstallation(value bool) {
	source.mu.Lock()
	source.installation = value
	source.mu.Unlock()
}

type runtimeSettings map[string]string

func (settings runtimeSettings) GetEffective(key string) string { return settings[key] }

func runtimeFoundationSettings(enabled bool) runtimeSettings {
	settings := runtimeFoundationSettingsFromEnvironment()
	settings["backup_assets.enabled"] = fmt.Sprintf("%t", enabled)
	return settings
}

func runtimeFoundationSettingsFromEnvironment() runtimeSettings {
	return runtimeSettings{
		"backup_assets.enabled":                          strings.TrimSpace(getenv("BACKUP_ASSETS_ENABLED", "false")),
		"backup_assets.catalog_batch_size":               "2000",
		"backup_assets.catalog_build_timeout":            "30m",
		"backup_assets.repository_reconcile_interval":    "15m",
		"backup_assets.audit_segment_max_events":         "10000",
		"backup_assets.audit_segment_max_age":            "24h",
		"backup_assets.audit_detail_retention_days":      "180",
		"backup_assets.audit_checkpoint_retention_days":  "2555",
		"backup_assets.lease_duration":                   "5m",
		"backup_assets.lease_heartbeat":                  "60s",
		"backup_assets.lease_absolute_deadline":          "168h",
		"backup_assets.provider_operation_timeout":       "2m",
		"backup_assets.provider_max_concurrency":         "4",
		"backup_assets.provider_metadata_limit_bytes":    "16777216",
		"backup_assets.publication_reconcile_interval":   "5m",
		"backup_assets.publication_reconcile_batch_size": "100",
		"backup_assets.publication_worker_concurrency":   "2",
		"backup_assets.publication_missing_grace":        "30m",
		"backup_assets.publication_stream_max_bytes":     "268435456",
		"backup_assets.manifest_timeout":                 "2h",
		"backup_assets.manifest_max_bytes":               "4294967296",
		"backup_assets.manifest_max_entries":             "10000000",
		"backup_assets.manifest_max_record_bytes":        "1048576",
		"backup_assets.manifest_max_depth":               "4096",
	}
}

func getenv(key, fallback string) string {
	// t.Setenv controls the process environment; the tiny indirection keeps the
	// settings reader focused on the same DB -> environment -> default shape.
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func newRuntimeAdmissionDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.RecoveryPoint{}, &model.RecoveryPointLease{}, &model.BackupAssetManagedHistoryLatch{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedRuntimeNativePoint(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	point := model.RecoveryPoint{
		ID: strings.Repeat("a", 32), RepositoryID: strings.Repeat("b", 32), Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointFailed),
		LineageJSON: `{}`, ConsistencyJSON: `{}`, FidelityJSON: `{}`, CapabilitiesJSON: `{}`, CapabilityRevision: 1,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
}

func seedRuntimePublicationLease(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	lease := model.RecoveryPointLease{
		ID: strings.Repeat("c", 32), RecoveryPointID: strings.Repeat("d", 32), HolderType: string(backupasset.LeaseHolderPointPublication), OwnerID: "runtime-publication-worker",
		AttemptID: strings.Repeat("e", 32), FenceToken: strings.Repeat("f", 64), Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: now.Add(time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatal(err)
	}
}
