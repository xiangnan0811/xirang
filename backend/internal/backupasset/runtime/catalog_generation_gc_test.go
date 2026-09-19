package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type catalogGCFixture struct {
	db               *gorm.DB
	now              time.Time
	repoID           string
	pointID          string
	searchGeneration int
}

func newCatalogGCFixture(t *testing.T) *catalogGCFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "catalog-gc.db")})
	if err != nil {
		t.Fatalf("open Catalog GC database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db.Logger = logger.Default.LogMode(logger.Silent)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.RecoveryPointLease{},
		&model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchPosting{}, &model.BackupAssetSearchDocumentField{},
		&model.BackupAssetDeliveryGrant{}, &model.BackupAssetProcessingJob{},
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedBlobReference{},
		&model.BackupAssetRecoveryPlanItem{},
	); err != nil {
		t.Fatalf("migrate Catalog GC schema: %v", err)
	}
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	fixture := &catalogGCFixture{db: db, now: now, repoID: strings.Repeat("b", 32), pointID: strings.Repeat("a", 32)}
	if err := db.Create(&model.BackupRepository{
		ID: fixture.repoID, ProviderKind: string(backupasset.ProviderRsync), DisplayName: "catalog-gc",
		VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed Catalog GC repository: %v", err)
	}
	if err := db.Create(&model.RecoveryPoint{
		ID: fixture.pointID, RepositoryID: fixture.repoID, Semantics: string(backupasset.PointMutableHead),
		State: string(backupasset.RecoveryPointObserved), SourceFingerprint: strings.Repeat("c", 64),
		CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		ObservedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed Catalog GC point: %v", err)
	}
	return fixture
}

func (fixture *catalogGCFixture) collector() *CatalogGenerationGC {
	gc, err := NewCatalogGenerationGC(fixture.db, func() time.Time { return fixture.now })
	if err != nil {
		panic(err)
	}
	return gc
}

// seedGeneration inserts one Catalog generation. updatedAt orders candidates,
// so tests can choose which generation the collector reaches first.
func (fixture *catalogGCFixture) seedGeneration(
	t *testing.T,
	id string,
	sequence int,
	state catalog.GenerationState,
	active bool,
	updatedAt time.Time,
	entries int,
) model.CatalogGeneration {
	t.Helper()
	finished := updatedAt
	generation := model.CatalogGeneration{
		ID: id, RecoveryPointID: fixture.pointID, Generation: sequence, State: string(state), IsActive: active,
		SourceFingerprint: strings.Repeat("c", 64),
		StartedAt:         updatedAt.Add(-time.Minute), CreatedAt: updatedAt.Add(-time.Minute), UpdatedAt: updatedAt,
	}
	if state != catalog.GenerationBuilding {
		generation.FinishedAt = &finished
	}
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatalf("seed Catalog generation %s: %v", id, err)
	}
	for index := 0; index < entries; index++ {
		fixture.seedEntry(t, id, fmt.Sprintf("%064x", index+1))
	}
	return generation
}

func (fixture *catalogGCFixture) seedEntry(t *testing.T, generationID, entryID string) {
	t.Helper()
	if err := fixture.db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: entryID, RecoveryPointID: fixture.pointID,
		NormalizedPath: "entry-" + entryID[:8], Name: "entry-" + entryID[:8], EntryType: string(backupasset.CatalogEntryFile),
		CreatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("seed Catalog entry: %v", err)
	}
}

