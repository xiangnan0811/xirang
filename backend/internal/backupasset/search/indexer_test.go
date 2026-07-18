package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var indexerTestDBSequence atomic.Uint64

func TestIndexerZeroDocumentProjectionActivatesAtomically(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, catalogID := harness.seedCatalog(t, nil)
	generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID, CorrelationID: "zero"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if generation.State != string(SearchGenerationComplete) || !generation.IsActive || generation.CatalogGenerationID != catalogID || generation.WrittenDocumentCount != 0 {
		t.Fatalf("invalid zero-document generation: %+v", generation)
	}
	var documents int64
	if err := harness.db.Model(&model.BackupAssetSearchDocument{}).Where("search_generation_id = ?", generation.ID).Count(&documents).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if documents != 0 {
		t.Fatalf("zero Catalog produced %d search documents", documents)
	}
}

func TestIndexerProjectionStoresOnlyHMACsAndMapsLegacySecurityUnknown(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("d", 64), NormalizedPath: "Docs/年度报告.TXT", Name: "年度报告.TXT",
		EntryType: "file", SecurityState: "", ModifiedAt: timePointer(harness.now.Add(-time.Hour)),
	}})
	generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID, CorrelationID: "projection"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var document model.BackupAssetSearchDocument
	if err := harness.db.Where("search_generation_id = ?", generation.ID).First(&document).Error; err != nil {
		t.Fatalf("load document: %v", err)
	}
	if document.Sensitivity != string(SensitivityUnknown) || document.PathSortKey == "" || document.NameSortKey == "" ||
		document.LineageToken == "" || document.PathGroupToken == "" {
		t.Fatalf("invalid projected document: %+v", document)
	}
	var postings []model.BackupAssetSearchPosting
	if err := harness.db.Where("search_generation_id = ?", generation.ID).Find(&postings).Error; err != nil {
		t.Fatalf("load postings: %v", err)
	}
	if len(postings) == 0 {
		t.Fatal("metadata projection produced no postings")
	}
	for _, posting := range postings {
		if len(posting.TokenHMAC) != 64 || strings.Contains(posting.TokenHMAC, "报告") || strings.Contains(posting.TokenHMAC, "txt") || posting.TermFrequency <= 0 {
			t.Fatalf("posting leaked plaintext or invalid frequency: %+v", posting)
		}
	}
	var fields []model.BackupAssetSearchDocumentField
	if err := harness.db.Where("search_generation_id = ?", generation.ID).Find(&fields).Error; err != nil {
		t.Fatalf("load fields: %v", err)
	}
	states := make(map[string]string, len(fields))
	for _, field := range fields {
		states[field.Field] = field.State
	}
	for _, field := range []string{"name", "path", "extension", "type", "modified_time"} {
		if states[field] != string(FieldCoverageComplete) {
			t.Fatalf("metadata field %s state=%q, want complete", field, states[field])
		}
	}
	for _, field := range []string{"content", "ocr"} {
		if states[field] != string(FieldCoverageUnavailable) {
			t.Fatalf("future field %s state=%q, want unavailable", field, states[field])
		}
	}
}

func TestIndexerInvalidSecurityFailsAndPreservesOldActiveProjection(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("e", 64), NormalizedPath: "bad.txt", Name: "bad.txt",
		EntryType: "file", SecurityState: "future_security",
	}})
	old := harness.seedActiveSearch(t, pointID, catalogID, 1)
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID, CorrelationID: "bad-security"}); !errors.Is(err, ErrInvalidSecurityState) {
		t.Fatalf("Build got %v, want ErrInvalidSecurityState", err)
	}
	var persisted model.BackupAssetSearchGeneration
	if err := harness.db.First(&persisted, "id = ?", old.ID).Error; err != nil {
		t.Fatalf("load old active: %v", err)
	}
	if !persisted.IsActive || persisted.State != string(SearchGenerationComplete) {
		t.Fatalf("failed build replaced old active projection: %+v", persisted)
	}
	var failed int64
	if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
		Where("recovery_point_id = ? AND state = ?", pointID, SearchGenerationFailed).Count(&failed).Error; err != nil {
		t.Fatalf("count failed generation: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed generation count=%d, want 1", failed)
	}
}

