package search

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var searchBehaviorDBSequence atomic.Uint64

func TestSearchBehaviorSQLite(t *testing.T) {
	runSearchBehaviorContract(t, openSearchBehaviorSQLite(t))
}

func TestSearchBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_SEARCH_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_SEARCH_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runSearchBehaviorContract(t, openSearchBehaviorPostgres(t, dsn))
}

func TestSearchSourceLifecycleMigratedSchemaSQLite(t *testing.T) {
	fixture := openSearchBehaviorSQLite(t)
	if err := database.RunMigrations(fixture.db, fixture.engine); err != nil {
		t.Fatalf("run %s migrations: %v", fixture.engine, err)
	}
	runSearchSourceLifecycleMigratedSchemaContract(t, fixture)
}

func TestSearchSourceLifecycleMigratedSchemaPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_SEARCH_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_SEARCH_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	fixture := openSearchBehaviorPostgres(t, dsn)
	if err := database.RunMigrations(fixture.db, fixture.engine); err != nil {
		t.Fatalf("run %s migrations: %v", fixture.engine, err)
	}
	runSearchSourceLifecycleMigratedSchemaContract(t, fixture)
}

func runSearchSourceLifecycleMigratedSchemaContract(t *testing.T, fixture searchBehaviorFixture) {
	t.Helper()
	now := fixture.now
	repository := model.BackupRepository{
		ID: strings.Repeat("1", 32), ProviderKind: string(backupasset.ProviderRsync),
		DisplayName: "search-source-lifecycle", VersionMode: string(backupasset.VersionFullCopyTree),
		Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: "{}",
		ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&repository).Error; err != nil {
		t.Fatalf("seed %s repository: %v", fixture.engine, err)
	}
	pointID := strings.Repeat("2", 32)
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repository.ID, LineageJSON: "{}",
		Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		SourceFingerprint: strings.Repeat("a", 64), ManifestDigestAlgorithm: "sha256",
		ConsistencyJSON: "{}", FidelityJSON: "{}", PointRevision: 1,
		CapabilityRevision: 1, CapabilitiesJSON: "{}",
		ImmutabilityLevel:    string(backupasset.ImmutabilityXirangManaged),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatalf("seed %s point: %v", fixture.engine, err)
	}
	catalogID := strings.Repeat("3", 32)
	finishedAt := now
	catalogGeneration := model.CatalogGeneration{
		ID: catalogID, RecoveryPointID: pointID, Generation: 1, State: "complete", IsActive: true,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: 3, WrittenEntryCount: 3,
		StartedAt: now, FinishedAt: &finishedAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&catalogGeneration).Error; err != nil {
		t.Fatalf("seed %s Catalog generation: %v", fixture.engine, err)
	}
	searchGenerationID := strings.Repeat("4", 32)
	searchGeneration := model.BackupAssetSearchGeneration{
		ID: searchGenerationID, RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		Generation: 1, State: string(SearchGenerationBuilding), SourceFingerprint: point.SourceFingerprint,
		NormalizerVersion: NormalizerVersion, SearchKeyVersion: 1, ProjectionRevision: 1,
		LeaseID: strings.Repeat("5", 32), BuildAttemptID: strings.Repeat("6", 32),
		FenceTokenHash: strings.Repeat("7", 64), ExpectedDocumentCount: 3, WrittenDocumentCount: 3,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&searchGeneration).Error; err != nil {
		t.Fatalf("seed %s Search generation: %v", fixture.engine, err)
	}
	for index, entryMarker := range []string{"a", "b", "c"} {
		entryID := strings.Repeat(entryMarker, 64)
		entry := model.CatalogEntry{
			GenerationID: catalogID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "bounded/" + entryMarker, Name: entryMarker, EntryType: "file",
			SecurityState: "non_secret", CreatedAt: now,
		}
		if err := fixture.db.Create(&entry).Error; err != nil {
			t.Fatalf("seed %s bounded Catalog entry ordinal=%d: %v", fixture.engine, index, err)
		}
		document := model.BackupAssetSearchDocument{
			SearchGenerationID: searchGenerationID, DocumentID: entryID, RecoveryPointID: pointID,
			CatalogGenerationID: catalogID, EntryID: entryID, Sensitivity: string(SensitivityNonSecret),
			ClassificationRevision: 1, MetadataRevision: 1, EntryType: "file",
			LineageToken: strings.Repeat("d", 64), PathGroupToken: strings.Repeat(entryMarker, 64),
			PathSortKey: entryMarker, NameSortKey: entryMarker, CreatedAt: now, UpdatedAt: now,
		}
		if err := fixture.db.Create(&document).Error; err != nil {
			t.Fatalf("seed %s bounded Search document ordinal=%d: %v", fixture.engine, index, err)
		}
		posting := model.BackupAssetSearchPosting{
			SearchGenerationID: searchGenerationID, DocumentID: entryID, Field: string(SearchFieldName),
			TokenKind: string(TokenKindExact), KeyVersion: 1, TokenHMAC: strings.Repeat(string(rune('e'+index)), 64), TermFrequency: 1,
		}
		if err := fixture.db.Create(&posting).Error; err != nil {
			t.Fatalf("seed %s bounded Search posting ordinal=%d: %v", fixture.engine, index, err)
		}
		field := model.BackupAssetSearchDocumentField{
			SearchGenerationID: searchGenerationID, DocumentID: entryID, Field: string(SearchFieldName),
			State: string(FieldCoverageComplete), CoverageRevision: 1, ClassificationRevision: 1,
			PipelineRevision: 1, IndexRevision: 1, SourceFingerprint: point.SourceFingerprint, UpdatedAt: now,
		}
		if err := fixture.db.Create(&field).Error; err != nil {
			t.Fatalf("seed %s bounded Search field ordinal=%d: %v", fixture.engine, index, err)
		}
	}
	attemptID := strings.Repeat("8", 32)
	attempt := model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseRevoking), TransitionRevision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&attempt).Error; err != nil {
		t.Fatalf("seed %s lifecycle attempt: %v", fixture.engine, err)
	}
	owner, err := NewSourceLifecycle(fixture.db, &Indexer{db: fixture.db}, func() time.Time { return now }, 2)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("prepare %s migrated Search source lifecycle: %v", fixture.engine, err)
	}
	var persisted model.BackupAssetSearchGeneration
	if err := fixture.db.First(&persisted, "id = ?", searchGenerationID).Error; err != nil {
		t.Fatalf("load %s Search generation: %v", fixture.engine, err)
	}
	if persisted.State != string(SearchGenerationFailed) || persisted.IsActive ||
		persisted.ErrorCode != string(SearchErrorBuildFailed) || persisted.FinishedAt == nil {
		t.Fatalf("%s Search prepare state=%s active=%t error_code=%s finished=%t",
			fixture.engine, persisted.State, persisted.IsActive, persisted.ErrorCode, persisted.FinishedAt != nil)
	}
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ?", attemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance %s lifecycle attempt to cleaning: %v", fixture.engine, err)
	}
	request.Stage = backupasset.SourceLifecycleCleanup
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("cleanup %s migrated Search source lifecycle: %v", fixture.engine, err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent cleanup %s migrated Search source lifecycle: %v", fixture.engine, err)
	}
	var documents, postings, fields, generations, catalogEntries int64
	if err := fixture.db.Model(&model.BackupAssetSearchDocument{}).
		Where("search_generation_id = ?", searchGenerationID).Count(&documents).Error; err != nil {
		t.Fatalf("count %s cleaned Search documents: %v", fixture.engine, err)
	}
	if err := fixture.db.Model(&model.BackupAssetSearchPosting{}).
		Where("search_generation_id = ?", searchGenerationID).Count(&postings).Error; err != nil {
		t.Fatalf("count %s cleaned Search postings: %v", fixture.engine, err)
	}
	if err := fixture.db.Model(&model.BackupAssetSearchDocumentField{}).
		Where("search_generation_id = ?", searchGenerationID).Count(&fields).Error; err != nil {
		t.Fatalf("count %s cleaned Search fields: %v", fixture.engine, err)
	}
	if err := fixture.db.Model(&model.BackupAssetSearchGeneration{}).
		Where("id = ? AND recovery_point_id = ?", searchGenerationID, pointID).Count(&generations).Error; err != nil {
		t.Fatalf("count %s retained Search generation evidence: %v", fixture.engine, err)
	}
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND recovery_point_id = ?", catalogID, pointID).Count(&catalogEntries).Error; err != nil {
		t.Fatalf("count %s retained Catalog entries: %v", fixture.engine, err)
	}
	if documents != 0 || postings != 0 || fields != 0 || generations != 1 || catalogEntries != 3 {
		t.Fatalf("%s bounded cleanup documents=%d postings=%d fields=%d generations=%d catalog_entries=%d, want 0/0/0/1/3",
			fixture.engine, documents, postings, fields, generations, catalogEntries)
	}
	if err := fixture.db.First(&persisted, "id = ?", searchGenerationID).Error; err != nil {
		t.Fatalf("reload %s retained Search generation: %v", fixture.engine, err)
	}
	if persisted.State != string(SearchGenerationFailed) || persisted.IsActive || persisted.ErrorCode != string(SearchErrorBuildFailed) {
		t.Fatalf("%s retained Search generation state=%s active=%t error_code=%s",
			fixture.engine, persisted.State, persisted.IsActive, persisted.ErrorCode)
	}
}

