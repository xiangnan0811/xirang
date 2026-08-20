package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type rebuildCatalogStarterSpy struct {
	requests []CatalogRebuildRequest
}

func (spy *rebuildCatalogStarterSpy) StartFreshCatalogGeneration(_ context.Context, request CatalogRebuildRequest) (CatalogRebuildStart, error) {
	spy.requests = append(spy.requests, request)
	return CatalogRebuildStart{GenerationID: strings.Repeat(string('a'+rune(len(spy.requests)-1)), 32)}, nil
}

type rebuildDerivedQueuerSpy struct {
	requests []DerivedBackfillRequest
	failFor  string
}

func (spy *rebuildDerivedQueuerSpy) QueueLowPriorityDerivedBackfill(_ context.Context, request DerivedBackfillRequest) (int, error) {
	spy.requests = append(spy.requests, request)
	if request.RecoveryPointID == spy.failFor {
		return 0, errors.New("FAKE_DERIVED_QUEUE_FAILURE_FOR_TEST_ONLY")
	}
	return 1, nil
}

func TestRebuildAcceptedImportsIsBoundedAndCursorStable(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 9, 54, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "bounded rebuild", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	wantPointIDs := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		pointID := strings.Repeat(string(rune('1'+index)), 32)
		createdAt := now.Add(time.Duration(index/2) * time.Minute)
		point := seedAcceptedRebuildImport(t, db, repository, pointID,
			strings.Repeat(string(rune('a'+index)), 64), strings.Repeat(string(rune('b'+index)), 64), createdAt, false)
		wantPointIDs = append(wantPointIDs, point.ID)
	}
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := RequestContext{Actor: backupasset.AuditActor{UserID: 31, Username: "admin", Role: "admin"}}

	var cursor string
	pageSizes := []int{2, 2, 1}
	for pageIndex, wantSize := range pageSizes {
		result, err := service.RebuildAcceptedImports(context.Background(), repositoryID,
			RebuildRequest{Limit: 2, Cursor: cursor}, adminContext)
		if err != nil {
			t.Fatalf("page %d: %v", pageIndex, err)
		}
		if result.Accepted != wantSize || result.CatalogStarted != wantSize || result.DerivedQueued != wantSize ||
			result.Partial != 0 || result.Failed != 0 || len(result.Reasons) != 0 {
			t.Fatalf("page %d result=%+v", pageIndex, result)
		}
		if pageIndex < len(pageSizes)-1 && result.NextCursor == "" {
			t.Fatalf("page %d missing continuation: %+v", pageIndex, result)
		}
		if pageIndex == len(pageSizes)-1 && result.NextCursor != "" {
			t.Fatalf("terminal page continuation=%q", result.NextCursor)
		}
		cursor = result.NextCursor
	}
	if len(catalogStarter.requests) != len(wantPointIDs) || len(derivedQueuer.requests) != len(wantPointIDs) {
		t.Fatalf("catalog=%d derived=%d", len(catalogStarter.requests), len(derivedQueuer.requests))
	}
	seen := make(map[string]struct{}, len(wantPointIDs))
	for index, request := range catalogStarter.requests {
		if request.RecoveryPointID != wantPointIDs[index] {
			t.Fatalf("catalog[%d]=%s want=%s", index, request.RecoveryPointID, wantPointIDs[index])
		}
		if _, duplicate := seen[request.RecoveryPointID]; duplicate {
			t.Fatalf("duplicate catalog request for %s", request.RecoveryPointID)
		}
		seen[request.RecoveryPointID] = struct{}{}
	}
	for _, limit := range []int{-1, maxRebuildPageSize + 1} {
		if _, err := service.RebuildAcceptedImports(context.Background(), repositoryID,
			RebuildRequest{Limit: limit}, adminContext); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("invalid limit %d error=%v", limit, err)
		}
	}
	if len(catalogStarter.requests) != len(wantPointIDs) || len(derivedQueuer.requests) != len(wantPointIDs) {
		t.Fatalf("invalid limits invoked owners: catalog=%d derived=%d", len(catalogStarter.requests), len(derivedQueuer.requests))
	}
}

