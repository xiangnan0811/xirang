package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestCatalogServiceFiltersBeforePaginationAndBrowsesOfflineCommittedCatalog(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	if err := db.Model(&model.BackupRepository{}).Where("id = ?", strings.Repeat("1", 32)).
		Update("status", backupasset.RepositoryOffline).Error; err != nil {
		t.Fatal(err)
	}
	ownedGeneration := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	unlinkedGeneration := seedCatalogServiceGeneration(t, db, fixture.unlinkedPointID, 1, true, GenerationComplete, now)
	partialGeneration := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 2, false, GenerationPartial, now.Add(time.Minute))
	failedGeneration := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 3, false, GenerationFailed, now.Add(2*time.Minute))
	ownedDirectory := seedCatalogServiceEntry(t, db, ownedGeneration, strings.Repeat("a", 64), nil, "docs", "docs", backupasset.CatalogEntryDirectory, 0, now)
	ownedFile := seedCatalogServiceEntry(t, db, ownedGeneration, strings.Repeat("b", 64), &ownedDirectory.EntryID, "docs/report.txt", "report.txt", backupasset.CatalogEntryFile, 42, now)
	otherFile := seedCatalogServiceEntry(t, db, unlinkedGeneration, strings.Repeat("c", 64), nil, "other.txt", "other.txt", backupasset.CatalogEntryFile, 7, now)
	seedCatalogServiceEntry(t, db, partialGeneration, strings.Repeat("d", 64), nil, "must-not-leak.txt", "must-not-leak.txt", backupasset.CatalogEntryFile, 99, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	operatorSummary, err := service.RepositorySummary(context.Background(), strings.Repeat("1", 32), scope)
	if err != nil || operatorSummary.RecoveryPointCount != 2 {
		t.Fatalf("operator repository summary=%+v err=%v", operatorSummary, err)
	}
	adminSummary, err := service.RepositorySummary(context.Background(), strings.Repeat("1", 32), AuthorizationScope{Role: "admin", UserID: 1})
	if err != nil || adminSummary.RecoveryPointCount != 8 {
		t.Fatalf("admin repository summary=%+v err=%v", adminSummary, err)
	}

	first, err := service.ListRecoveryPoints(context.Background(), strings.Repeat("1", 32), scope, RecoveryPointListRequest{
		Limit: 1, Sort: RecoveryPointSortCapturedDesc,
	})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != fixture.unlinkedPointID || first.NextCursor == "" {
		t.Fatalf("first visible page=%+v err=%v", first, err)
	}
	second, err := service.ListRecoveryPoints(context.Background(), strings.Repeat("1", 32), scope, RecoveryPointListRequest{
		Limit: 1, Sort: RecoveryPointSortCapturedDesc, Cursor: first.NextCursor,
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != fixture.ownedPointID || second.NextCursor != "" {
		t.Fatalf("second visible page=%+v err=%v", second, err)
	}
	for _, page := range []RecoveryPointPage{first, second} {
		payload, marshalErr := json.Marshal(page)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		body := string(payload)
		for _, forbidden := range []string{
			fixture.unownedPointID, fixture.malformedPointID, fixture.conflictingPointID,
			fixture.importedPointID, fixture.unattributedPointID, fixture.archivedPointID,
			"must-not-leak", "lineage_json", "provider_locator", "source_fingerprint",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("visible page leaked %q: %s", forbidden, body)
			}
		}
	}

	status, err := service.GetCatalogStatus(context.Background(), fixture.ownedPointID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation == nil || status.Generation.ID != ownedGeneration.ID || status.Coverage.Status != CoverageComplete ||
		status.ContentAvailability.Available || status.ContentAvailability.Reason == nil ||
		status.ContentAvailability.Reason.Code != backupasset.CapabilityRepositoryOffline || status.LatestBuild == nil ||
		status.LatestBuild.ID != failedGeneration.ID || status.LatestBuild.State != GenerationFailed {
		t.Fatalf("offline orthogonal status=%+v", status)
	}
	entries, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		Limit: 10, Sort: EntrySortNameAsc,
	})
	if err != nil || len(entries.Items) != 1 || entries.Items[0].RecoveryPointID != fixture.ownedPointID ||
		entries.Items[0].EntryID != ownedDirectory.EntryID || entries.Items[0].ParentEntryID != nil {
		t.Fatalf("root entries=%+v err=%v", entries, err)
	}
	detail, err := service.GetEntry(context.Background(), fixture.ownedPointID, ownedFile.EntryID, scope)
	if err != nil || detail.RecoveryPointID != fixture.ownedPointID || len(detail.Breadcrumb) != 1 ||
		detail.Breadcrumb[0].EntryID != ownedDirectory.EntryID {
		t.Fatalf("entry detail=%+v err=%v", detail, err)
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"docs/report.txt", strings.Repeat("f", 64), "provider_locator", "normalized_path"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("entry detail leaked %q: %s", forbidden, payload)
		}
	}
	if _, err := service.GetEntry(context.Background(), fixture.ownedPointID, otherFile.EntryID, scope); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("cross-point entry replay error=%v", err)
	}
	if _, err := service.GetEntry(context.Background(), fixture.unownedPointID, ownedFile.EntryID, scope); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("unowned point entry error=%v", err)
	}
}