// seedSearchProjection creates one Search generation for a Catalog generation
// together with its documents, postings and fields.
func (fixture *catalogGCFixture) seedSearchProjection(
	t *testing.T,
	catalogGenerationID string,
	searchGenerationID string,
	active bool,
	documentCount int,
) {
	t.Helper()
	state := string(search.SearchGenerationSuperseded)
	if active {
		state = string(search.SearchGenerationComplete)
	}
	fixture.searchGeneration++
	if err := fixture.db.Create(&model.BackupAssetSearchGeneration{
		ID: searchGenerationID, RecoveryPointID: fixture.pointID, CatalogGenerationID: catalogGenerationID,
		Generation: fixture.searchGeneration, State: state, IsActive: active, SourceFingerprint: strings.Repeat("c", 64),
		NormalizerVersion: 1, SearchKeyVersion: 1, LeaseID: strings.Repeat("d", 32),
		BuildAttemptID: strings.Repeat("e", 32), FenceTokenHash: strings.Repeat("f", 64),
		StartedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("seed Search generation: %v", err)
	}
	for index := 0; index < documentCount; index++ {
		documentID := fmt.Sprintf("%064x", index+1)
		if err := fixture.db.Create(&model.BackupAssetSearchDocument{
			SearchGenerationID: searchGenerationID, DocumentID: documentID, RecoveryPointID: fixture.pointID,
			CatalogGenerationID: catalogGenerationID, EntryID: documentID, Sensitivity: "non_secret",
			ClassificationRevision: 1, MetadataRevision: 1, EntryType: "file",
			LineageToken: strings.Repeat("1", 64), PathGroupToken: strings.Repeat("2", 64),
			PathSortKey: documentID, NameSortKey: documentID, CreatedAt: fixture.now, UpdatedAt: fixture.now,
		}).Error; err != nil {
			t.Fatalf("seed Search document: %v", err)
		}
		if err := fixture.db.Create(&model.BackupAssetSearchPosting{
			SearchGenerationID: searchGenerationID, DocumentID: documentID, Field: "name",
			TokenKind: "exact", KeyVersion: 1, TokenHMAC: strings.Repeat("3", 64), TermFrequency: 1,
		}).Error; err != nil {
			t.Fatalf("seed Search posting: %v", err)
		}
		if err := fixture.db.Create(&model.BackupAssetSearchDocumentField{
			SearchGenerationID: searchGenerationID, DocumentID: documentID, Field: "content",
			State: "complete", CoverageRevision: 1, ClassificationRevision: 1, PipelineRevision: 1,
			IndexRevision: 1, UpdatedAt: fixture.now,
		}).Error; err != nil {
			t.Fatalf("seed Search field: %v", err)
		}
	}
}

func (fixture *catalogGCFixture) generationIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	if err := fixture.db.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ?", fixture.pointID).
		Order("generation ASC").Pluck("id", &ids).Error; err != nil {
		t.Fatalf("load Catalog generation ids: %v", err)
	}
	return ids
}

func (fixture *catalogGCFixture) catalogEntryCount(t *testing.T, generationID string) int64 {
	t.Helper()
	var count int64
	if err := fixture.db.Model(&model.CatalogEntry{}).Where("generation_id = ?", generationID).Count(&count).Error; err != nil {
		t.Fatalf("count Catalog entries: %v", err)
	}
	return count
}

func (fixture *catalogGCFixture) searchPayloadCounts(t *testing.T, searchGenerationID string) (int64, int64, int64) {
	t.Helper()
	var documents, postings, fields int64
	if err := fixture.db.Model(&model.BackupAssetSearchDocument{}).
		Where("search_generation_id = ?", searchGenerationID).Count(&documents).Error; err != nil {
		t.Fatalf("count Search documents: %v", err)
	}
	if err := fixture.db.Model(&model.BackupAssetSearchPosting{}).
		Where("search_generation_id = ?", searchGenerationID).Count(&postings).Error; err != nil {
		t.Fatalf("count Search postings: %v", err)
	}
	if err := fixture.db.Model(&model.BackupAssetSearchDocumentField{}).
		Where("search_generation_id = ?", searchGenerationID).Count(&fields).Error; err != nil {
		t.Fatalf("count Search fields: %v", err)
	}
	return documents, postings, fields
}