func TestRebuildAcceptedImportsReportsTruthfulPerManifestOutcomes(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "rebuild", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	firstPoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32), strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	secondPoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("2", 32), strings.Repeat("c", 64), strings.Repeat("d", 64), now.Add(time.Minute), false)
	seedAcceptedRebuildImport(t, db, repository, strings.Repeat("3", 32), strings.Repeat("e", 64), strings.Repeat("f", 64), now.Add(2*time.Minute), true)
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{failFor: secondPoint.ID}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(10 * time.Minute) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RebuildAcceptedImports(context.Background(), repositoryID, RebuildRequest{Limit: 100},
		RequestContext{Actor: backupasset.AuditActor{UserID: 31, Username: "admin", Role: "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 3 || result.CatalogStarted != 2 || result.DerivedQueued != 1 || result.Partial != 1 || result.Failed != 1 ||
		result.Reasons[RebuildReasonInvalidManifest] != 1 || result.Reasons[RebuildReasonDerivedQueueFailed] != 1 || len(result.Reasons) != 2 {
		t.Fatalf("result=%+v", result)
	}
	if len(catalogStarter.requests) != 2 || len(derivedQueuer.requests) != 2 {
		t.Fatalf("catalog=%+v derived=%+v", catalogStarter.requests, derivedQueuer.requests)
	}
	for index, request := range catalogStarter.requests {
		wantPoint := []model.RecoveryPoint{firstPoint, secondPoint}[index]
		if request.RepositoryID != repositoryID || request.RecoveryPointID != wantPoint.ID || request.ManifestDigest != wantPoint.ManifestDigest ||
			request.CapabilityRevision != repository.CapabilityRevision {
			t.Fatalf("catalog request[%d]=%+v", index, request)
		}
	}
	for index, request := range derivedQueuer.requests {
		if request.RepositoryID != repositoryID || request.RecoveryPointID != catalogStarter.requests[index].RecoveryPointID ||
			request.CatalogGenerationID == "" || request.Priority != "low" {
			t.Fatalf("derived request[%d]=%+v", index, request)
		}
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"native", "evidence", "credential", "locator", strings.Repeat("b", 64), strings.Repeat("d", 64)} {
		if strings.Contains(strings.ToLower(string(payload)), forbidden) {
			t.Fatalf("rebuild result leaked %q: %s", forbidden, payload)
		}
	}
}

func seedReadableRebuildBinding(t *testing.T, db *gorm.DB, repositoryID string, createdAt time.Time) {
	t.Helper()
	binding := model.RepositoryAccessBinding{
		ID: strings.Repeat("e", 32), RepositoryID: repositoryID, BindingKind: "task_derived_v1",
		EncryptedConfig: "FAKE_REBUILD_BINDING_PAYLOAD_FOR_TEST_ONLY", ConfigFingerprint: strings.Repeat("f", 64),
		Status: bindingStatusActive, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
}

func seedAcceptedRebuildImport(
	t *testing.T,
	db *gorm.DB,
	repository model.BackupRepository,
	pointID string,
	sourceFingerprint string,
	nativeID string,
	createdAt time.Time,
	driftManifest bool,
) model.RecoveryPoint {
	t.Helper()
	locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: nativeID})
	evidence, _ := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: nativeID, CapturedAt: createdAt.Add(-time.Hour),
		Semantics: backupasset.PointNativeSnapshot, SourceRevision: nativeID,
	})
	manifestDigest := nativeID
	if driftManifest {
		manifestDigest = strings.Repeat("0", 64)
	}
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repository.ID, LineageJSON: `{}`, EncryptedProviderLocator: string(locator),
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: pointerTime(createdAt.Add(-time.Hour)), CommittedAt: pointerTime(createdAt), SourceFingerprint: sourceFingerprint,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: manifestDigest, ConsistencyJSON: `{}`, FidelityJSON: `{}`, PointRevision: 1,
		CapabilityRevision: repository.CapabilityRevision, CapabilitiesJSON: repository.CapabilitiesJSON,
		ImmutabilityLevel: repository.ImmutabilityLevel, PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	reviewerID := uint(31)
	reviewedAt := createdAt
	candidateIDCharacter := byte('4') + (pointID[0] - '1')
	candidate := model.BackupRepositoryImportCandidate{
		ID: strings.Repeat(string(candidateIDCharacter), 32), RepositoryID: repository.ID, CandidateKind: string(backupasset.ImportCandidateNativeSnapshot),
		SourceFingerprint: sourceFingerprint, EncryptedProviderLocator: string(locator), EncryptedEvidence: string(evidence),
		ReviewState: string(backupasset.ImportReviewAccepted), ReviewedBy: &reviewerID, ReviewedAt: &reviewedAt, AcceptedRecoveryPointID: &point.ID,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	return point
}