type searchBehaviorFixture struct {
	db     *gorm.DB
	engine string
	now    time.Time
}

type searchBehaviorSummary struct {
	Names              []string
	Scores             []int64
	Fields             [][]SearchField
	Total              int64
	Relation           TotalRelation
	Coverage           CoverageStatus
	AuthoritativeEmpty bool
	QueryGeneration    string
}

func runSearchBehaviorContract(t *testing.T, fixture searchBehaviorFixture) {
	t.Helper()
	secure.ResetForTesting()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SEARCH_BEHAVIOR_DATA_KEY_FOR_TEST_ONLY")
	t.Cleanup(secure.ResetForTesting)
	prepareSearchBehaviorSchema(t, fixture)
	ring := backupasset.NewKeyring(fixture.db, func() time.Time { return fixture.now })
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatalf("ensure %s Search token: %v", fixture.engine, err)
	}
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatalf("ensure %s cursor key: %v", fixture.engine, err)
	}
	lease, err := backupasset.NewLeaseService(fixture.db, func() time.Time { return fixture.now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexer, err := NewIndexer(IndexerDependencies{
		DB: fixture.db, Lease: lease, Keys: ring, Now: func() time.Time { return fixture.now }, Config: standardIndexerConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	harness := &indexerTestHarness{db: fixture.db, ring: ring, now: fixture.now}
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{
		{EntryID: strings.Repeat("1", 64), NormalizedPath: "A/Straße.txt", Name: "Straße.txt", EntryType: "file", Size: 10, SecurityState: "non_secret"},
		{EntryID: strings.Repeat("2", 64), NormalizedPath: "B/年度报告.txt", Name: "年度报告.txt", EntryType: "file", Size: 20, SecurityState: "non_secret"},
	})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatalf("build %s behavior projection: %v", fixture.engine, err)
	}
	scope, err := NewScopeResolver(fixture.db, &scopeTestAuthorizer{allowed: map[string]bool{pointID: true}}, ScopeResolverLimits{MaxCandidates: 100})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceDependencies{
		DB: fixture.db, Scope: scope, Keys: ring,
		Cursor: NewCursorCodec(ring, func() time.Time { return fixture.now }, 15*time.Minute),
		Now:    func() time.Time { return fixture.now }, Limits: DefaultServiceLimits(),
		FeatureEnabled: func() (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := SearchActor{Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1}}
	exact := SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}}
	unicodeResult, err := service.Search(context.Background(), actor, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "STRASSE"},
		Scope: exact, Sort: SearchSortRelevance, Limit: 20,
	})
	if err != nil || len(unicodeResult.Items) != 1 || unicodeResult.Items[0].Asset.Name != "Straße.txt" {
		t.Fatalf("%s Unicode behavior result=%+v err=%v", fixture.engine, unicodeResult, err)
	}
	hanResult, err := service.Search(context.Background(), actor, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "报告"},
		Scope: exact, Sort: SearchSortRelevance, Limit: 20,
	})
	if err != nil || len(hanResult.Items) != 1 || hanResult.Items[0].Asset.Name != "年度报告.txt" {
		t.Fatalf("%s Han bigram behavior result=%+v err=%v", fixture.engine, hanResult, err)
	}

	request := SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpType, Values: []string{"file"}},
		Scope: exact, Sort: SearchSortRelevance, Limit: 1,
	}
	first, err := service.Search(context.Background(), actor, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Cursor = first.NextCursor
	second, err := service.Search(context.Background(), actor, request)
	if err != nil {
		t.Fatal(err)
	}
	items := append(append([]SearchHit(nil), first.Items...), second.Items...)
	summary := searchBehaviorSummary{
		Total: *first.Total, Relation: first.TotalRelation, Coverage: first.Coverage.Status,
		AuthoritativeEmpty: first.AuthoritativeEmpty, QueryGeneration: first.QueryGeneration,
	}
	for _, item := range items {
		summary.Names = append(summary.Names, item.Asset.Name)
		summary.Scores = append(summary.Scores, item.Score)
		summary.Fields = append(summary.Fields, item.HitFields)
	}
	want := searchBehaviorSummary{
		Names: []string{"Straße.txt", "年度报告.txt"}, Scores: []int64{1_100_401, 1_100_401},
		Fields: [][]SearchField{{SearchFieldType}, {SearchFieldType}}, Total: 2,
		Relation: TotalRelationExact, Coverage: CoverageComplete,
		AuthoritativeEmpty: false, QueryGeneration: summary.QueryGeneration,
	}
	if !reflect.DeepEqual(summary, want) || first.QueryGeneration != second.QueryGeneration {
		t.Fatalf("%s behavior summary=%+v want=%+v second_generation=%s", fixture.engine, summary, want, second.QueryGeneration)
	}
	assertAtomicContentProjectionBehavior(t, fixture, harness, pointID, searchGenerationIDForBehavior(t, fixture.db, pointID))
}