// TestCatalogGenerationGCReclaimsOneGenerationPerScanWithItsSearchPayload covers
// contract case 1 and 5: a point with an active complete generation plus two
// superseded generations loses exactly one generation per scan until only the
// active (and latest) generation remains, and every Search posting of a
// reclaimed generation disappears with it.
func TestCatalogGenerationGCReclaimsOneGenerationPerScanWithItsSearchPayload(t *testing.T) {
	fixture := newCatalogGCFixture(t)
	oldest := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-3*time.Hour), 2)
	middle := fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationSuperseded, false, fixture.now.Add(-2*time.Hour), 0)
	active := fixture.seedGeneration(t, strings.Repeat("3", 32), 3, catalog.GenerationComplete, true, fixture.now.Add(-time.Hour), 1)
	fixture.seedSearchProjection(t, oldest.ID, strings.Repeat("4", 32), false, 3)
	fixture.seedSearchProjection(t, active.ID, strings.Repeat("5", 32), true, 1)
	gc := fixture.collector()

	first, err := gc.Collect(context.Background())
	if err != nil {
		t.Fatalf("first Catalog GC pass: %v", err)
	}
	if first.DeletedGenerations != 1 || first.SkippedRestricted != 0 {
		t.Fatalf("first Catalog GC pass result=%+v", first)
	}
	if ids := fixture.generationIDs(t); len(ids) != 2 {
		t.Fatalf("generations after first pass=%v", ids)
	}
	documents, postings, fields := fixture.searchPayloadCounts(t, strings.Repeat("4", 32))
	if documents != 0 || postings != 0 || fields != 0 {
		t.Fatalf("reclaimed generation payload documents=%d postings=%d fields=%d", documents, postings, fields)
	}
	var searchGenerations int64
	if err := fixture.db.Model(&model.BackupAssetSearchGeneration{}).
		Where("catalog_generation_id = ?", oldest.ID).Count(&searchGenerations).Error; err != nil {
		t.Fatalf("count reclaimed Search generations: %v", err)
	}
	if searchGenerations != 0 {
		t.Fatalf("reclaimed Catalog generation still owns %d Search generations", searchGenerations)
	}

	second, err := gc.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Catalog GC pass: %v", err)
	}
	if second.DeletedGenerations != 1 {
		t.Fatalf("second Catalog GC pass result=%+v", second)
	}
	if ids := fixture.generationIDs(t); len(ids) != 1 || ids[0] != active.ID {
		t.Fatalf("generations after second pass=%v want only %s", ids, active.ID)
	}
	if entries := fixture.catalogEntryCount(t, oldest.ID); entries != 0 {
		t.Fatalf("reclaimed generation entries=%d", entries)
	}
	if entries := fixture.catalogEntryCount(t, active.ID); entries != 1 {
		t.Fatalf("active generation entries=%d want 1", entries)
	}
	if documents, postings, fields := fixture.searchPayloadCounts(t, strings.Repeat("5", 32)); documents != 1 || postings != 1 || fields != 1 {
		t.Fatalf("active generation payload documents=%d postings=%d fields=%d", documents, postings, fields)
	}
	if entries := fixture.catalogEntryCount(t, middle.ID); entries != 0 {
		t.Fatalf("second reclaimed generation entries=%d", entries)
	}
	// A third pass has nothing left to reclaim and must stay idempotent.
	third, err := gc.Collect(context.Background())
	if err != nil || third.DeletedGenerations != 0 {
		t.Fatalf("idle Catalog GC pass result=%+v err=%v", third, err)
	}
}

// TestCatalogGenerationGCSkipsRestrictedGenerationButReclaimsOthers covers
// contract case 2: a RESTRICT-referenced generation is never deleted and never
// trips a foreign-key error, and because it is the *older* generation it must
// not stall reclamation of the newer unreferenced one.
func TestCatalogGenerationGCSkipsRestrictedGenerationButReclaimsOthers(t *testing.T) {
	fixture := newCatalogGCFixture(t)
	restricted := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-3*time.Hour), 1)
	free := fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationSuperseded, false, fixture.now.Add(-2*time.Hour), 0)
	active := fixture.seedGeneration(t, strings.Repeat("3", 32), 3, catalog.GenerationComplete, true, fixture.now.Add(-time.Hour), 0)
	entryID := fmt.Sprintf("%064x", 1)
	if err := fixture.db.Create(&model.BackupAssetDeliveryGrant{
		ID: strings.Repeat("6", 32), DeliveryID: strings.Repeat("7", 32), ResourceKind: "backup_asset",
		RecoveryPointID: &fixture.pointID, CatalogGenerationID: &restricted.ID, EntryID: &entryID,
	}).Error; err != nil {
		t.Fatalf("seed RESTRICT delivery grant: %v", err)
	}
	gc := fixture.collector()

	result, err := gc.Collect(context.Background())
	if err != nil {
		t.Fatalf("Catalog GC with a restricted generation: %v", err)
	}
	if result.DeletedGenerations != 1 {
		t.Fatalf("Catalog GC result=%+v want one reclaim despite the restricted prefix", result)
	}
	ids := fixture.generationIDs(t)
	if len(ids) != 2 {
		t.Fatalf("generations after restricted pass=%v", ids)
	}
	present := map[string]bool{}
	for _, id := range ids {
		present[id] = true
	}
	if !present[restricted.ID] || !present[active.ID] || present[free.ID] {
		t.Fatalf("restricted generation handling wrong: present=%v", present)
	}
	if entries := fixture.catalogEntryCount(t, restricted.ID); entries != 1 {
		t.Fatalf("restricted generation lost entries=%d", entries)
	}
	// The restricted generation stays out of the candidate query, so a later
	// pass deletes nothing and does not increment skipped-restricted.
	later, err := gc.Collect(context.Background())
	if err != nil || later.DeletedGenerations != 0 || later.SkippedRestricted != 0 {
		t.Fatalf("repeated restricted pass result=%+v err=%v", later, err)
	}
	if ids := fixture.generationIDs(t); len(ids) != 2 {
		t.Fatalf("repeated restricted pass removed generations: %v", ids)
	}
}