func TestIndexerActivationRejectsFenceAndSourceDrift(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		validate func(*gorm.DB, backupasset.LeaseFence) error
		want     error
	}{
		{name: "fence", validate: func(*gorm.DB, backupasset.LeaseFence) error { return backupasset.ErrLeaseFenceLost }, want: backupasset.ErrLeaseFenceLost},
		{name: "source", validate: func(tx *gorm.DB, fence backupasset.LeaseFence) error {
			return tx.Model(&model.RecoveryPoint{}).Where("id = ?", fence.RecoveryPointID).Update("source_fingerprint", "drifted").Error
		}, want: ErrSearchSourceChanged},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, harness := newIndexerTestHarness(t)
			pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
				EntryID: strings.Repeat("f", 64), NormalizedPath: "safe.txt", Name: "safe.txt", EntryType: "file",
			}})
			indexer, err := NewIndexer(IndexerDependencies{
				DB: harness.db, Lease: &indexerLeaseFake{now: harness.now, validate: testCase.validate},
				Keys: harness.ring, Now: func() time.Time { return harness.now }, Config: standardIndexerConfig(),
			})
			if err != nil {
				t.Fatalf("NewIndexer: %v", err)
			}
			if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); !errors.Is(err, testCase.want) {
				t.Fatalf("Build got %v, want %v", err, testCase.want)
			}
			var active int64
			if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).Where("recovery_point_id = ? AND is_active = ?", pointID, true).Count(&active).Error; err != nil {
				t.Fatal(err)
			}
			if active != 0 {
				t.Fatalf("drifted activation published %d active generations", active)
			}
		})
	}
}

func TestIndexerCandidateAndAbandonedProjectionReconciliation(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, catalogID := harness.seedCatalog(t, nil)
	candidates, err := indexer.ListCandidates(context.Background(), 10)
	if err != nil || len(candidates) != 1 || candidates[0].RecoveryPointID != pointID || candidates[0].CatalogGenerationID != catalogID {
		t.Fatalf("ListCandidates=%+v err=%v", candidates, err)
	}
	stale := harness.seedBuildingSearch(t, pointID, catalogID, harness.now.Add(-time.Hour))
	count, err := indexer.ReconcileAbandoned(context.Background(), harness.now.Add(-30*time.Minute), 10)
	if err != nil || count != 1 {
		t.Fatalf("ReconcileAbandoned count=%d err=%v", count, err)
	}
	var row model.BackupAssetSearchGeneration
	if err := harness.db.First(&row, "id = ?", stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != string(SearchGenerationFailed) || row.ErrorCode != string(SearchErrorBuildAbandoned) {
		t.Fatalf("abandoned generation=%+v", row)
	}
}

type indexerTestHarness struct {
	db   *gorm.DB
	ring *backupasset.Keyring
	now  time.Time
}

func newIndexerTestHarness(t *testing.T) (*Indexer, *indexerTestHarness) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SEARCH_INDEXER_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	now := time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC)
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"), indexerTestDBSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return now }, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open indexer DB: %v", err)
	}
	models := []any{
		&model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.WrappedDomainKey{}, &model.RecoveryPointLease{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchPosting{}, &model.BackupAssetSearchDocumentField{},
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate indexer models: %v", err)
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX idx_test_catalog_active ON catalog_generations(recovery_point_id) WHERE is_active = 1`,
		`CREATE UNIQUE INDEX idx_test_search_active ON backup_asset_search_generations(recovery_point_id) WHERE is_active = 1`,
		`CREATE UNIQUE INDEX idx_test_key_active ON wrapped_domain_keys(domain) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX idx_test_lease_active ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create indexer test index: %v", err)
		}
	}
	ring := backupasset.NewKeyring(db, func() time.Time { return now })
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatalf("ensure search token: %v", err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour})
	if err != nil {
		t.Fatalf("new lease service: %v", err)
	}
	harness := &indexerTestHarness{db: db, ring: ring, now: now}
	indexer, err := NewIndexer(IndexerDependencies{DB: db, Lease: lease, Keys: ring, Now: func() time.Time { return now }, Config: standardIndexerConfig()})
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}
	return indexer, harness
}

func standardIndexerConfig() IndexerConfig {
	return IndexerConfig{BatchSize: 50, BuildTimeout: time.Minute, MaxDocuments: 1000}
}