func assertAtomicContentProjectionBehavior(
	t *testing.T,
	fixture searchBehaviorFixture,
	harness *indexerTestHarness,
	pointID string,
	searchGenerationID string,
) {
	t.Helper()
	entryID := strings.Repeat("1", 64)
	var catalog model.CatalogGeneration
	if err := fixture.db.Where("recovery_point_id = ? AND is_active = ?", pointID, true).Take(&catalog).Error; err != nil {
		t.Fatalf("load %s active Catalog generation: %v", fixture.engine, err)
	}
	ingest, processingLease := newContentIngestForHarness(t, harness)
	projection := ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldContent,
		Terms: []TermFrequency{{Term: "cross-engine-atomic", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
		CatalogGenerationID: catalog.ID, SearchGenerationID: searchGenerationID,
		ProcessingLeaseID: processingLease.ID, AttemptID: processingLease.Fence.AttemptID,
		FenceToken: processingLease.Fence.FenceToken, ExpectedClassificationRevision: 1,
		Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2,
		ExcerptRef: stringPointer(strings.Repeat("f", 32)), Coverage: FieldCoverageComplete,
	}
	prepared, err := ingest.PrepareContentProjection(context.Background(), projection)
	if err != nil {
		t.Fatalf("prepare %s content projection: %v", fixture.engine, err)
	}
	before := contentProjectionSnapshot(t, harness, searchGenerationID, entryID, SearchFieldContent)
	rollback := fmt.Errorf("force %s caller rollback", fixture.engine)
	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		if err := ingest.PublishContentProjectionTx(context.Background(), tx, prepared); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("%s publish rollback error=%v", fixture.engine, err)
	}
	if after := contentProjectionSnapshot(t, harness, searchGenerationID, entryID, SearchFieldContent); !reflect.DeepEqual(before, after) {
		t.Fatalf("%s publish rollback leaked state: before=%+v after=%+v", fixture.engine, before, after)
	}
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		return ingest.PublishContentProjectionTx(context.Background(), tx, prepared)
	}); err != nil {
		t.Fatalf("publish %s content projection: %v", fixture.engine, err)
	}
	revoke := RevokeProjection{
		Ref: projection.Ref, Field: projection.Field, SourceFingerprint: projection.SourceFingerprint,
		CatalogGenerationID: projection.CatalogGenerationID, SearchGenerationID: projection.SearchGenerationID,
		ProcessingLeaseID: projection.ProcessingLeaseID, AttemptID: projection.AttemptID, FenceToken: projection.FenceToken,
		ExpectedClassificationRevision: 1, CoverageRevision: 3, PipelineRevision: 3, IndexRevision: 3,
	}
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		return ingest.RevokeContentProjectionTx(context.Background(), tx, revoke)
	}); err != nil {
		t.Fatalf("revoke %s content projection: %v", fixture.engine, err)
	}
	snapshot := contentProjectionSnapshot(t, harness, searchGenerationID, entryID, SearchFieldContent)
	if snapshot.Postings != 0 || snapshot.State != string(FieldCoverageUnavailable) || snapshot.ExcerptRef != "" {
		t.Fatalf("%s revoked projection=%+v", fixture.engine, snapshot)
	}
}

