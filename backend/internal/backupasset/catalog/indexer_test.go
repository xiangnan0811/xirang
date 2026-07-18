package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
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

type catalogIndexerKeySource struct{ key []byte }

func (source catalogIndexerKeySource) Active(_ context.Context, domain backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	if domain != backupasset.KeyDomainEntryIdentity || len(source.key) < 32 {
		return backupasset.DomainKeyMaterial{}, errors.New("FAKE_KEY_UNAVAILABLE_FOR_TEST_ONLY")
	}
	return backupasset.DomainKeyMaterial{Domain: domain, Version: 1, State: backupasset.DomainKeyActive, Key: append([]byte(nil), source.key...)}, nil
}

type catalogIndexerFactory struct {
	mu       sync.Mutex
	provider backupasset.ProviderKind
	source   string
	mode     provider.CatalogProofMode
	manifest provider.CatalogManifestProof
	records  []provider.CatalogRecord
	listErr  error
	mutate   func()
	proofMut func(*provider.CatalogReadProof)
	requests []PointReadRequest
}

func (factory *catalogIndexerFactory) OpenCatalogRead(_ context.Context, request PointReadRequest) (provider.CatalogReadSession, error) {
	factory.mu.Lock()
	factory.requests = append(factory.requests, request)
	records := append([]provider.CatalogRecord(nil), factory.records...)
	factory.mu.Unlock()
	accumulator, err := provider.NewCatalogProjectionAccumulator(
		factory.provider, request.RepositoryID, request.RecoveryPointID, factory.source,
	)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if err := accumulator.Write(record); err != nil {
			return nil, err
		}
	}
	digest, count, err := accumulator.Finalize()
	if err != nil {
		return nil, err
	}
	proof := provider.CatalogReadProof{
		Provider: factory.provider, Mode: factory.mode, SourceRevision: factory.source,
		Manifest: factory.manifest,
		Catalog:  provider.CatalogProjectionProof{DigestAlgorithm: "sha256", Digest: digest, EntryCount: count, Complete: true},
	}
	if factory.proofMut != nil {
		factory.proofMut(&proof)
	}
	return &catalogIndexerSession{records: records, proof: proof, listErr: factory.listErr, mutate: factory.mutate}, nil
}

type catalogIndexerSession struct {
	records []provider.CatalogRecord
	proof   provider.CatalogReadProof
	listErr error
	mutate  func()
	offset  int
	closed  bool
}

func (session *catalogIndexerSession) SourceRevision() string { return session.proof.SourceRevision }

func (session *catalogIndexerSession) ListCanonical(_ context.Context, request provider.PageRequest) (provider.CatalogRecordPage, error) {
	if session.closed {
		return provider.CatalogRecordPage{}, provider.ErrCatalogSessionClosed
	}
	if session.listErr != nil {
		err := session.listErr
		session.listErr = nil
		return provider.CatalogRecordPage{}, err
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 100
	}
	end := min(session.offset+limit, len(session.records))
	page := provider.CatalogRecordPage{Items: append([]provider.CatalogRecord(nil), session.records[session.offset:end]...)}
	session.offset = end
	if end < len(session.records) {
		page.NextCursor = strings.Repeat("c", 64)
	}
	return page, nil
}

func (session *catalogIndexerSession) Finalize(context.Context) (provider.CatalogReadProof, error) {
	if session.offset != len(session.records) {
		return provider.CatalogReadProof{}, provider.ErrCatalogSessionIncomplete
	}
	if session.mutate != nil {
		session.mutate()
		session.mutate = nil
	}
	return session.proof, nil
}

func (session *catalogIndexerSession) Close() error { session.closed = true; return nil }

type catalogIndexerFixture struct {
	db       *gorm.DB
	now      time.Time
	point    model.RecoveryPoint
	manifest *model.RecoveryPointManifest
	lease    *backupasset.LeaseService
	keys     catalogIndexerKeySource
}

