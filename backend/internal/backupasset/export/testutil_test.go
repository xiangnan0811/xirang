package export

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type exportBehaviorFixture struct {
	serviceHarness
	engine string
}

func openExportBehaviorSQLite(t *testing.T) exportBehaviorFixture {
	t.Helper()
	configureExportBehaviorEnvironment(t)
	db, err := database.Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "export-behavior.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeExportBehaviorDB(t, db) })
	return prepareExportBehaviorFixture(t, db, "sqlite")
}

func openExportBehaviorPostgres(t *testing.T, dsn string) exportBehaviorFixture {
	t.Helper()
	configureExportBehaviorEnvironment(t)
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("open PostgreSQL Export behavior base: %v", err)
	}
	schema := fmt.Sprintf("xirang_export_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		closeExportBehaviorDB(t, base)
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: parsed.String()})
	if err != nil {
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		closeExportBehaviorDB(t, base)
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "postgres"); err != nil {
		closeExportBehaviorDB(t, db)
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		closeExportBehaviorDB(t, base)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeExportBehaviorDB(t, db)
		if err := base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop Export behavior schema: %v", err)
		}
		closeExportBehaviorDB(t, base)
	})
	return prepareExportBehaviorFixture(t, db, "postgres")
}

func prepareExportBehaviorFixture(t *testing.T, db *gorm.DB, engine string) exportBehaviorFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	item := frozenItemFixture()
	seedExportBehaviorParents(t, db, now, item)
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 15 * time.Minute, Heartbeat: 5 * time.Minute, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(db, func() time.Time { return now })
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainExportStore); err != nil {
		t.Fatal(err)
	}
	spy := &leaseAcquireSpy{LeaseService: lease}
	resolver := &selectionResolverStub{}
	const (
		chunkBytes       int64 = 65536
		maxItemBytes     int64 = 1 << 20
		globalStoreBytes int64 = 256 << 20
	)
	selectionLimits := SelectionLimits{MaxItems: 100, MaxSourcePoints: 10, MaxLogicalBytes: 1 << 20}
	maxCiphertextBytes, err := minimumArchiveCiphertextBytesV1(
		selectionLimits.MaxLogicalBytes, selectionLimits.MaxItems, chunkBytes,
	)
	if err != nil {
		t.Fatalf("size Export behavior archive ciphertext: %v", err)
	}
	maxItemCiphertextBytes, err := ciphertextSizeV1(maxItemBytes, chunkBytes)
	if err != nil {
		t.Fatalf("size Export behavior max-item ciphertext: %v", err)
	}
	if maxItemCiphertextBytes > math.MaxInt64-maxCiphertextBytes {
		t.Fatal("Export behavior per-job store reservation overflows")
	}
	worstCaseJobStoreBytes := maxCiphertextBytes + maxItemCiphertextBytes
	if worstCaseJobStoreBytes > math.MaxInt64/3 {
		t.Fatal("Export behavior three-job store capacity overflows")
	}
	userStoreBytes := worstCaseJobStoreBytes * 3
	if userStoreBytes > globalStoreBytes {
		t.Fatalf("Export behavior user store capacity %d exceeds global capacity %d", userStoreBytes, globalStoreBytes)
	}
	serviceConfig := ServiceConfig{
		Selection: selectionLimits,
		Quota: QuotaLimits{
			GlobalActiveJobs: 8, UserActiveJobs: 2,
			GlobalStoreBytes: globalStoreBytes, UserStoreBytes: userStoreBytes,
		},
		ChunkBytes: chunkBytes, MaxItemBytes: maxItemBytes, MaxProviderBytes: 2 << 20,
		MaxCiphertextBytes: maxCiphertextBytes,
		MaxOpenReaders:     2, MaxDuration: time.Hour,
		MaxAttempts: 3, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		LeaseTTL: 15 * time.Minute, LeaseRenewMargin: 5 * time.Minute, ReadyTTL: 24 * time.Hour,
		IdempotencyTTL: 24 * time.Hour, IdempotencyKeyMaxBytes: 128,
	}
	service, err := NewService(ServiceDependencies{
		DB: db, Now: func() time.Time { return now }, Leases: spy,
		Keys: ring, Resolver: resolver, Config: serviceConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	return exportBehaviorFixture{
		serviceHarness: serviceHarness{
			db: db, lease: lease, leaseSpy: spy, resolver: resolver,
			config: serviceConfig, service: service,
		},
		engine: engine,
	}
}

func createExportTestJobWithLifecycleSequence(
	t *testing.T,
	db *gorm.DB,
	job *model.BackupAssetExportJob,
) {
	t.Helper()
	if db == nil || job == nil || job.OwnerUserID == 0 || job.CreatedAt.IsZero() {
		t.Fatal("invalid Export test job lifecycle fixture")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, job.OwnerUserID, job.CreatedAt.UTC())
		if err != nil {
			return err
		}
		sequence, err := allocateLifecycleEnqueueSequenceTx(tx, buckets.Global)
		if err != nil {
			return err
		}
		job.LifecycleEnqueueSequence = sequence
		return tx.Create(job).Error
	}); err != nil {
		t.Fatal(err)
	}
}

func seedExportBehaviorParents(t *testing.T, db *gorm.DB, now time.Time, item FrozenItem) {
	t.Helper()
	repositoryID := strings.Repeat("9", 32)
	rows := []any{
		&model.User{
			ID: 41, Username: "export-behavior-admin", PasswordHash: "hash", Role: "admin",
			Onboarded: true, CreatedAt: now, UpdatedAt: now,
		},
		&model.BackupRepository{
			ID: repositoryID, ProviderKind: "restic", DisplayName: "Export behavior",
			VersionMode: "native_snapshot", Status: "online", CapabilityRevision: 1,
			CapabilitiesJSON: "{}", ImmutabilityLevel: "backend_versioned",
			CreatedAt: now, UpdatedAt: now,
		},
		&model.RecoveryPoint{
			ID: item.Ref.RecoveryPointID, RepositoryID: repositoryID,
			Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
			SourceFingerprint: item.SourceFingerprint, CapabilityRevision: int(item.ProviderCapabilityRevision),
			CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
			PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
			RetentionUntil: item.RetentionUntil, CreatedAt: now, UpdatedAt: now,
		},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed Export behavior %T: %v", row, err)
		}
	}
}

func configureExportBehaviorEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_EXPORT_BEHAVIOR_DATA_KEY_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func closeExportBehaviorDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		return
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Errorf("resolve Export behavior database: %v", err)
		return
	}
	if err := sqlDB.Close(); err != nil {
		t.Errorf("close Export behavior database: %v", err)
	}
}
