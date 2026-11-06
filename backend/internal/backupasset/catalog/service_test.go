package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// TestCatalogServiceProjectStalenessTracksFingerprintNotClock proves the
// Catalog status contract through GetCatalogStatus: for a mutable head only a
// fingerprint mismatch is source drift, an unchanged fingerprint stays Fresh no
// matter how old observed_at is, a mutable head missing source observation
// reports Unknown staleness, and points without an active complete generation
// project accurate non-complete coverage with Unknown staleness while failing
// closed without echoing unknown internal state or error codes.
func TestCatalogServiceProjectStalenessTracksFingerprintNotClock(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	aged := now.Add(-30 * 24 * time.Hour)
	committed := now.Add(-24 * time.Hour)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	var immutablePoint model.RecoveryPoint
	if err := db.First(&immutablePoint, "id = ?", fixture.ownedPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", immutablePoint.ID).Updates(map[string]any{
		"source_fingerprint": strings.Repeat("a", 64),
		"committed_at":       committed,
	}).Error; err != nil {
		t.Fatal(err)
	}

	mutableRepo := model.BackupRepository{
		ID: strings.Repeat("9", 32), ProviderKind: string(backupasset.ProviderRsync), DisplayName: "mutable-status-repo",
		VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&mutableRepo).Error; err != nil {
		t.Fatal(err)
	}
	mutableTask := seedCatalogOwnershipTask(t, db, immutablePoint.ProducingNodeIDSnapshot, "mutable-status-task", now)
	mutableLink := seedCatalogOwnershipLink(t, db, strings.Repeat("a", 32), mutableRepo.ID, mutableTask.ID, nil, now)
	mutableLineage, err := json.Marshal(backupasset.RecoveryPointLineageSummary{ProducingTaskID: &mutableTask.ID})
	if err != nil {
		t.Fatal(err)
	}
	mutableFingerprint := strings.Repeat("c", 64)
	driftedFingerprint := strings.Repeat("d", 64)
	mutablePoint := model.RecoveryPoint{
		ID: strings.Repeat("b", 32), RepositoryID: mutableRepo.ID, ProducingTaskID: &mutableTask.ID,
		ProducingTaskNameSnapshot: mutableLink.TaskNameSnapshot, ProducingNodeIDSnapshot: mutableLink.NodeIDSnapshot,
		ProducingNodeNameSnapshot: mutableLink.NodeNameSnapshot, LineageJSON: string(mutableLineage),
		EncryptedProviderLocator: "SECRET_MUTABLE_PROVIDER_LOCATOR_CANARY",
		Semantics:                string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointObserved),
		ObservedAt: &aged, SourceFingerprint: mutableFingerprint,
		ConsistencyJSON: `{"mode":"mutable"}`, FidelityJSON: `{"strength":"weak"}`,
		CapabilityRevision: 1, CapabilitiesJSON: `{"list":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&mutablePoint).Error; err != nil {
		t.Fatal(err)
	}

	assertStatusNoInternalLeak := func(t *testing.T, status StatusDTO, forbidden ...string) {
		t.Helper()
		payload, marshalErr := json.Marshal(status)
		if marshalErr != nil {
			t.Fatalf("marshal status: %v", marshalErr)
		}
		body := string(payload)
		for _, canary := range append([]string{
			mutableFingerprint, driftedFingerprint, "SECRET_MUTABLE_PROVIDER_LOCATOR_CANARY", "source_fingerprint",
		}, forbidden...) {
			if canary != "" && strings.Contains(body, canary) {
				t.Fatalf("catalog status leaked %q: %s", canary, body)
			}
		}
	}

	noIndexStatus, err := service.GetCatalogStatus(context.Background(), mutablePoint.ID, scope)
	if err != nil {
		t.Fatalf("no-index catalog status error: %v", err)
	}
	if noIndexStatus.Generation != nil || noIndexStatus.LatestBuild != nil ||
		noIndexStatus.Coverage.Status != CoverageUnavailable || noIndexStatus.Coverage.IndexedEntries != 0 ||
		noIndexStatus.Staleness.Status != StalenessUnknown || noIndexStatus.Staleness.ObservedAt != nil ||
		noIndexStatus.Staleness.Reason != nil {
		t.Fatalf("no-index catalog status=%+v, want unavailable/unknown", noIndexStatus)
	}
	assertStatusNoInternalLeak(t, noIndexStatus)

	for index, tc := range []struct {
		state        GenerationState
		errorCode    GenerationErrorCode
		wantCoverage CoverageStatus
	}{
		{state: GenerationBuilding, errorCode: GenerationErrorNone, wantCoverage: CoverageBuilding},
		{state: GenerationSuperseded, errorCode: GenerationErrorNone, wantCoverage: CoverageBuilding},
		{state: GenerationPartial, errorCode: GenerationErrorBuildIncomplete, wantCoverage: CoveragePartial},
		{state: GenerationFailed, errorCode: GenerationErrorSourceChanged, wantCoverage: CoverageFailed},
	} {
		if err := db.Where("recovery_point_id = ?", mutablePoint.ID).Delete(&model.CatalogGeneration{}).Error; err != nil {
			t.Fatal(err)
		}
		var finished *time.Time
		if tc.state != GenerationBuilding {
			finishedAt := now.Add(time.Duration(index+1) * time.Minute)
			finished = &finishedAt
		}
		row := model.CatalogGeneration{
			ID: fmt.Sprintf("%032x", 900+index), RecoveryPointID: mutablePoint.ID, Generation: index + 1,
			State: string(tc.state), IsActive: false, SourceFingerprint: mutableFingerprint,
			ExpectedEntryCount: 5, WrittenEntryCount: 2, ExpectedDigest: strings.Repeat("e", 64),
			WrittenDigest: strings.Repeat("f", 64), ErrorCode: string(tc.errorCode),
			StartedAt: now, FinishedAt: finished, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		status, statusErr := service.GetCatalogStatus(context.Background(), mutablePoint.ID, scope)
		if statusErr != nil {
			t.Fatalf("non-active %s catalog status error: %v", tc.state, statusErr)
		}
		if status.Generation != nil || status.LatestBuild == nil || status.LatestBuild.State != tc.state ||
			status.Coverage.Status != tc.wantCoverage || status.Coverage.IndexedEntries != 2 ||
			status.Staleness.Status != StalenessUnknown || status.Staleness.ObservedAt != nil || status.Staleness.Reason != nil {
			t.Fatalf("non-active %s catalog status=%+v, want coverage=%s staleness=unknown", tc.state, status, tc.wantCoverage)
		}
		assertStatusNoInternalLeak(t, status)
	}

	for index, bad := range []struct {
		name      string
		active    bool
		state     string
		errorCode string
		canary    string
	}{
		{name: "unknown_state", active: false, state: "SECRET_FUTURE_GENERATION_STATE", errorCode: "", canary: "SECRET_FUTURE_GENERATION_STATE"},
		{name: "unknown_error_code", active: false, state: string(GenerationFailed), errorCode: "SECRET_PROVIDER_RAW_ERROR_CODE", canary: "SECRET_PROVIDER_RAW_ERROR_CODE"},
		{name: "active_non_complete", active: true, state: string(GenerationPartial), errorCode: string(GenerationErrorBuildIncomplete)},
	} {
		if err := db.Where("recovery_point_id = ?", mutablePoint.ID).Delete(&model.CatalogGeneration{}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
			t.Fatal(err)
		}
		row := model.CatalogGeneration{
			ID: fmt.Sprintf("%032x", 950+index), RecoveryPointID: mutablePoint.ID, Generation: index + 1,
			State: bad.state, IsActive: bad.active, SourceFingerprint: mutableFingerprint,
			ExpectedEntryCount: 1, WrittenEntryCount: 1, ExpectedDigest: strings.Repeat("e", 64),
			WrittenDigest: strings.Repeat("f", 64), ErrorCode: bad.errorCode,
			StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		createErr := db.Create(&row).Error
		_ = db.Exec("PRAGMA ignore_check_constraints = OFF").Error
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, statusErr := service.GetCatalogStatus(context.Background(), mutablePoint.ID, scope)
		if !errors.Is(statusErr, ErrUnknownInternalState) {
			t.Fatalf("%s error=%v, want ErrUnknownInternalState", bad.name, statusErr)
		}
		if bad.canary != "" && strings.Contains(statusErr.Error(), bad.canary) {
			t.Fatalf("%s error echoed canary %q: %v", bad.name, bad.canary, statusErr)
		}
	}

	if err := db.Where("recovery_point_id = ?", mutablePoint.ID).Delete(&model.CatalogGeneration{}).Error; err != nil {
		t.Fatal(err)
	}
	activeMutableGen := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: mutablePoint.ID, Generation: 10,
		State: string(GenerationComplete), IsActive: true, SourceFingerprint: mutableFingerprint,
		ExpectedEntryCount: 3, WrittenEntryCount: 3, ExpectedDigest: strings.Repeat("e", 64),
		WrittenDigest: strings.Repeat("f", 64), StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&activeMutableGen).Error; err != nil {
		t.Fatal(err)
	}

	freshStatus, err := service.GetCatalogStatus(context.Background(), mutablePoint.ID, scope)
	if err != nil {
		t.Fatalf("aged matching mutable catalog status error: %v", err)
	}
	if freshStatus.Coverage.Status != CoverageComplete || freshStatus.Staleness.Status != StalenessFresh ||
		freshStatus.Staleness.Reason != nil || freshStatus.Staleness.ObservedAt == nil ||
		!freshStatus.Staleness.ObservedAt.Equal(aged.UTC()) {
		t.Fatalf("aged matching mutable status=%+v, want complete/fresh at %s", freshStatus, aged.UTC())
	}
	assertStatusNoInternalLeak(t, freshStatus)

	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", mutablePoint.ID).
		Update("source_fingerprint", driftedFingerprint).Error; err != nil {
		t.Fatal(err)
	}
	driftedStatus, err := service.GetCatalogStatus(context.Background(), mutablePoint.ID, scope)
	if err != nil {
		t.Fatalf("drifted mutable catalog status error: %v", err)
	}
	if driftedStatus.Coverage.Status != CoverageComplete || driftedStatus.Staleness.Status != StalenessStale ||
		driftedStatus.Staleness.Reason == nil || driftedStatus.Staleness.Reason.Code != backupasset.CapabilityMutableSourceChanged ||
		driftedStatus.Staleness.ObservedAt == nil || !driftedStatus.Staleness.ObservedAt.Equal(aged.UTC()) {
		t.Fatalf("drifted mutable status=%+v, want complete/stale mutable_source_changed", driftedStatus)
	}
	assertStatusNoInternalLeak(t, driftedStatus)

	for _, missingObservedAt := range []any{nil, time.Time{}} {
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", mutablePoint.ID).Updates(map[string]any{
			"source_fingerprint": mutableFingerprint,
			"observed_at":        missingObservedAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
		unobservedStatus, unobservedErr := service.GetCatalogStatus(context.Background(), mutablePoint.ID, scope)
		if unobservedErr != nil {
			t.Fatalf("unobserved mutable (%v) catalog status error: %v", missingObservedAt, unobservedErr)
		}
		if unobservedStatus.Coverage.Status != CoverageComplete || unobservedStatus.Staleness.Status != StalenessUnknown ||
			unobservedStatus.Staleness.ObservedAt != nil || unobservedStatus.Staleness.Reason != nil {
			t.Fatalf("unobserved mutable (%v) status=%+v, want complete/unknown", missingObservedAt, unobservedStatus)
		}
		assertStatusNoInternalLeak(t, unobservedStatus)
	}

	seedCatalogServiceGeneration(t, db, immutablePoint.ID, 1, true, GenerationComplete, now)
	immutableStatus, err := service.GetCatalogStatus(context.Background(), immutablePoint.ID, scope)
	if err != nil {
		t.Fatalf("immutable catalog status error: %v", err)
	}
	if immutableStatus.Coverage.Status != CoverageComplete || immutableStatus.Staleness.Status != StalenessFresh ||
		immutableStatus.Staleness.Reason != nil || immutableStatus.Staleness.ObservedAt == nil ||
		!immutableStatus.Staleness.ObservedAt.Equal(committed.UTC()) {
		t.Fatalf("immutable status=%+v, want complete/fresh at %s", immutableStatus, committed.UTC())
	}
	assertStatusNoInternalLeak(t, immutableStatus, strings.Repeat("a", 64))
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

func TestCatalogServiceEntryPageCarriesValidatedDirectoryContextAcrossEmptyAndCursorPages(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	generation := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	parent := seedCatalogServiceEntry(t, db, generation, strings.Repeat("a", 64), nil, "parent", "parent", backupasset.CatalogEntryDirectory, 0, now)
	current := seedCatalogServiceEntry(t, db, generation, strings.Repeat("b", 64), &parent.EntryID, "parent/current", "current", backupasset.CatalogEntryDirectory, 0, now)
	for index, name := range []string{"one.txt", "two.txt"} {
		seedCatalogServiceEntry(t, db, generation, strings.Repeat(string(rune('c'+index)), 64), &current.EntryID, "parent/current/"+name, name, backupasset.CatalogEntryFile, 1, now)
	}
	empty := seedCatalogServiceEntry(t, db, generation, strings.Repeat("e", 64), nil, "empty", "empty", backupasset.CatalogEntryDirectory, 0, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	root, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{Limit: 10, Sort: EntrySortNameAsc})
	if err != nil {
		t.Fatal(err)
	}
	if root.Directory.Current != nil || root.Directory.Parent != nil || len(root.Directory.Breadcrumb) != 0 {
		t.Fatalf("root directory context=%+v", root.Directory)
	}

	first, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Limit: 1, Sort: EntrySortNameAsc,
	})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Limit: 1, Sort: EntrySortNameAsc, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := DirectoryContextDTO{
		Current: &DirectoryEntryDTO{RecoveryPointID: fixture.ownedPointID, EntryID: current.EntryID, Name: current.Name},
		Parent:  &DirectoryRefDTO{RecoveryPointID: fixture.ownedPointID, EntryID: parent.EntryID},
		Breadcrumb: []BreadcrumbDTO{
			{RecoveryPointID: fixture.ownedPointID, EntryID: parent.EntryID, Name: parent.Name},
			{RecoveryPointID: fixture.ownedPointID, EntryID: current.EntryID, Name: current.Name},
		},
	}
	if !reflect.DeepEqual(first.Directory, want) || !reflect.DeepEqual(second.Directory, want) {
		t.Fatalf("directory context changed across cursor pages: first=%+v second=%+v want=%+v", first.Directory, second.Directory, want)
	}
	if err := db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?", generation.ID, fixture.ownedPointID, current.EntryID).
		Update("name", "renamed-current").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Limit: 1, Sort: EntrySortNameAsc, Cursor: first.NextCursor,
	}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("directory-context drift cursor error=%v", err)
	}

	emptyPage, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: empty.EntryID, Limit: 10, Sort: EntrySortNameAsc,
	})
	if err != nil || len(emptyPage.Items) != 0 || emptyPage.Directory.Current == nil || emptyPage.Directory.Current.EntryID != empty.EntryID ||
		emptyPage.Directory.Parent != nil || len(emptyPage.Directory.Breadcrumb) != 1 {
		t.Fatalf("empty directory page=%+v err=%v", emptyPage, err)
	}
}

func TestCatalogServiceEntryCursorRejectsGenerationReplayBeforeMissingDirectoryLookup(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 28, 9, 10, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	generation := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	current := seedCatalogServiceEntry(t, db, generation, strings.Repeat("a", 64), nil,
		"current", "current", backupasset.CatalogEntryDirectory, 0, now)
	for index, name := range []string{"one.txt", "two.txt"} {
		seedCatalogServiceEntry(t, db, generation, strings.Repeat(string(rune('b'+index)), 64), &current.EntryID,
			"current/"+name, name, backupasset.CatalogEntryFile, 1, now)
	}
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	first, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Limit: 1, Sort: EntrySortNameAsc,
	})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first directory page=%+v err=%v", first, err)
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
		ParentEntryID: current.EntryID, Limit: 1, Sort: EntrySortNameAsc, Cursor: first.NextCursor,
	}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("non-root generation replay error=%v", err)
	}
	tampered := first.NextCursor + "x"
	if _, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Limit: 1, Sort: EntrySortNameAsc, Cursor: tampered,
	}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered non-root cursor error=%v", err)
	}
}

func TestCatalogServiceEntryPageRejectsMalformedDirectoryAncestry(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 28, 9, 15, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	generation := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	ancestor := seedCatalogServiceEntry(t, db, generation, strings.Repeat("a", 64), nil, "ancestor", "ancestor", backupasset.CatalogEntryDirectory, 0, now)
	current := seedCatalogServiceEntry(t, db, generation, strings.Repeat("b", 64), &ancestor.EntryID, "ancestor/current", "current", backupasset.CatalogEntryDirectory, 0, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	if err := db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?", generation.ID, fixture.ownedPointID, ancestor.EntryID).
		Update("entry_type", backupasset.CatalogEntryFile).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Sort: EntrySortNameAsc,
	}); !errors.Is(err, ErrInvalidCatalogContract) {
		t.Fatalf("non-directory ancestor error=%v", err)
	}
	if err := db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?", generation.ID, fixture.ownedPointID, ancestor.EntryID).
		Updates(map[string]any{"entry_type": backupasset.CatalogEntryDirectory, "parent_entry_id": current.EntryID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Sort: EntrySortNameAsc,
	}); !errors.Is(err, ErrInvalidCatalogContract) {
		t.Fatalf("cyclic ancestor error=%v", err)
	}
}

func TestCatalogServiceEntryPagePreservesCurrentDirectoryStorageFailure(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 28, 9, 20, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	generation := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	current := seedCatalogServiceEntry(t, db, generation, strings.Repeat("a", 64), nil,
		"current", "current", backupasset.CatalogEntryDirectory, 0, now)
	service := newCatalogServiceForTest(t, db, now)
	if err := db.Migrator().DropTable(&model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}

	_, err := service.ListEntries(context.Background(), fixture.ownedPointID,
		AuthorizationScope{Role: "operator", UserID: fixture.operatorID},
		EntryListRequest{ParentEntryID: current.EntryID, Sort: EntrySortNameAsc})
	if err == nil || errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("directory storage failure must remain internal, got %v", err)
	}
}

func TestCatalogServiceEntryPageAcceptsExactly256AncestorsAndRejects257(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 28, 9, 30, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	generation := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	var parentID *string
	var current model.CatalogEntry
	for index := 0; index < maxBreadcrumbDepth; index++ {
		entryID := fmt.Sprintf("%064x", index+1)
		current = seedCatalogServiceEntry(t, db, generation, entryID, parentID,
			fmt.Sprintf("directory-%d", index), fmt.Sprintf("directory-%d", index),
			backupasset.CatalogEntryDirectory, 0, now)
		parentID = &current.EntryID
	}
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	page, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: current.EntryID, Sort: EntrySortNameAsc,
	})
	if err != nil || len(page.Directory.Breadcrumb) != maxBreadcrumbDepth {
		t.Fatalf("256-level directory context=%+v err=%v", page.Directory, err)
	}

	tooDeep := seedCatalogServiceEntry(t, db, generation, strings.Repeat("f", 64), &current.EntryID,
		"too-deep", "too-deep", backupasset.CatalogEntryDirectory, 0, now)
	if _, err := service.ListEntries(context.Background(), fixture.ownedPointID, scope, EntryListRequest{
		ParentEntryID: tooDeep.EntryID, Sort: EntrySortNameAsc,
	}); !errors.Is(err, ErrInvalidCatalogContract) {
		t.Fatalf("257-level directory error=%v", err)
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
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity); err != nil {
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