// TestCatalogGenerationGCKeepsBuildingGenerationAndLiveLease covers contract
// case 3: a live Catalog build lease protects every generation of its point, and
// a building generation is never a reclamation candidate even without a lease.
func TestCatalogGenerationGCKeepsBuildingGenerationAndLiveLease(t *testing.T) {
	fixture := newCatalogGCFixture(t)
	superseded := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-3*time.Hour), 0)
	building := fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationBuilding, false, fixture.now.Add(-2*time.Hour), 0)
	active := fixture.seedGeneration(t, strings.Repeat("3", 32), 3, catalog.GenerationComplete, true, fixture.now.Add(-time.Hour), 0)
	if err := fixture.db.Create(&model.RecoveryPointLease{
		ID: strings.Repeat("8", 32), RecoveryPointID: fixture.pointID,
		HolderType: string(backupasset.LeaseHolderCatalogBuild), OwnerID: "catalog:" + fixture.pointID,
		AttemptID: strings.Repeat("9", 32), FenceToken: strings.Repeat("f", 64), Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: fixture.now.Add(time.Hour), AbsoluteDeadline: fixture.now.Add(2 * time.Hour),
		LastHeartbeatAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("seed live Catalog lease: %v", err)
	}
	gc := fixture.collector()

	result, err := gc.Collect(context.Background())
	if err != nil || result.DeletedGenerations != 0 {
		t.Fatalf("Catalog GC under a live lease result=%+v err=%v", result, err)
	}
	if ids := fixture.generationIDs(t); len(ids) != 3 {
		t.Fatalf("live lease allowed reclamation: %v", ids)
	}

	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ?", fixture.pointID).
		Update("status", string(backupasset.LeaseReleased)).Error; err != nil {
		t.Fatalf("release live Catalog lease: %v", err)
	}
	released, err := gc.Collect(context.Background())
	if err != nil || released.DeletedGenerations != 1 {
		t.Fatalf("Catalog GC after lease release result=%+v err=%v", released, err)
	}
	ids := fixture.generationIDs(t)
	present := map[string]bool{}
	for _, id := range ids {
		present[id] = true
	}
	if present[superseded.ID] || !present[building.ID] || !present[active.ID] {
		t.Fatalf("building generation reclamation wrong: present=%v", present)
	}
}

// TestCatalogGenerationGCKeepsLatestFailureEvidence covers contract case 4: the
// latest generation and the two most recent failed/partial attempts survive, so
// retry backoff keeps reading durable evidence, while older superseded
// generations are reclaimed.
func TestCatalogGenerationGCKeepsLatestFailureEvidence(t *testing.T) {
	fixture := newCatalogGCFixture(t)
	superseded := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-6*time.Hour), 0)
	oldestFailure := fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationFailed, false, fixture.now.Add(-5*time.Hour), 0)
	olderFailure := fixture.seedGeneration(t, strings.Repeat("3", 32), 3, catalog.GenerationFailed, false, fixture.now.Add(-4*time.Hour), 0)
	latestFailure := fixture.seedGeneration(t, strings.Repeat("4", 32), 4, catalog.GenerationPartial, false, fixture.now.Add(-3*time.Hour), 0)
	gc := fixture.collector()

	result, err := gc.Collect(context.Background())
	if err != nil || result.DeletedGenerations != 1 {
		t.Fatalf("Catalog GC with failure evidence result=%+v err=%v", result, err)
	}
	ids := fixture.generationIDs(t)
	present := map[string]bool{}
	for _, id := range ids {
		present[id] = true
	}
	if present[superseded.ID] {
		t.Fatalf("old superseded generation survived: %v", ids)
	}
	if !present[oldestFailure.ID] || !present[olderFailure.ID] || !present[latestFailure.ID] {
		t.Fatalf("failure evidence was reclaimed: %v", ids)
	}
}