func TestReconcileRebuildsStartsCatalogForAcceptedMissingCatalog(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 9, 54, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "reconcile rebuild", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	point := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32),
		strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.ReconcileRebuilds(context.Background(), 10)
	if err != nil {
		t.Fatalf("ReconcileRebuilds: %v", err)
	}
	if started != 1 {
		t.Fatalf("ReconcileRebuilds started=%d, want 1", started)
	}
	if len(catalogStarter.requests) != 1 || catalogStarter.requests[0].RecoveryPointID != point.ID {
		t.Fatalf("catalog starts=%+v, want point %s", catalogStarter.requests, point.ID)
	}
	if len(derivedQueuer.requests) != 1 || derivedQueuer.requests[0].RecoveryPointID != point.ID {
		t.Fatalf("derived queue=%+v, want point %s", derivedQueuer.requests, point.ID)
	}

	if started, err := service.ReconcileRebuilds(context.Background(), 10); err != nil || started != 1 {
		t.Fatalf("second missing-catalog pass started=%d err=%v, want 1", started, err)
	}
	if len(catalogStarter.requests) != 2 {
		t.Fatalf("missing catalog restarted=%d, want 2", len(catalogStarter.requests))
	}
}

func TestReconcileRebuildsSkipsCompleteCatalogAndRestartsIncomplete(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "reconcile rebuild guard", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	completePoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32),
		strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	buildingPoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("2", 32),
		strings.Repeat("c", 64), strings.Repeat("d", 64), now.Add(time.Minute), false)
	failedPoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("3", 32),
		strings.Repeat("e", 64), strings.Repeat("f", 64), now.Add(2*time.Minute), false)
	seedCatalogGeneration(t, db, completePoint, string(catalog.GenerationComplete), true, now)
	seedCatalogGeneration(t, db, buildingPoint, string(catalog.GenerationBuilding), false, now.Add(time.Minute))
	seedCatalogGeneration(t, db, failedPoint, string(catalog.GenerationFailed), false, now.Add(2*time.Minute))

	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.ReconcileRebuilds(context.Background(), 10)
	if err != nil {
		t.Fatalf("ReconcileRebuilds: %v", err)
	}
	if started != 1 {
		t.Fatalf("ReconcileRebuilds started=%d, want 1 incomplete rebuild", started)
	}
	if len(catalogStarter.requests) != 1 || catalogStarter.requests[0].RecoveryPointID != failedPoint.ID {
		t.Fatalf("catalog starts=%+v, want failed point %s", catalogStarter.requests, failedPoint.ID)
	}
	if startedAgain, err := service.ReconcileRebuilds(context.Background(), 10); err != nil || startedAgain != 1 {
		t.Fatalf("incomplete rebuild still eligible started=%d err=%v, want 1", startedAgain, err)
	}
	if len(catalogStarter.requests) != 2 || catalogStarter.requests[1].RecoveryPointID != failedPoint.ID {
		t.Fatalf("second pass starts=%+v, want failed point only", catalogStarter.requests)
	}
}