func searchGenerationIDForBehavior(t *testing.T, db *gorm.DB, pointID string) string {
	t.Helper()
	var generation model.BackupAssetSearchGeneration
	if err := db.Where("recovery_point_id = ? AND is_active = ?", pointID, true).Take(&generation).Error; err != nil {
		t.Fatal(err)
	}
	return generation.ID
}

func openSearchBehaviorSQLite(t *testing.T) searchBehaviorFixture {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"), searchBehaviorDBSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return searchBehaviorFixture{db: db, engine: "sqlite", now: time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)}
}

func openSearchBehaviorPostgres(t *testing.T, dsn string) searchBehaviorFixture {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL behavior base: %v", err)
	}
	schema := fmt.Sprintf("xirang_search_%d", time.Now().UnixNano())
	if err := base.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatalf("create PostgreSQL behavior schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open scoped PostgreSQL behavior DB: %v", err)
	}
	t.Cleanup(func() { _ = base.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error })
	return searchBehaviorFixture{db: db, engine: "postgres", now: time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)}
}

func prepareSearchBehaviorSchema(t *testing.T, fixture searchBehaviorFixture) {
	t.Helper()
	if err := fixture.db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.WrappedDomainKey{}, &model.RecoveryPointLease{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchPosting{}, &model.BackupAssetSearchDocumentField{},
	); err != nil {
		t.Fatalf("migrate %s Search behavior schema: %v", fixture.engine, err)
	}
	truth := "1"
	if fixture.engine == "postgres" {
		truth = "TRUE"
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX idx_behavior_catalog_active ON catalog_generations(recovery_point_id) WHERE is_active = ` + truth,
		`CREATE UNIQUE INDEX idx_behavior_search_active ON backup_asset_search_generations(recovery_point_id) WHERE is_active = ` + truth,
		`CREATE UNIQUE INDEX idx_behavior_key_active ON wrapped_domain_keys(domain) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX idx_behavior_lease_active ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`,
	} {
		if err := fixture.db.Exec(statement).Error; err != nil {
			t.Fatalf("create %s behavior index: %v", fixture.engine, err)
		}
	}
}