// TestCatalogGenerationGCStopsOnCancellationAndResumes proves the collector is
// interruptible and resumable: a canceled context stops the pass before the
// generation row is removed, the payload already deleted stays committed, and
// the next pass finishes the generation without leaving a partial delete.
func TestCatalogGenerationGCStopsOnCancellationAndResumes(t *testing.T) {
	fixture := newCatalogGCFixture(t)
	victim := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-2*time.Hour), 3)
	active := fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationComplete, true, fixture.now.Add(-time.Hour), 0)
	fixture.seedSearchProjection(t, victim.ID, strings.Repeat("3", 32), false, 4)
	gc := fixture.collector()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gc.Collect(canceled); err == nil {
		t.Fatal("canceled Catalog GC pass reported no error")
	}
	if ids := fixture.generationIDs(t); len(ids) != 2 {
		t.Fatalf("canceled pass removed generations: %v", ids)
	}

	resumed, err := gc.Collect(context.Background())
	if err != nil || resumed.DeletedGenerations != 1 {
		t.Fatalf("resumed Catalog GC pass result=%+v err=%v", resumed, err)
	}
	if ids := fixture.generationIDs(t); len(ids) != 1 || ids[0] != active.ID {
		t.Fatalf("resumed pass generations=%v want only %s", ids, active.ID)
	}
	if entries := fixture.catalogEntryCount(t, victim.ID); entries != 0 {
		t.Fatalf("resumed pass left entries=%d", entries)
	}
	if documents, postings, fields := fixture.searchPayloadCounts(t, strings.Repeat("3", 32)); documents != 0 || postings != 0 || fields != 0 {
		t.Fatalf("resumed pass left payload documents=%d postings=%d fields=%d", documents, postings, fields)
	}
}

// TestCatalogGenerationGCResumesAcrossBudgetBoundScans proves the per-scan budget
// is resumable rather than lossy: a generation owning many more payload batches
// than one scan may delete keeps its row and finishes over later scans.
func TestCatalogGenerationGCResumesAcrossBudgetBoundScans(t *testing.T) {
	fixture := newCatalogGCFixture(t)
	// Each scan may delete at most catalogGCPayloadBatchesPerScan batches of
	// catalogGCPayloadDocumentBatch documents, so this generation needs more
	// scans than one.
	documentCount := catalogGCPayloadDocumentBatch*(catalogGCPayloadBatchesPerScan+1) + 7
	victim := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-2*time.Hour), 0)
	fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationComplete, true, fixture.now.Add(-time.Hour), 0)
	fixture.seedSearchProjection(t, victim.ID, strings.Repeat("3", 32), false, documentCount)
	gc := fixture.collector()

	first, err := gc.Collect(context.Background())
	if err != nil {
		t.Fatalf("first budget-bound Catalog GC pass: %v", err)
	}
	if first.DeletedGenerations != 0 {
		t.Fatalf("first budget-bound pass deleted=%d want deferred", first.DeletedGenerations)
	}
	if ids := fixture.generationIDs(t); len(ids) != 2 {
		t.Fatalf("budget-bound pass removed the generation row early: %v", ids)
	}
	documents, _, _ := fixture.searchPayloadCounts(t, strings.Repeat("3", 32))
	if documents != int64(documentCount-catalogGCPayloadDocumentBatch*catalogGCPayloadBatchesPerScan) {
		t.Fatalf("budget-bound pass leftover documents=%d", documents)
	}

	second, err := gc.Collect(context.Background())
	if err != nil || second.DeletedGenerations != 1 {
		t.Fatalf("second budget-bound pass result=%+v err=%v", second, err)
	}
	if ids := fixture.generationIDs(t); len(ids) != 1 {
		t.Fatalf("budget-bound pass did not finish: %v", ids)
	}
}