func TestReconcileRebuildsRetriesDerivedBackfillWithoutNewCatalog(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "derived retry rebuild", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	point := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32),
		strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{failFor: point.ID}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ReconcileRebuilds(context.Background(), 1)
	if err != nil {
		t.Fatalf("first ReconcileRebuilds: %v", err)
	}
	if first != 1 || len(catalogStarter.requests) != 1 || len(derivedQueuer.requests) != 1 {
		t.Fatalf("first tick started=%d catalog=%d derived=%d, want 1/1/1", first, len(catalogStarter.requests), len(derivedQueuer.requests))
	}
	derivedQueuer.failFor = ""
	second, err := service.ReconcileRebuilds(context.Background(), 1)
	if err != nil {
		t.Fatalf("second ReconcileRebuilds: %v", err)
	}
	if second != 0 || len(catalogStarter.requests) != 1 {
		t.Fatalf("second tick started=%d catalog=%d, want catalog start count 1", second, len(catalogStarter.requests))
	}
	if len(derivedQueuer.requests) != 2 || derivedQueuer.requests[1].RecoveryPointID != point.ID {
		t.Fatalf("second tick derived=%+v, want retry for point %s", derivedQueuer.requests, point.ID)
	}
	if derivedQueuer.requests[1].CatalogGenerationID != strings.Repeat("a", 32) {
		t.Fatalf("derived retry generation=%s, want first catalog generation", derivedQueuer.requests[1].CatalogGenerationID)
	}
}

type derivedExpectationStub struct {
	descriptors []ExpectedDerivedDescriptor
}

func (stub derivedExpectationStub) ExpectedDescriptors(
	_ context.Context,
	_ DerivedBackfillRequest,
) ([]ExpectedDerivedDescriptor, error) {
	return stub.descriptors, nil
}