func TestCatalogGenerationDTORejectsUnknownErrorCodeWithoutEcho(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	finished := now.Add(time.Minute)
	_, err := generationDTO(model.CatalogGeneration{
		ID:            strings.Repeat("a", 32),
		Generation:    1,
		State:         string(GenerationFailed),
		StartedAt:     now,
		FinishedAt:    &finished,
		ErrorCode:     "provider_raw_/secret/path",
		CorrelationID: "safe-correlation",
	})
	if !errors.Is(err, ErrUnknownInternalState) {
		t.Fatalf("unknown generation error code must fail closed, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "provider_raw_/secret/path") {
		t.Fatalf("error echoed the unknown raw value: %v", err)
	}
}

func TestCatalogServiceEntryCursorBindsActiveGenerationAndStableBinaryOrder(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	generation := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	for index, name := range []string{"Zeta", "alpha", "Äther"} {
		seedCatalogServiceEntry(t, db, generation, strings.Repeat(string(rune('a'+index)), 64), nil, name, name, backupasset.CatalogEntryFile, int64(index), now)
	}
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	first, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{Limit: 1, Sort: EntrySortNameAsc})
	if err != nil || len(first.Items) != 1 || first.Items[0].Name != "Zeta" || first.NextCursor == "" {
		t.Fatalf("first entry page=%+v err=%v", first, err)
	}
	second, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		Limit: 1, Sort: EntrySortNameAsc, Cursor: first.NextCursor,
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].Name != "alpha" {
		t.Fatalf("second entry page=%+v err=%v", second, err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.CatalogGeneration{}).Where("id = ?", generation.ID).
			Updates(map[string]any{"state": GenerationSuperseded, "is_active": false}).Error; err != nil {
			return err
		}
		newGeneration := model.CatalogGeneration{
			ID: strings.Repeat("e", 32), RecoveryPointID: fixture.ownedPointID, Generation: 2,
			State: string(GenerationComplete), IsActive: true, SourceFingerprint: fmt.Sprintf("%064x", 10),
			WrittenDigest: strings.Repeat("e", 64), StartedAt: now.Add(time.Minute), FinishedAt: timePointer(now.Add(time.Minute)),
			CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
		}
		return tx.Create(&newGeneration).Error
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		Limit: 1, Sort: EntrySortNameAsc, Cursor: first.NextCursor,
	}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("generation-drift cursor error=%v", err)
	}
}