func newCatalogIndexerFixture(t *testing.T, mutable bool, expectedEntries int64) catalogIndexerFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_CATALOG_INDEXER_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "catalog.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	repository := model.BackupRepository{
		ID: strings.Repeat("1", 32), ProviderKind: string(backupasset.ProviderRsync), DisplayName: "catalog-fixture",
		VersionMode: string(backupasset.VersionFullCopyTree), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
		CreatedAt: now, UpdatedAt: now,
	}
	semantics := backupasset.PointXirangManifest
	state := backupasset.RecoveryPointCommitted
	if mutable {
		repository.VersionMode = string(backupasset.VersionMutableHead)
		repository.ImmutabilityLevel = string(backupasset.ImmutabilityMutable)
		semantics = backupasset.PointMutableHead
		state = backupasset.RecoveryPointObserved
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	observedAt := now.Add(-time.Minute)
	point := model.RecoveryPoint{
		ID: strings.Repeat("2", 32), RepositoryID: repository.ID, Semantics: string(semantics), State: string(state),
		LineageJSON: "{}", SourceFingerprint: strings.Repeat("a", 64), ManifestDigestAlgorithm: "sha256",
		ManifestDigest: strings.Repeat("b", 64), EntryCount: expectedEntries, ConsistencyJSON: "{}", FidelityJSON: "{}",
		CapabilityRevision: repository.CapabilityRevision, CapabilitiesJSON: "{}", ImmutabilityLevel: repository.ImmutabilityLevel,
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		ObservedAt: &observedAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	var manifest *model.RecoveryPointManifest
	if !mutable {
		row := model.RecoveryPointManifest{
			ID: strings.Repeat("3", 32), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256",
			Digest: point.ManifestDigest, Generator: "fixture", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
			EntryCount: expectedEntries, FidelityJSON: "{}", IsActive: true, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		manifest = &row
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalogIndexerFixture{
		db: db, now: now, point: point, manifest: manifest, lease: lease,
		keys: catalogIndexerKeySource{key: []byte("FAKE_CATALOG_ENTRY_IDENTITY_KEY_32_BYTES")},
	}
}

func (fixture catalogIndexerFixture) newIndexer(t *testing.T, factory PointReadFactory) *Indexer {
	t.Helper()
	indexer, err := NewIndexer(IndexerDependencies{
		DB: fixture.db, Factory: factory, Lease: fixture.lease, IdentityKeys: fixture.keys,
		Now: func() time.Time { return fixture.now }, Config: IndexerConfig{BatchSize: 2, BuildTimeout: 30 * time.Minute, MaxEntries: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	return indexer
}

func (fixture catalogIndexerFixture) factory(records ...provider.CatalogRecord) *catalogIndexerFactory {
	mode := provider.CatalogProofMutableObservation
	manifest := provider.CatalogManifestProof{}
	if fixture.manifest != nil {
		mode = provider.CatalogProofPublicationManifest
		manifest = provider.CatalogManifestProof{
			ManifestID: fixture.manifest.ID, Revision: fixture.manifest.Revision,
			DigestAlgorithm: fixture.manifest.DigestAlgorithm, Digest: fixture.manifest.Digest,
			EntryCount: fixture.manifest.EntryCount, Completeness: backupasset.ManifestCompleteness(fixture.manifest.Completeness),
			SourceRevision: fixture.point.SourceFingerprint,
		}
	}
	return &catalogIndexerFactory{
		provider: backupasset.ProviderRsync, source: fixture.point.SourceFingerprint, mode: mode,
		manifest: manifest, records: append([]provider.CatalogRecord(nil), records...),
	}
}

func sealedCatalogRecordForTest(t *testing.T, path, parent, name string, entryType backupasset.CatalogEntryType, size int64) provider.CatalogRecord {
	t.Helper()
	sealed, err := secure.EncryptIfNeeded(`{"version":1,"native":"FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY"}`)
	if err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	return provider.CatalogRecord{
		NormalizedPath: path, ParentNormalizedPath: parent, Name: name, Type: entryType, Size: size,
		ModifiedAt: &modified, Fingerprint: strings.Repeat("d", 64), FingerprintStrength: string(FingerprintStrong),
		SealedProviderLocator: sealed,
	}
}

func TestCatalogIndexerCompletesZeroEntryAndAtomicallySupersedes(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, false, 0)
	factory := fixture.factory()
	indexer := fixture.newIndexer(t, factory)
	first, err := indexer.Build(context.Background(), BuildRequest{
		RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID, CorrelationID: "corr-zero-first",
	})
	if err != nil || first.State != string(GenerationComplete) || first.WrittenEntryCount != 0 || !first.IsActive {
		t.Fatalf("first generation=%+v err=%v", first, err)
	}
	second, err := indexer.Build(context.Background(), BuildRequest{
		RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID, CorrelationID: "corr-zero-second",
	})
	if err != nil || second.State != string(GenerationComplete) || !second.IsActive || second.Generation != first.Generation+1 {
		t.Fatalf("second generation=%+v err=%v", second, err)
	}
	var generations []model.CatalogGeneration
	if err := fixture.db.Order("generation ASC").Find(&generations).Error; err != nil {
		t.Fatal(err)
	}
	if len(generations) != 2 || generations[0].State != string(GenerationSuperseded) || generations[0].IsActive ||
		generations[1].State != string(GenerationComplete) || !generations[1].IsActive {
		t.Fatalf("generations=%+v", generations)
	}
	var active int64
	if err := fixture.db.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ? AND is_active = ?", fixture.point.ID, true).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("active generations=%d err=%v", active, err)
	}
}

func TestCatalogIndexerProofMismatchPreservesOldActiveGeneration(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, false, 2)
	factory := fixture.factory(
		sealedCatalogRecordForTest(t, "docs", "", "docs", backupasset.CatalogEntryDirectory, 0),
		sealedCatalogRecordForTest(t, "docs/report.txt", "docs", "report.txt", backupasset.CatalogEntryFile, 7),
	)
	indexer := fixture.newIndexer(t, factory)
	first, err := indexer.Build(context.Background(), BuildRequest{RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID})
	if err != nil {
		t.Fatal(err)
	}
	factory.proofMut = func(proof *provider.CatalogReadProof) { proof.Catalog.Digest = strings.Repeat("e", 64) }
	if _, err := indexer.Build(context.Background(), BuildRequest{RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID}); !errors.Is(err, ErrCatalogProofMismatch) {
		t.Fatalf("proof mismatch error=%v", err)
	}
	var active model.CatalogGeneration
	if err := fixture.db.Where("recovery_point_id = ? AND is_active = ?", fixture.point.ID, true).First(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID || active.State != string(GenerationComplete) {
		t.Fatalf("active after failed rebuild=%+v want=%s", active, first.ID)
	}
	var failed int64
	if err := fixture.db.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ? AND state = ?", fixture.point.ID, GenerationFailed).Count(&failed).Error; err != nil || failed != 1 {
		t.Fatalf("failed generations=%d err=%v", failed, err)
	}
}

func TestCatalogIndexerRejectsEveryFrozenProofDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*provider.CatalogReadProof)
		want   error
	}{
		{"manifest id", func(proof *provider.CatalogReadProof) { proof.Manifest.ManifestID = strings.Repeat("e", 32) }, ErrCatalogProofMismatch},
		{"manifest revision", func(proof *provider.CatalogReadProof) { proof.Manifest.Revision++ }, ErrCatalogProofMismatch},
		{"manifest digest", func(proof *provider.CatalogReadProof) { proof.Manifest.Digest = strings.Repeat("e", 64) }, ErrCatalogProofMismatch},
		{"manifest count", func(proof *provider.CatalogReadProof) { proof.Manifest.EntryCount++ }, ErrCatalogProofMismatch},
		{"manifest completeness", func(proof *provider.CatalogReadProof) { proof.Manifest.Completeness = backupasset.ManifestPartial }, ErrCatalogProofMismatch},
		{"manifest source", func(proof *provider.CatalogReadProof) { proof.Manifest.SourceRevision = strings.Repeat("e", 64) }, ErrCatalogProofMismatch},
		{"session source", func(proof *provider.CatalogReadProof) { proof.SourceRevision = strings.Repeat("e", 64) }, ErrCatalogSourceChanged},
		{"catalog digest", func(proof *provider.CatalogReadProof) { proof.Catalog.Digest = strings.Repeat("e", 64) }, ErrCatalogProofMismatch},
		{"catalog count", func(proof *provider.CatalogReadProof) { proof.Catalog.EntryCount++ }, ErrCatalogProofMismatch},
		{"catalog incomplete", func(proof *provider.CatalogReadProof) { proof.Catalog.Complete = false }, ErrCatalogProofMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCatalogIndexerFixture(t, false, 0)
			factory := fixture.factory()
			factory.proofMut = test.mutate
			_, err := fixture.newIndexer(t, factory).Build(context.Background(), BuildRequest{
				RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Build error=%v, want %v", err, test.want)
			}
			var active int64
			if err := fixture.db.Model(&model.CatalogGeneration{}).
				Where("recovery_point_id = ? AND is_active = ?", fixture.point.ID, true).Count(&active).Error; err != nil {
				t.Fatal(err)
			}
			if active != 0 {
				t.Fatalf("proof drift exposed %d active generations", active)
			}
		})
	}
}

