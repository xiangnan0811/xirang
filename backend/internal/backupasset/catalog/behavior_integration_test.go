package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestCatalogBehaviorSQLite(t *testing.T) {
	primary, observer := openCatalogBehaviorSQLite(t)
	runCatalogGenerationBehavior(t, primary, observer)
}

func TestCatalogBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_CATALOG_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_CATALOG_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	primary, observer := openCatalogBehaviorPostgres(t, dsn)
	runCatalogGenerationBehavior(t, primary, observer)
}

func runCatalogGenerationBehavior(t *testing.T, primary, observer *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)

	t.Run("activation statement failures preserve old active", func(t *testing.T) {
		cases := []struct {
			name         string
			callbackKind string
			ordinal      int32
		}{
			{name: "point lock", callbackKind: "query", ordinal: 1},
			{name: "manifest lock", callbackKind: "query", ordinal: 2},
			{name: "fence validation", callbackKind: "query", ordinal: 3},
			{name: "building generation lock", callbackKind: "query", ordinal: 4},
			{name: "supersede old active", callbackKind: "update", ordinal: 1},
			{name: "activate new generation", callbackKind: "update", ordinal: 2},
			{name: "reload activated generation", callbackKind: "query", ordinal: 5},
		}
		for caseIndex, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				fixture := seedCatalogBehaviorFixture(t, primary, now, 100+caseIndex)
				indexer, frozen, fence, oldActive, building, proof, digest := prepareCatalogActivation(t, fixture)
				injected := errors.New("FAKE_CATALOG_ACTIVATION_FAILURE_FOR_TEST_ONLY")
				remove, fired := registerCatalogStatementFailure(t, primary, test.callbackKind, test.ordinal, injected)

				_, err := indexer.activate(context.Background(), building, frozen, fence, proof, 0, digest)
				remove()
				if !fired() {
					t.Fatalf("activation callback %s[%d] did not fire", test.callbackKind, test.ordinal)
				}
				if !errors.Is(err, injected) {
					t.Fatalf("activate error=%v, want injected failure", err)
				}
				assertCatalogActiveGeneration(t, observer, fixture.point.ID, oldActive.ID)
				var stored model.CatalogGeneration
				if err := observer.First(&stored, "id = ?", building.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.State != string(GenerationBuilding) || stored.IsActive {
					t.Fatalf("rolled-back building generation=%+v", stored)
				}
				if err := fixture.lease.Release(context.Background(), fence); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("separate connection never observes zero active generations", func(t *testing.T) {
		fixture := seedCatalogBehaviorFixture(t, primary, now, 300)
		indexer, frozen, fence, oldActive, building, proof, digest := prepareCatalogActivation(t, fixture)
		entered := make(chan struct{})
		resume := make(chan struct{})
		remove := registerCatalogActivationPause(t, primary, entered, resume)
		errCh := make(chan error, 1)
		go func() {
			_, err := indexer.activate(context.Background(), building, frozen, fence, proof, 0, digest)
			errCh <- err
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(resume)
			remove()
			t.Fatal("activation did not reach supersede statement")
		}
		assertCatalogActiveGeneration(t, observer, fixture.point.ID, oldActive.ID)
		close(resume)
		if err := <-errCh; err != nil {
			remove()
			t.Fatal(err)
		}
		remove()
		assertCatalogActiveGeneration(t, observer, fixture.point.ID, building.ID)
		if err := fixture.lease.Release(context.Background(), fence); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("concurrent generation sequence is collision free", func(t *testing.T) {
		fixture := seedCatalogBehaviorFixture(t, primary, now, 400)
		lease, err := fixture.lease.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
			RecoveryPointID: fixture.point.ID,
			HolderType:      backupasset.LeaseHolderCatalogBuild,
			OwnerID:         catalogBuildOwnerPrefix + fixture.point.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fixture.lease.Release(context.Background(), lease.Fence) }()
		first := fixture.newIndexer(t, fixture.factory())
		second, err := NewIndexer(IndexerDependencies{
			DB: observer, Factory: fixture.factory(), Lease: fixture.lease, IdentityKeys: fixture.keys,
			Now: func() time.Time { return now }, Config: IndexerConfig{BatchSize: 2, BuildTimeout: 30 * time.Minute, MaxEntries: 100},
		})
		if err != nil {
			t.Fatal(err)
		}
		frozen, err := first.loadFrozenBuild(context.Background(), BuildRequest{
			RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan model.CatalogGeneration, 2)
		errorsCh := make(chan error, 2)
		var wait sync.WaitGroup
		for index, candidate := range []*Indexer{first, second} {
			wait.Add(1)
			go func(index int, candidate *Indexer) {
				defer wait.Done()
				<-start
				generation, buildErr := candidate.beginGeneration(context.Background(), BuildRequest{
					RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
					CorrelationID: fmt.Sprintf("sequence-%d", index),
				}, frozen, lease.Fence)
				if buildErr != nil {
					errorsCh <- buildErr
					return
				}
				results <- generation
			}(index, candidate)
		}
		close(start)
		wait.Wait()
		close(results)
		close(errorsCh)
		for err := range errorsCh {
			t.Fatalf("concurrent begin generation: %v", err)
		}
		sequences := make([]int, 0, 2)
		for generation := range results {
			sequences = append(sequences, generation.Generation)
		}
		sort.Ints(sequences)
		if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 {
			t.Fatalf("concurrent generation sequences=%v, want [1 2]", sequences)
		}
	})

	t.Run("reconciliation deactivates retired mutable projection", func(t *testing.T) {
		fixture := seedCatalogBehaviorFixture(t, primary, now, 500)
		indexer := fixture.newIndexer(t, fixture.factory())
		active, err := indexer.Build(context.Background(), BuildRequest{
			RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := primary.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.point.ID).Updates(map[string]any{
			"semantics": backupasset.PointMutableHead, "state": backupasset.RecoveryPointRetired,
			"retired_at": now, "retirement_reason": backupasset.RetirementWithdrawn,
		}).Error; err != nil {
			t.Fatal(err)
		}
		count, err := indexer.ReconcileAbandoned(context.Background(), time.Hour, 10)
		if err != nil || count != 0 {
			t.Fatalf("retired projection reconciliation count=%d err=%v", count, err)
		}
		var stored model.CatalogGeneration
		if err := observer.First(&stored, "id = ?", active.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.IsActive || stored.State != string(GenerationSuperseded) {
			t.Fatalf("retired active projection was not deactivated: %+v", stored)
		}
	})

	t.Run("ownership root ordering cursor and diff are engine identical", func(t *testing.T) {
		fixture := seedCatalogOwnershipFixture(t, primary, now.Add(time.Hour))
		baseGeneration := seedCatalogServiceGeneration(t, primary, fixture.ownedPointID, 1, true, GenerationComplete, now.Add(time.Hour))
		compareGeneration := seedCatalogServiceGeneration(t, primary, fixture.unlinkedPointID, 1, true, GenerationComplete, now.Add(time.Hour))
		for index, entry := range []struct {
			path string
			name string
			size int64
		}{
			{path: "Zeta", name: "Zeta", size: 1},
			{path: "alpha", name: "alpha", size: 2},
			{path: "same", name: "same", size: 3},
		} {
			baseID := fmt.Sprintf("%064x", 100+index)
			compareID := fmt.Sprintf("%064x", 200+index)
			seedCatalogServiceEntry(t, primary, baseGeneration, baseID, nil, entry.path, entry.name, backupasset.CatalogEntryFile, entry.size, now)
			compareSize := entry.size
			if entry.path == "same" {
				compareSize = 30
			}
			seedCatalogServiceEntry(t, primary, compareGeneration, compareID, nil, entry.path, entry.name, backupasset.CatalogEntryFile, compareSize, now)
		}
		service := newCatalogServiceForTest(t, primary, now.Add(time.Hour))
		scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
		visible, err := service.ownership.AuthorizedPointIDs(context.Background(), scope, []string{
			fixture.unownedPointID, fixture.malformedPointID, fixture.ownedPointID, fixture.unlinkedPointID,
		})
		if err != nil || len(visible) != 2 || visible[0] != fixture.ownedPointID || visible[1] != fixture.unlinkedPointID {
			t.Fatalf("engine ownership visible=%v err=%v", visible, err)
		}
		first, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{Limit: 1, Sort: EntrySortNameAsc})
		if err != nil || len(first.Items) != 1 || first.Items[0].Name != "Zeta" || first.Items[0].ParentEntryID != nil || first.NextCursor == "" {
			t.Fatalf("engine first root page=%+v err=%v", first, err)
		}
		second, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
			Limit: 1, Sort: EntrySortNameAsc, Cursor: first.NextCursor,
		})
		if err != nil || len(second.Items) != 1 || second.Items[0].Name != "alpha" {
			t.Fatalf("engine second root page=%+v err=%v", second, err)
		}
		diff, err := service.Diff(context.Background(), scope, DiffRequest{
			BaseRecoveryPointID: fixture.ownedPointID, CompareRecoveryPointID: fixture.unlinkedPointID,
			Sort: DiffSortPathAsc, Limit: 10,
		})
		if err != nil || len(diff.Items) != 1 || diff.Items[0].Kind != DiffModified ||
			diff.Items[0].Base == nil || diff.Items[0].Compare == nil || diff.Items[0].Base.Name != "same" {
			t.Fatalf("engine exact diff=%+v err=%v", diff, err)
		}
	})
}

func prepareCatalogActivation(
	t *testing.T,
	fixture catalogIndexerFixture,
) (*Indexer, frozenBuild, backupasset.LeaseFence, model.CatalogGeneration, model.CatalogGeneration, provider.CatalogReadProof, string) {
	t.Helper()
	indexer := fixture.newIndexer(t, fixture.factory())
	oldActive, err := indexer.Build(context.Background(), BuildRequest{
		RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.lease.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: fixture.point.ID,
		HolderType:      backupasset.LeaseHolderCatalogBuild,
		OwnerID:         catalogBuildOwnerPrefix + fixture.point.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := indexer.loadFrozenBuild(context.Background(), BuildRequest{
		RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	building, err := indexer.beginGeneration(context.Background(), BuildRequest{
		RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
	}, frozen, lease.Fence)
	if err != nil {
		t.Fatal(err)
	}
	accumulator, err := provider.NewCatalogProjectionAccumulator(
		backupasset.ProviderKind(frozen.repository.ProviderKind), frozen.repository.ID, frozen.point.ID, frozen.point.SourceFingerprint,
	)
	if err != nil {
		t.Fatal(err)
	}
	digest, count, err := accumulator.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	proof := provider.CatalogReadProof{
		Provider: backupasset.ProviderKind(frozen.repository.ProviderKind), Mode: frozen.mode,
		SourceRevision: frozen.point.SourceFingerprint, Manifest: frozen.proof,
		Catalog: provider.CatalogProjectionProof{
			DigestAlgorithm: "sha256", Digest: digest, EntryCount: count, Complete: true,
		},
	}
	return indexer, frozen, lease.Fence, oldActive, building, proof, digest
}

func registerCatalogStatementFailure(
	t *testing.T,
	db *gorm.DB,
	kind string,
	ordinal int32,
	injected error,
) (func(), func() bool) {
	t.Helper()
	name := fmt.Sprintf("catalog:activation_failure:%s:%d:%d", kind, ordinal, time.Now().UnixNano())
	var seen int32
	var fired atomic.Bool
	callback := func(tx *gorm.DB) {
		if atomic.AddInt32(&seen, 1) == ordinal {
			fired.Store(true)
			_ = tx.AddError(injected)
		}
	}
	var remove func()
	switch kind {
	case "query":
		if err := db.Callback().Query().After("gorm:after_query").Register(name, callback); err != nil {
			t.Fatal(err)
		}
		remove = func() {
			if err := db.Callback().Query().Remove(name); err != nil {
				t.Fatalf("remove Catalog query callback: %v", err)
			}
		}
	case "update":
		if err := db.Callback().Update().After("gorm:update").Register(name, callback); err != nil {
			t.Fatal(err)
		}
		remove = func() {
			if err := db.Callback().Update().Remove(name); err != nil {
				t.Fatalf("remove Catalog update callback: %v", err)
			}
		}
	default:
		t.Fatalf("unknown callback kind %q", kind)
	}
	return remove, fired.Load
}

func registerCatalogActivationPause(t *testing.T, db *gorm.DB, entered chan<- struct{}, resume <-chan struct{}) func() {
	t.Helper()
	name := fmt.Sprintf("catalog:activation_pause:%d", time.Now().UnixNano())
	var seen int32
	if err := db.Callback().Update().After("gorm:update").Register(name, func(_ *gorm.DB) {
		if atomic.AddInt32(&seen, 1) == 1 {
			close(entered)
			<-resume
		}
	}); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := db.Callback().Update().Remove(name); err != nil {
			t.Fatalf("remove Catalog activation pause callback: %v", err)
		}
	}
}

func assertCatalogActiveGeneration(t *testing.T, db *gorm.DB, recoveryPointID, wantID string) {
	t.Helper()
	var rows []model.CatalogGeneration
	if err := db.Where("recovery_point_id = ? AND is_active = ?", recoveryPointID, true).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != wantID || rows[0].State != string(GenerationComplete) {
		t.Fatalf("active generations=%+v, want one complete %s", rows, wantID)
	}
}

func seedCatalogBehaviorFixture(t *testing.T, db *gorm.DB, now time.Time, seed int) catalogIndexerFixture {
	t.Helper()
	id := func(offset int) string { return fmt.Sprintf("%032x", seed*10+offset) }
	repository := model.BackupRepository{
		ID: id(1), ProviderKind: string(backupasset.ProviderRsync), DisplayName: "catalog-behavior",
		VersionMode: string(backupasset.VersionFullCopyTree), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	point := model.RecoveryPoint{
		ID: id(2), RepositoryID: repository.ID, Semantics: string(backupasset.PointXirangManifest),
		State: string(backupasset.RecoveryPointCommitted), LineageJSON: "{}", SourceFingerprint: strings.Repeat("a", 64),
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("b", 64), EntryCount: 0,
		ConsistencyJSON: "{}", FidelityJSON: "{}", CapabilityRevision: repository.CapabilityRevision,
		CapabilitiesJSON: "{}", ImmutabilityLevel: repository.ImmutabilityLevel,
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: id(3), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256", Digest: point.ManifestDigest,
		Generator: "catalog-behavior", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
		EntryCount: 0, FidelityJSON: "{}", IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalogIndexerFixture{
		db: db, now: now, point: point, manifest: &manifest, lease: lease,
		keys: catalogIndexerKeySource{key: []byte("FAKE_CATALOG_ENTRY_IDENTITY_KEY_32_BYTES")},
	}
}

func openCatalogBehaviorSQLite(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	configureCatalogBehaviorEnvironment(t)
	path := t.TempDir() + "/catalog-behavior.db"
	primary, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(primary, "sqlite"); err != nil {
		t.Fatal(err)
	}
	observer, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	closeCatalogBehaviorDBs(t, primary, observer)
	return primary, observer
}

func openCatalogBehaviorPostgres(t *testing.T, dsn string) (*gorm.DB, *gorm.DB) {
	t.Helper()
	configureCatalogBehaviorEnvironment(t)
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("xirang_catalog_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	scopedDSN := parsed.String()
	primary, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: scopedDSN})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(primary, "postgres"); err != nil {
		t.Fatal(err)
	}
	observer, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: scopedDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeGormDB(primary)
		closeGormDB(observer)
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		closeGormDB(base)
	})
	return primary, observer
}

func configureCatalogBehaviorEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_CATALOG_BEHAVIOR_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func closeCatalogBehaviorDBs(t *testing.T, databases ...*gorm.DB) {
	t.Helper()
	t.Cleanup(func() {
		for _, db := range databases {
			closeGormDB(db)
		}
	})
}

func closeGormDB(db *gorm.DB) {
	if db == nil {
		return
	}
	sqlDB, err := db.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}