func TestReconcileRebuildsQueuesMissingDerivedDescriptorsWithoutNewCatalog(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}, &model.BackupAssetProcessingJob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 18, 40, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "derived missing descriptor", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	point := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32),
		strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	seedCatalogGeneration(t, db, point, string(catalog.GenerationComplete), true, now)
	generationID := strings.Repeat("c", 32)
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: strings.Repeat("p", 32), WorkKey: strings.Repeat("w", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: point.ID, CatalogGenerationID: generationID,
		EntryID: "entry-a", SourceFingerprint: point.SourceFingerprint, Capability: "cap-a",
		CapabilitySchema: "cap.v1", PipelineFingerprint: strings.Repeat("1", 64), OutputProfile: "default",
		SecurityPolicyRevision: "1", PriorityClass: "background", State: "queued", IsCurrent: true,
		QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
		DerivedExpectations: derivedExpectationStub{descriptors: []ExpectedDerivedDescriptor{
			{EntryID: "entry-a", Capability: "cap-a"},
			{EntryID: "entry-b", Capability: "cap-b"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.ReconcileRebuilds(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReconcileRebuilds: %v", err)
	}
	if started != 0 || len(catalogStarter.requests) != 0 {
		t.Fatalf("started=%d catalog=%d, want no new catalog", started, len(catalogStarter.requests))
	}
	if len(derivedQueuer.requests) != 1 || derivedQueuer.requests[0].RecoveryPointID != point.ID ||
		derivedQueuer.requests[0].CatalogGenerationID != generationID {
		t.Fatalf("derived=%+v, want missing-descriptor queue on existing catalog", derivedQueuer.requests)
	}
}

func TestReconcileRebuildsInspectedBudgetContinuesPastCompletePrefix(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 11, 20, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "reconcile rebuild inspected", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	completePoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32),
		strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	laterPoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("3", 32),
		strings.Repeat("e", 64), strings.Repeat("f", 64), now.Add(2*time.Minute), false)
	seedCatalogGeneration(t, db, completePoint, string(catalog.GenerationComplete), true, now)
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ReconcileRebuilds(context.Background(), 1)
	if err != nil {
		t.Fatalf("first ReconcileRebuilds: %v", err)
	}
	if first != 0 || len(catalogStarter.requests) != 0 {
		t.Fatalf("first tick started=%d catalog=%d, want inspected-only complete prefix", first, len(catalogStarter.requests))
	}
	second, err := service.ReconcileRebuilds(context.Background(), 1)
	if err != nil {
		t.Fatalf("second ReconcileRebuilds: %v", err)
	}
	if second != 1 || len(catalogStarter.requests) != 1 || catalogStarter.requests[0].RecoveryPointID != laterPoint.ID {
		t.Fatalf("second tick started=%d catalog=%+v, want later point %s", second, catalogStarter.requests, laterPoint.ID)
	}
}

func TestReconcileRebuildsWalksPastCompletePrefix(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "reconcile rebuild prefix", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	completePoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32),
		strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	laterPoint := seedAcceptedRebuildImport(t, db, repository, strings.Repeat("3", 32),
		strings.Repeat("e", 64), strings.Repeat("f", 64), now.Add(2*time.Minute), false)
	seedCatalogGeneration(t, db, completePoint, string(catalog.GenerationComplete), true, now)
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ReconcileRebuilds(context.Background(), 1)
	if err != nil {
		t.Fatalf("first ReconcileRebuilds: %v", err)
	}
	if first != 0 || len(catalogStarter.requests) != 0 {
		t.Fatalf("first tick started=%d catalog=%d, want inspected-only complete prefix", first, len(catalogStarter.requests))
	}
	started, err := service.ReconcileRebuilds(context.Background(), 1)
	if err != nil {
		t.Fatalf("second ReconcileRebuilds: %v", err)
	}
	if started != 1 {
		t.Fatalf("ReconcileRebuilds started=%d, want 1 later incomplete after complete prefix", started)
	}
	if len(catalogStarter.requests) != 1 || catalogStarter.requests[0].RecoveryPointID != laterPoint.ID {
		t.Fatalf("catalog starts=%+v, want later point %s", catalogStarter.requests, laterPoint.ID)
	}
}

func TestReconcileRebuildsIsWorkerSafeWithoutRebuildPorts(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 10, 30, 0, 0, time.UTC)
	repositoryID := strings.Repeat("7", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "reconcile rebuild ports", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedReadableRebuildBinding(t, db, repository.ID, now)
	seedAcceptedRebuildImport(t, db, repository, strings.Repeat("1", 32),
		strings.Repeat("a", 64), strings.Repeat("b", 64), now, false)
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.ReconcileRebuilds(context.Background(), 10)
	if err != nil {
		t.Fatalf("ReconcileRebuilds without ports err=%v, want worker-safe skip", err)
	}
	if started != 0 {
		t.Fatalf("ReconcileRebuilds without ports started=%d, want 0", started)
	}
}

func seedCatalogGeneration(t *testing.T, db *gorm.DB, point model.RecoveryPoint, state string, active bool, at time.Time) {
	t.Helper()
	idChar := byte('c')
	switch state {
	case string(catalog.GenerationBuilding):
		idChar = 'd'
	case string(catalog.GenerationFailed):
		idChar = 'e'
	}
	generation := model.CatalogGeneration{
		ID: strings.Repeat(string(idChar), 32), RecoveryPointID: point.ID, Generation: 1, State: state,
		IsActive: active, SourceFingerprint: point.SourceFingerprint, StartedAt: at, CreatedAt: at, UpdatedAt: at,
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
}

func pointerTime(value time.Time) *time.Time { return &value }