func TestCatalogIndexerClassifiesBoundedInterruptionAsPartial(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	factory := fixture.factory()
	factory.listErr = provider.ErrCatalogSessionIncomplete

	_, err := fixture.newIndexer(t, factory).Build(context.Background(), BuildRequest{
		RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID,
	})
	if !errors.Is(err, provider.ErrCatalogSessionIncomplete) {
		t.Fatalf("Build error=%v, want provider session incomplete", err)
	}
	var generation model.CatalogGeneration
	if err := fixture.db.Where("recovery_point_id = ?", fixture.point.ID).First(&generation).Error; err != nil {
		t.Fatal(err)
	}
	if generation.State != string(GenerationPartial) || generation.IsActive || generation.ErrorCode != "catalog_build_incomplete" {
		t.Fatalf("bounded interruption generation=%+v", generation)
	}
}

func TestCatalogIndexerRejectsMutableSourceRaceAndLostFence(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(catalogIndexerFixture)
		want      error
		wantState GenerationState
	}{
		{"source race", func(f catalogIndexerFixture) {
			_ = f.db.Model(&model.RecoveryPoint{}).Where("id = ?", f.point.ID).Updates(map[string]any{
				"source_fingerprint": strings.Repeat("f", 64), "observed_at": f.now.Add(time.Second),
			}).Error
		}, ErrCatalogSourceChanged, GenerationFailed},
		{"fence loss", func(f catalogIndexerFixture) {
			_ = f.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", f.point.ID, backupasset.LeaseActive).
				Update("fence_token", strings.Repeat("f", 64)).Error
		}, backupasset.ErrLeaseFenceLost, GenerationBuilding},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCatalogIndexerFixture(t, true, 1)
			factory := fixture.factory(
				sealedCatalogRecordForTest(t, "file.txt", "", "file.txt", backupasset.CatalogEntryFile, 1),
			)
			factory.mutate = func() { test.mutate(fixture) }
			_, err := fixture.newIndexer(t, factory).Build(context.Background(), BuildRequest{
				RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Build error=%v want=%v", err, test.want)
			}
			var active int64
			if err := fixture.db.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ? AND is_active = ?", fixture.point.ID, true).Count(&active).Error; err != nil || active != 0 {
				t.Fatalf("active generations=%d err=%v", active, err)
			}
			var generation model.CatalogGeneration
			if err := fixture.db.Where("recovery_point_id = ?", fixture.point.ID).First(&generation).Error; err != nil || generation.State != string(test.wantState) {
				t.Fatalf("generation state=%q err=%v want=%q", generation.State, err, test.wantState)
			}
		})
	}
}