func TestCatalogServiceFeatureGateFailsBeforeResourceLookup(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC)
	ownership, err := NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	service, err := NewService(ServiceDependencies{
		DB: db, Ownership: ownership, Cursor: NewCursorCodec(keyring, func() time.Time { return now }, 15*time.Minute),
		Now: func() time.Time { return now }, ReconcileInterval: 5 * time.Minute,
		FeatureEnabled: func() (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetRecoveryPoint(context.Background(), strings.Repeat("f", 32), AuthorizationScope{Role: "admin", UserID: 1}); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("feature-disabled Catalog error=%v", err)
	}
}

func newCatalogServiceForTest(t *testing.T, db *gorm.DB, now time.Time) *Service {
	t.Helper()
	ownership, err := NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceDependencies{
		DB: db, Ownership: ownership, Cursor: NewCursorCodec(keyring, func() time.Time { return now }, 15*time.Minute),
		Now: func() time.Time { return now }, ReconcileInterval: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func seedCatalogServiceGeneration(
	t *testing.T,
	db *gorm.DB,
	recoveryPointID string,
	sequence int,
	active bool,
	state GenerationState,
	now time.Time,
) model.CatalogGeneration {
	t.Helper()
	finished := now
	generation := model.CatalogGeneration{
		ID: fmt.Sprintf("%032x", int(recoveryPointID[len(recoveryPointID)-1])+sequence*1000), RecoveryPointID: recoveryPointID,
		Generation: sequence, State: string(state), IsActive: active, SourceFingerprint: fmt.Sprintf("%064x", sequence),
		ExpectedEntryCount: 2, WrittenEntryCount: 2, ExpectedDigest: strings.Repeat("b", 64),
		WrittenDigest: strings.Repeat("c", 64), StartedAt: now, FinishedAt: &finished, CreatedAt: now, UpdatedAt: now,
	}
	if state != GenerationComplete {
		generation.ErrorCode = "catalog_build_incomplete"
		generation.WrittenEntryCount = 1
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestCatalogPaginatesTenThousandCommittedEntries(t *testing.T) {
	const total = 10000
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	generation := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	entries := make([]model.CatalogEntry, 0, total)
	modified := now.Add(-time.Minute)
	for i := 0; i < total; i++ {
		entries = append(entries, model.CatalogEntry{
			GenerationID: generation.ID, EntryID: fmt.Sprintf("%064x", i), RecoveryPointID: generation.RecoveryPointID,
			Name: fmt.Sprintf("file-%05d.txt", i), NormalizedPath: fmt.Sprintf("file-%05d.txt", i),
			EntryType: string(backupasset.CatalogEntryFile), Size: 1,
			ModifiedAt: &modified, Mode: "0640", Owner: "backup", MimeType: "text/plain",
			Fingerprint: strings.Repeat("f", 64), FingerprintStrength: string(FingerprintStrong),
			EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", SecurityState: "sealed",
			CreatedAt: now,
		})
	}
	if err := db.CreateInBatches(entries, 500).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", generation.ID).Updates(map[string]any{
		"expected_entry_count": total, "written_entry_count": total,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := newCatalogServiceForTest(t, db, now)
	seen := 0
	cursor := ""
	for pageNum := 0; pageNum < 80; pageNum++ {
		page, err := service.ListEntries(context.Background(), fixture.ownedPointID, AuthorizationScope{Role: "admin", UserID: 1}, EntryListRequest{
			Limit: 200, Sort: EntrySortNameAsc, Cursor: cursor,
		})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(page.Items)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if seen != total {
		t.Fatalf("paged %d entries, want %d", seen, total)
	}
}

func TestCatalogListEntryVersionsUsesLineageAndOmitsPaths(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	ownedGeneration := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	unlinkedGeneration := seedCatalogServiceGeneration(t, db, fixture.unlinkedPointID, 1, true, GenerationComplete, now)
	ownedFile := seedCatalogServiceEntry(t, db, ownedGeneration, strings.Repeat("b", 64), nil, "docs/report.txt", "report.txt", backupasset.CatalogEntryFile, 42, now)
	seedCatalogServiceEntry(t, db, unlinkedGeneration, strings.Repeat("c", 64), nil, "docs/report.txt", "report.txt", backupasset.CatalogEntryFile, 7, now)

	var owned model.RecoveryPoint
	if err := db.Where("id = ?", fixture.ownedPointID).First(&owned).Error; err != nil {
		t.Fatal(err)
	}
	newerNow := now.Add(2 * time.Hour)
	newerRun := seedCatalogOwnershipRun(t, db, *owned.ProducingTaskID, newerNow)
	ownedLineage, err := backupasset.DecodePublicationLineage(owned.LineageJSON)
	if err != nil {
		t.Fatal(err)
	}
	newerPoint := owned
	newerPoint.ID = fmt.Sprintf("%032x", 18)
	newerPoint.ProducingTaskRunID = &newerRun.ID
	newerPoint.LineageJSON = catalogOwnershipLineage(t, *owned.ProducingTaskID, newerRun.ID, ownedLineage.TaskRepositoryLinkID, newerNow)
	newerPoint.CapturedAt = &newerNow
	newerPoint.CommittedAt = &newerNow
	newerPoint.SourceFingerprint = fmt.Sprintf("%064x", 18)
	newerPoint.CreatedAt = newerNow
	newerPoint.UpdatedAt = newerNow
	if err := db.Create(&newerPoint).Error; err != nil {
		t.Fatal(err)
	}
	newerGeneration := seedCatalogServiceGeneration(t, db, newerPoint.ID, 1, true, GenerationComplete, newerNow)
	newerFile := seedCatalogServiceEntry(t, db, newerGeneration, strings.Repeat("e", 64), nil, "docs/report.txt", "report.txt", backupasset.CatalogEntryFile, 50, newerNow)

	service := newCatalogServiceForTest(t, db, now)
	page, err := service.ListEntryVersions(context.Background(), fixture.ownedPointID, ownedFile.EntryID, AuthorizationScope{Role: "operator", UserID: fixture.operatorID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].EntryID != newerFile.EntryID || page.Items[1].EntryID != ownedFile.EntryID {
		t.Fatalf("versions=%+v", page.Items)
	}
	payload, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	for _, forbidden := range []string{"docs/report.txt", "normalized_path", "provider_locator", fixture.unlinkedPointID} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("versions leaked %q: %s", forbidden, body)
		}
	}
}

func seedCatalogServiceEntry(
	t *testing.T,
	db *gorm.DB,
	generation model.CatalogGeneration,
	entryID string,
	parentID *string,
	normalizedPath, name string,
	entryType backupasset.CatalogEntryType,
	size int64,
	now time.Time,
) model.CatalogEntry {
	t.Helper()
	modified := now.Add(-time.Minute)
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: entryID, RecoveryPointID: generation.RecoveryPointID,
		ParentEntryID: parentID, NormalizedPath: normalizedPath, Name: name, EntryType: string(entryType), Size: size,
		ModifiedAt: &modified, Mode: "0640", Owner: "backup", MimeType: "application/octet-stream",
		Fingerprint: strings.Repeat("f", 64), FingerprintStrength: string(FingerprintStrong),
		EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", SecurityState: "sealed", CreatedAt: now,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return entry
}

func timePointer(value time.Time) *time.Time { return &value }