// newCatalogGCRealSchemaFixture opens the migrated production schema so the GC
// runs against the real foreign keys and CHECK constraints, not GORM's
// approximation of them.
func newCatalogGCRealSchemaFixture(t *testing.T) *catalogGCFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "catalog-gc-real.db")})
	if err != nil {
		t.Fatalf("open real-schema Catalog GC database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("migrate real-schema Catalog GC database: %v", err)
	}
	db.Logger = logger.Default.LogMode(logger.Silent)
	now := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	fixture := &catalogGCFixture{db: db, now: now, repoID: strings.Repeat("b", 32), pointID: strings.Repeat("a", 32)}
	if err := db.Create(&model.BackupRepository{
		ID: fixture.repoID, ProviderKind: string(backupasset.ProviderRsync), DisplayName: "catalog-gc-real",
		VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed real-schema repository: %v", err)
	}
	if err := db.Create(&model.RecoveryPoint{
		ID: fixture.pointID, RepositoryID: fixture.repoID, Semantics: string(backupasset.PointMutableHead),
		State: string(backupasset.RecoveryPointObserved), SourceFingerprint: strings.Repeat("c", 64),
		CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		ObservedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed real-schema point: %v", err)
	}
	return fixture
}

// TestCatalogGenerationGCReclaimsOnMigratedSchema proves reclamation against the
// real migrated schema: the protected lookup columns exist, the Search payload is
// removed in document batches ahead of its generation rows, and the generation
// row is deleted without tripping a foreign key.
func TestCatalogGenerationGCReclaimsOnMigratedSchema(t *testing.T) {
	fixture := newCatalogGCRealSchemaFixture(t)
	victim := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-2*time.Hour), 3)
	active := fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationComplete, true, fixture.now.Add(-time.Hour), 1)
	fixture.seedSearchProjection(t, victim.ID, strings.Repeat("3", 32), false, 3)
	fixture.seedSearchProjection(t, active.ID, strings.Repeat("4", 32), true, 1)
	gc := fixture.collector()

	result, err := gc.Collect(context.Background())
	if err != nil {
		t.Fatalf("real-schema Catalog GC pass: %v", err)
	}
	if result.DeletedGenerations != 1 || result.SkippedRestricted != 0 {
		t.Fatalf("real-schema Catalog GC result=%+v", result)
	}
	if ids := fixture.generationIDs(t); len(ids) != 1 || ids[0] != active.ID {
		t.Fatalf("real-schema generations=%v want only %s", ids, active.ID)
	}
	if entries := fixture.catalogEntryCount(t, victim.ID); entries != 0 {
		t.Fatalf("real-schema reclaimed entries=%d", entries)
	}
	documents, postings, fields := fixture.searchPayloadCounts(t, strings.Repeat("3", 32))
	if documents != 0 || postings != 0 || fields != 0 {
		t.Fatalf("real-schema reclaimed payload documents=%d postings=%d fields=%d", documents, postings, fields)
	}
	var searchGenerations int64
	if err := fixture.db.Model(&model.BackupAssetSearchGeneration{}).
		Where("catalog_generation_id = ?", victim.ID).Count(&searchGenerations).Error; err != nil {
		t.Fatalf("count real-schema Search generations: %v", err)
	}
	if searchGenerations != 0 {
		t.Fatalf("real-schema reclaimed generation still owns %d Search generations", searchGenerations)
	}
	if documents, postings, fields := fixture.searchPayloadCounts(t, strings.Repeat("4", 32)); documents != 1 || postings != 1 || fields != 1 {
		t.Fatalf("real-schema active payload documents=%d postings=%d fields=%d", documents, postings, fields)
	}
}

// TestCatalogGenerationGCProtectsLatestSupersededGeneration covers the
// completion gap: backup completion supersedes the active generation and the
// replacement has not been built yet, so the latest generation is a superseded
// inactive row. Eligibility reads that latest row, so it must be protected while
// older generations are still reclaimable.
func TestCatalogGenerationGCProtectsLatestSupersededGeneration(t *testing.T) {
	fixture := newCatalogGCFixture(t)
	older := fixture.seedGeneration(t, strings.Repeat("1", 32), 1, catalog.GenerationSuperseded, false, fixture.now.Add(-3*time.Hour), 0)
	completionSuperseded := fixture.seedGeneration(t, strings.Repeat("2", 32), 2, catalog.GenerationSuperseded, false, fixture.now.Add(-time.Hour), 1)
	if err := fixture.db.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ?", fixture.pointID).
		Update("is_active", false).Error; err != nil {
		t.Fatalf("clear active projection: %v", err)
	}
	gc := fixture.collector()

	result, err := gc.Collect(context.Background())
	if err != nil || result.DeletedGenerations != 1 {
		t.Fatalf("completion-gap GC result=%+v err=%v", result, err)
	}
	ids := fixture.generationIDs(t)
	if len(ids) != 1 || ids[0] != completionSuperseded.ID {
		t.Fatalf("completion-gap generations=%v want only latest %s", ids, completionSuperseded.ID)
	}
	if entries := fixture.catalogEntryCount(t, completionSuperseded.ID); entries != 1 {
		t.Fatalf("completion-gap latest generation lost entries=%d", entries)
	}
	if entries := fixture.catalogEntryCount(t, older.ID); entries != 0 {
		t.Fatalf("completion-gap older generation entries=%d", entries)
	}
}