func TestCatalogIndexerRetirementDeactivatesProjection(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	indexer := fixture.newIndexer(t, fixture.factory())
	generation, err := indexer.Build(context.Background(), BuildRequest{RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.point.ID).Updates(map[string]any{
		"state": backupasset.RecoveryPointRetired, "retired_at": fixture.now, "retirement_reason": backupasset.RetirementWithdrawn,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := indexer.RetireProjection(context.Background(), fixture.point.ID); err != nil {
		t.Fatal(err)
	}
	var stored model.CatalogGeneration
	if err := fixture.db.First(&stored, "id = ?", generation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.IsActive || stored.State != string(GenerationSuperseded) {
		t.Fatalf("retired generation=%+v", stored)
	}
}

func TestCatalogIndexerRetirementProjectionSharesCallerTransaction(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	indexer := fixture.newIndexer(t, fixture.factory())
	generation, err := indexer.Build(context.Background(), BuildRequest{
		RepositoryID: strings.Repeat("1", 32), RecoveryPointID: fixture.point.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("FAKE_RETIREMENT_ROLLBACK_FOR_TEST_ONLY")
	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		if err := indexer.DeactivatePointProjectionTx(context.Background(), tx, fixture.point.ID); err != nil {
			return err
		}
		if err := tx.Model(&model.RecoveryPoint{}).Where("id = ? AND state = ?", fixture.point.ID, backupasset.RecoveryPointObserved).
			Updates(map[string]any{
				"state": backupasset.RecoveryPointRetired, "retired_at": fixture.now,
				"retirement_reason": backupasset.RetirementWithdrawn,
			}).Error; err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("retirement rollback error=%v", err)
	}
	var stored model.CatalogGeneration
	if err := fixture.db.First(&stored, "id = ?", generation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.IsActive || stored.State != string(GenerationComplete) {
		t.Fatalf("rolled-back retirement changed generation=%+v", stored)
	}

	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		if err := indexer.DeactivatePointProjectionTx(context.Background(), tx, fixture.point.ID); err != nil {
			return err
		}
		result := tx.Model(&model.RecoveryPoint{}).Where("id = ? AND state = ?", fixture.point.ID, backupasset.RecoveryPointObserved).
			Updates(map[string]any{
				"state": backupasset.RecoveryPointRetired, "retired_at": fixture.now,
				"retirement_reason": backupasset.RetirementWithdrawn,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("retirement point CAS lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&stored, "id = ?", generation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.IsActive || stored.State != string(GenerationSuperseded) {
		t.Fatalf("committed retirement left projection=%+v", stored)
	}
}

func TestCatalogIndexerReconcilesOnlyUnfencedAbandonedBuilds(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	indexer := fixture.newIndexer(t, fixture.factory())
	old := fixture.now.Add(-2 * time.Hour)
	recent := fixture.now.Add(-10 * time.Minute)
	rows := []model.CatalogGeneration{
		{ID: strings.Repeat("4", 32), RecoveryPointID: fixture.point.ID, Generation: 1, State: string(GenerationBuilding), StartedAt: old, CreatedAt: old, UpdatedAt: old},
		{ID: strings.Repeat("5", 32), RecoveryPointID: fixture.point.ID, Generation: 2, State: string(GenerationBuilding), StartedAt: recent, CreatedAt: recent, UpdatedAt: recent},
	}
	if err := fixture.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	count, err := indexer.ReconcileAbandoned(context.Background(), time.Hour, 10)
	if err != nil || count != 1 {
		t.Fatalf("reconcile abandoned count=%d err=%v", count, err)
	}
	var oldStored, recentStored model.CatalogGeneration
	if err := fixture.db.First(&oldStored, "id = ?", rows[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&recentStored, "id = ?", rows[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if oldStored.State != string(GenerationFailed) || oldStored.ErrorCode != "catalog_build_abandoned" || oldStored.FinishedAt == nil ||
		recentStored.State != string(GenerationBuilding) {
		t.Fatalf("reconciled old=%+v recent=%+v", oldStored, recentStored)
	}

	leased := model.CatalogGeneration{
		ID: strings.Repeat("6", 32), RecoveryPointID: fixture.point.ID, Generation: 3,
		State: string(GenerationBuilding), StartedAt: old, CreatedAt: old, UpdatedAt: old,
	}
	if err := fixture.db.Create(&leased).Error; err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.lease.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: fixture.point.ID, HolderType: backupasset.LeaseHolderCatalogBuild,
		OwnerID: catalogBuildOwnerPrefix + fixture.point.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixture.lease.Release(context.Background(), lease.Fence) }()
	count, err = indexer.ReconcileAbandoned(context.Background(), time.Hour, 10)
	if err != nil || count != 0 {
		t.Fatalf("reconcile leased count=%d err=%v", count, err)
	}
	if err := fixture.db.First(&leased, "id = ?", leased.ID).Error; err != nil || leased.State != string(GenerationBuilding) {
		t.Fatalf("leased generation=%+v err=%v", leased, err)
	}
}

func TestCatalogIndexerRenewsAndRevokesFenceBeforeCancellationIgnoringSessionReturns(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	lease := &catalogIndexerLeaseSpy{
		CatalogLease: fixture.lease, renewer: fixture.lease,
		renewed: make(chan struct{}, 1), released: make(chan struct{}, 2),
	}
	session := &catalogCancellationIgnoringSession{
		source: fixture.point.SourceFingerprint, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	indexer, err := NewIndexer(IndexerDependencies{
		DB: fixture.db, Factory: catalogBlockingFactory{session: session}, Lease: lease, IdentityKeys: fixture.keys,
		Now:    func() time.Time { return fixture.now },
		Config: IndexerConfig{BatchSize: 2, BuildTimeout: 30 * time.Minute, MaxEntries: 100, HeartbeatInterval: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	buildDone := make(chan error, 1)
	go func() {
		_, buildErr := indexer.Build(context.Background(), BuildRequest{RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID})
		buildDone <- buildErr
	}()
	select {
	case <-session.entered:
	case <-time.After(time.Second):
		t.Fatal("Catalog session did not enter enumeration")
	}
	select {
	case <-lease.renewed:
	case <-time.After(time.Second):
		t.Fatal("Catalog fence was not heartbeated")
	}
	if err := indexer.RevokeActiveBuilds(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.released:
	case <-time.After(time.Second):
		t.Fatal("active Catalog fence was not durably released")
	}
	select {
	case <-session.canceled:
	case <-time.After(time.Second):
		t.Fatal("active Catalog session was not canceled")
	}
	select {
	case err := <-buildDone:
		t.Fatalf("Build returned before cancellation-ignoring session was joined: %v", err)
	default:
	}
	close(session.release)
	if err := <-buildDone; !errors.Is(err, context.Canceled) && !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("revoked Build error=%v", err)
	}
}

func TestCatalogIndexerRenewalLossCancelsBuild(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	lease := &catalogIndexerLeaseSpy{
		CatalogLease: fixture.lease, renewer: catalogFailingLeaseRenewer{},
		renewed: make(chan struct{}, 1), released: make(chan struct{}, 2),
	}
	session := &catalogCancellationIgnoringSession{
		source: fixture.point.SourceFingerprint, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	indexer, err := NewIndexer(IndexerDependencies{
		DB: fixture.db, Factory: catalogBlockingFactory{session: session}, Lease: lease, IdentityKeys: fixture.keys,
		Now:    func() time.Time { return fixture.now },
		Config: IndexerConfig{BatchSize: 2, BuildTimeout: 30 * time.Minute, MaxEntries: 100, HeartbeatInterval: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, buildErr := indexer.Build(context.Background(), BuildRequest{RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID})
		done <- buildErr
	}()
	select {
	case <-session.canceled:
	case <-time.After(time.Second):
		t.Fatal("lease renewal loss did not cancel Catalog enumeration")
	}
	close(session.release)
	if err := <-done; !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("renewal-loss Build error=%v", err)
	}
}

func TestCatalogIndexerBuildJoinsHeartbeatBeforeReturning(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	innerFactory := fixture.factory()
	delayedFactory := &catalogDelayedFactory{
		inner: innerFactory, entered: make(chan struct{}), release: make(chan struct{}),
	}
	blockingRenewer := &catalogBlockingLeaseRenewer{
		inner: fixture.lease, entered: make(chan struct{}), release: make(chan struct{}),
	}
	lease := &catalogIndexerLeaseSpy{
		CatalogLease: fixture.lease, renewer: blockingRenewer,
		renewed: make(chan struct{}, 1), released: make(chan struct{}, 2),
	}
	indexer, err := NewIndexer(IndexerDependencies{
		DB: fixture.db, Factory: delayedFactory, Lease: lease, IdentityKeys: fixture.keys,
		Now:    func() time.Time { return fixture.now },
		Config: IndexerConfig{BatchSize: 2, BuildTimeout: 30 * time.Minute, MaxEntries: 100, HeartbeatInterval: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	buildDone := make(chan error, 1)
	go func() {
		_, buildErr := indexer.Build(context.Background(), BuildRequest{
			RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
		})
		buildDone <- buildErr
	}()
	select {
	case <-delayedFactory.entered:
	case <-time.After(time.Second):
		t.Fatal("Catalog session did not enter enumeration")
	}
	select {
	case <-blockingRenewer.entered:
	case <-time.After(time.Second):
		t.Fatal("Catalog heartbeat did not enter renewal")
	}
	close(delayedFactory.release)
	var buildErr error
	returnedBeforeHeartbeat := false
	select {
	case buildErr = <-buildDone:
		returnedBeforeHeartbeat = true
	case <-time.After(50 * time.Millisecond):
	}
	close(blockingRenewer.release)
	if !returnedBeforeHeartbeat {
		select {
		case buildErr = <-buildDone:
		case <-time.After(time.Second):
			t.Fatal("Catalog Build did not return after heartbeat renewal completed")
		}
	}
	if returnedBeforeHeartbeat {
		t.Fatal("Catalog Build returned while its heartbeat goroutine was still renewing")
	}
	if buildErr != nil {
		t.Fatalf("Catalog Build error=%v", buildErr)
	}
}

func TestCatalogIndexerCandidatesHonorActiveProjectionAndDurableBackoff(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	indexer := fixture.newIndexer(t, fixture.factory())
	config := backupasset.CatalogConfig{
		Enabled: true, BatchSize: 2, BuildTimeout: 30 * time.Minute, ReconcileInterval: 15 * time.Minute,
		MaxConcurrency: 2, MaxEntries: 100,
		Lease: backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour},
	}
	candidates, err := indexer.ListCandidates(context.Background(), 20, fixture.now, config)
	if err != nil || len(candidates) != 1 || candidates[0].RecoveryPointID != fixture.point.ID {
		t.Fatalf("missing-Catalog candidates=%+v err=%v", candidates, err)
	}

	finished := fixture.now
	failed := model.CatalogGeneration{
		ID: strings.Repeat("8", 32), RecoveryPointID: fixture.point.ID, Generation: 1, State: string(GenerationFailed),
		SourceFingerprint: fixture.point.SourceFingerprint, ErrorCode: "catalog_provider_unavailable",
		StartedAt: fixture.now.Add(-time.Minute), FinishedAt: &finished, CreatedAt: fixture.now.Add(-time.Minute), UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err = indexer.ListCandidates(context.Background(), 20, fixture.now, config)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("backoff-now candidates=%+v err=%v", candidates, err)
	}
	delay := RetryDelay(config, 1, fixture.point.ID, failed.ID)
	candidates, err = indexer.ListCandidates(context.Background(), 20, fixture.now.Add(delay+time.Second), config)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("backoff-expired candidates=%+v err=%v delay=%s", candidates, err, delay)
	}

	active := model.CatalogGeneration{
		ID: strings.Repeat("9", 32), RecoveryPointID: fixture.point.ID, Generation: 2, State: string(GenerationComplete), IsActive: true,
		SourceFingerprint: fixture.point.SourceFingerprint, WrittenDigest: strings.Repeat("a", 64),
		StartedAt: fixture.now, FinishedAt: &finished, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	freshNow := fixture.point.ObservedAt.Add(2*config.ReconcileInterval - time.Second)
	candidates, err = indexer.ListCandidates(context.Background(), 20, freshNow, config)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("fresh active mutable candidates=%+v err=%v", candidates, err)
	}
	staleNow := fixture.point.ObservedAt.Add(2 * config.ReconcileInterval)
	candidates, err = indexer.ListCandidates(context.Background(), 20, staleNow, config)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("stale active mutable candidates=%+v err=%v", candidates, err)
	}
}

type catalogLeaseRenewer interface {
	Renew(context.Context, backupasset.LeaseFence) (backupasset.Lease, error)
}

type catalogIndexerLeaseSpy struct {
	CatalogLease
	renewer  catalogLeaseRenewer
	renewed  chan struct{}
	released chan struct{}
}

func (lease *catalogIndexerLeaseSpy) Renew(ctx context.Context, fence backupasset.LeaseFence) (backupasset.Lease, error) {
	select {
	case lease.renewed <- struct{}{}:
	default:
	}
	return lease.renewer.Renew(ctx, fence)
}

func (lease *catalogIndexerLeaseSpy) Release(ctx context.Context, fence backupasset.LeaseFence) error {
	err := lease.CatalogLease.Release(ctx, fence)
	select {
	case lease.released <- struct{}{}:
	default:
	}
	return err
}

type catalogFailingLeaseRenewer struct{}

func (catalogFailingLeaseRenewer) Renew(context.Context, backupasset.LeaseFence) (backupasset.Lease, error) {
	return backupasset.Lease{}, backupasset.ErrLeaseFenceLost
}

type catalogBlockingLeaseRenewer struct {
	inner   catalogLeaseRenewer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (renewer *catalogBlockingLeaseRenewer) Renew(ctx context.Context, fence backupasset.LeaseFence) (backupasset.Lease, error) {
	renewer.once.Do(func() { close(renewer.entered) })
	select {
	case <-renewer.release:
		return renewer.inner.Renew(ctx, fence)
	case <-ctx.Done():
		return backupasset.Lease{}, ctx.Err()
	}
}

type catalogDelayedFactory struct {
	inner   PointReadFactory
	entered chan struct{}
	release chan struct{}
}

func (factory *catalogDelayedFactory) OpenCatalogRead(ctx context.Context, request PointReadRequest) (provider.CatalogReadSession, error) {
	inner, err := factory.inner.OpenCatalogRead(ctx, request)
	if err != nil {
		return nil, err
	}
	return &catalogDelayedSession{inner: inner, entered: factory.entered, release: factory.release}, nil
}

type catalogDelayedSession struct {
	inner   provider.CatalogReadSession
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (session *catalogDelayedSession) SourceRevision() string { return session.inner.SourceRevision() }

func (session *catalogDelayedSession) ListCanonical(ctx context.Context, request provider.PageRequest) (provider.CatalogRecordPage, error) {
	session.once.Do(func() { close(session.entered) })
	select {
	case <-session.release:
		return session.inner.ListCanonical(ctx, request)
	case <-ctx.Done():
		return provider.CatalogRecordPage{}, ctx.Err()
	}
}

func (session *catalogDelayedSession) Finalize(ctx context.Context) (provider.CatalogReadProof, error) {
	return session.inner.Finalize(ctx)
}

func (session *catalogDelayedSession) Close() error { return session.inner.Close() }

type catalogBlockingFactory struct{ session provider.CatalogReadSession }

func (factory catalogBlockingFactory) OpenCatalogRead(context.Context, PointReadRequest) (provider.CatalogReadSession, error) {
	return factory.session, nil
}

type catalogCancellationIgnoringSession struct {
	source   string
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (session *catalogCancellationIgnoringSession) SourceRevision() string { return session.source }
func (session *catalogCancellationIgnoringSession) ListCanonical(ctx context.Context, _ provider.PageRequest) (provider.CatalogRecordPage, error) {
	session.once.Do(func() { close(session.entered) })
	<-ctx.Done()
	close(session.canceled)
	<-session.release
	return provider.CatalogRecordPage{}, ctx.Err()
}
func (*catalogCancellationIgnoringSession) Finalize(context.Context) (provider.CatalogReadProof, error) {
	return provider.CatalogReadProof{}, provider.ErrCatalogSessionIncomplete
}
func (*catalogCancellationIgnoringSession) Close() error { return nil }