func (harness *indexerTestHarness) seedCatalog(t *testing.T, entries []model.CatalogEntry) (string, string) {
	t.Helper()
	pointID := fmt.Sprintf("%032x", time.Now().UnixNano()&0xfffffff)
	catalogID := fmt.Sprintf("%032x", (time.Now().UnixNano()+1)&0xfffffff)
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: strings.Repeat("a", 32), ProducingTaskID: uintPointer(11), ProducingTaskRunID: uintPointer(12),
		LineageJSON: validIndexerLineage(t, harness.now), Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		SourceFingerprint: "source-" + pointID, CreatedAt: harness.now, UpdatedAt: harness.now,
	}
	if err := harness.db.Create(&point).Error; err != nil {
		t.Fatalf("seed point: %v", err)
	}
	generation := model.CatalogGeneration{
		ID: catalogID, RecoveryPointID: pointID, Generation: 1, State: "complete", IsActive: true,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: int64(len(entries)), WrittenEntryCount: int64(len(entries)),
		StartedAt: harness.now, FinishedAt: timePointer(harness.now), CreatedAt: harness.now, UpdatedAt: harness.now,
	}
	if err := harness.db.Create(&generation).Error; err != nil {
		t.Fatalf("seed Catalog generation: %v", err)
	}
	for index := range entries {
		entries[index].GenerationID = catalogID
		entries[index].RecoveryPointID = pointID
		entries[index].CreatedAt = harness.now
		if err := harness.db.Create(&entries[index]).Error; err != nil {
			t.Fatalf("seed Catalog entry: %v", err)
		}
	}
	return pointID, catalogID
}

func (harness *indexerTestHarness) seedActiveSearch(t *testing.T, pointID, catalogID string, generation int) model.BackupAssetSearchGeneration {
	t.Helper()
	key, _ := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	row := model.BackupAssetSearchGeneration{
		ID: fmt.Sprintf("%032x", time.Now().UnixNano()&0xfffffff), RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		Generation: generation, State: string(SearchGenerationComplete), IsActive: true,
		NormalizerVersion: NormalizerVersion, SearchKeyVersion: key.Version, ProjectionRevision: 1,
		LeaseID: strings.Repeat("1", 32), BuildAttemptID: strings.Repeat("2", 32), FenceTokenHash: strings.Repeat("3", 64),
		StartedAt: harness.now, FinishedAt: timePointer(harness.now), CreatedAt: harness.now, UpdatedAt: harness.now,
	}
	if err := harness.db.Create(&row).Error; err != nil {
		t.Fatalf("seed active Search: %v", err)
	}
	return row
}

func (harness *indexerTestHarness) seedBuildingSearch(t *testing.T, pointID, catalogID string, updatedAt time.Time) model.BackupAssetSearchGeneration {
	t.Helper()
	key, _ := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	row := model.BackupAssetSearchGeneration{
		ID: fmt.Sprintf("%032x", (time.Now().UnixNano()+2)&0xfffffff), RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		Generation: 2, State: string(SearchGenerationBuilding), NormalizerVersion: NormalizerVersion,
		SearchKeyVersion: key.Version, ProjectionRevision: 1, LeaseID: strings.Repeat("4", 32),
		BuildAttemptID: strings.Repeat("5", 32), FenceTokenHash: strings.Repeat("6", 64),
		StartedAt: updatedAt, CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	if err := harness.db.Create(&row).Error; err != nil {
		t.Fatalf("seed building Search: %v", err)
	}
	return row
}

type indexerLeaseFake struct {
	now      time.Time
	validate func(*gorm.DB, backupasset.LeaseFence) error
}

func (lease *indexerLeaseFake) Acquire(_ context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("7", 32), RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType,
		OwnerID: request.OwnerID, AttemptID: strings.Repeat("8", 32), FenceToken: strings.Repeat("9", 64),
	}
	return backupasset.Lease{ID: fence.LeaseID, RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType, OwnerID: request.OwnerID, AbsoluteDeadline: lease.now.Add(time.Hour), Fence: fence}, nil
}

func (*indexerLeaseFake) Release(context.Context, backupasset.LeaseFence) error { return nil }
func (*indexerLeaseFake) ReleaseTx(context.Context, *gorm.DB, backupasset.LeaseFence) error {
	return nil
}
func (lease *indexerLeaseFake) ValidateFenceTx(_ context.Context, tx *gorm.DB, fence backupasset.LeaseFence) error {
	return lease.validate(tx, fence)
}

func validIndexerLineage(t *testing.T, now time.Time) string {
	t.Helper()
	value, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: strings.Repeat("a", 32), TaskID: 11, TaskRunID: 12,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationVersionedFullCopy), PointCodecVersion: 1,
		StartedAt: now.Add(-time.Hour), PreparedAt: now.Add(-59 * time.Minute), PointDeadlineAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("encode lineage: %v", err)
	}
	return value
}

func uintPointer(value uint) *uint           { return &value }
func timePointer(value time.Time) *time.Time { return &value }
