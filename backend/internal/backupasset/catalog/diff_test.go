package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestCatalogDiffIsExactCompositePagedAndOfflineMetadataOnly(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	baseGeneration := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	compareGeneration := seedCatalogServiceGeneration(t, db, fixture.unlinkedPointID, 1, true, GenerationComplete, now)
	baseParent := seedCatalogServiceEntry(t, db, baseGeneration, strings.Repeat("1", 64), nil, "base-root", "base-root", backupasset.CatalogEntryDirectory, 0, now)
	compareParent := seedCatalogServiceEntry(t, db, compareGeneration, strings.Repeat("2", 64), nil, "compare-root", "compare-root", backupasset.CatalogEntryDirectory, 0, now)
	baseUnchanged := seedCatalogServiceEntry(t, db, baseGeneration, strings.Repeat("3", 64), &baseParent.EntryID, "base-root/a.txt", "a.txt", backupasset.CatalogEntryFile, 1, now)
	compareUnchanged := seedCatalogServiceEntry(t, db, compareGeneration, strings.Repeat("4", 64), &compareParent.EntryID, "compare-root/a.txt", "a.txt", backupasset.CatalogEntryFile, 1, now)
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, compareUnchanged.EntryID).
		Update("fingerprint", baseUnchanged.Fingerprint).Error; err != nil {
		t.Fatal(err)
	}
	removed := seedCatalogServiceEntry(t, db, baseGeneration, strings.Repeat("5", 64), &baseParent.EntryID, "base-root/b.txt", "b.txt", backupasset.CatalogEntryFile, 2, now)
	added := seedCatalogServiceEntry(t, db, compareGeneration, strings.Repeat("6", 64), &compareParent.EntryID, "compare-root/c.txt", "c.txt", backupasset.CatalogEntryFile, 3, now)
	baseModified := seedCatalogServiceEntry(t, db, baseGeneration, strings.Repeat("7", 64), &baseParent.EntryID, "base-root/d.txt", "d.txt", backupasset.CatalogEntryFile, 4, now)
	compareModified := seedCatalogServiceEntry(t, db, compareGeneration, strings.Repeat("8", 64), &compareParent.EntryID, "compare-root/d.txt", "d.txt", backupasset.CatalogEntryFile, 40, now)
	baseType := seedCatalogServiceEntry(t, db, baseGeneration, strings.Repeat("9", 64), &baseParent.EntryID, "base-root/e", "e", backupasset.CatalogEntryFile, 0, now)
	compareType := seedCatalogServiceEntry(t, db, compareGeneration, strings.Repeat("a", 64), &compareParent.EntryID, "compare-root/e", "e", backupasset.CatalogEntryDirectory, 0, now)
	if err := db.Model(&model.BackupRepository{}).Where("id = ?", strings.Repeat("1", 32)).
		Update("status", backupasset.RepositoryOffline).Error; err != nil {
		t.Fatal(err)
	}
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	request := DiffRequest{
		BaseRecoveryPointID: fixture.ownedPointID, CompareRecoveryPointID: fixture.unlinkedPointID,
		BaseParentEntryID: baseParent.EntryID, CompareParentEntryID: compareParent.EntryID,
		Sort: DiffSortPathAsc, Limit: 2,
	}
	first, err := service.Diff(context.Background(), scope, request)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first diff page=%+v err=%v", first, err)
	}
	request.Cursor = first.NextCursor
	second, err := service.Diff(context.Background(), scope, request)
	if err != nil || len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second diff page=%+v err=%v", second, err)
	}
	all := append(append([]DiffItemDTO{}, first.Items...), second.Items...)
	if len(all) != 4 {
		t.Fatalf("diff items=%+v", all)
	}
	wantKinds := []DiffChangeKind{DiffRemoved, DiffAdded, DiffModified, DiffTypeChanged}
	for index, item := range all {
		if item.Kind != wantKinds[index] {
			t.Fatalf("diff[%d] kind=%q want=%q items=%+v", index, item.Kind, wantKinds[index], all)
		}
	}
	if all[0].Base == nil || all[0].Base.EntryID != removed.EntryID || all[0].Compare != nil ||
		all[1].Compare == nil || all[1].Compare.EntryID != added.EntryID || all[1].Base != nil ||
		all[2].Base == nil || all[2].Compare == nil || all[2].Base.EntryID != baseModified.EntryID || all[2].Compare.EntryID != compareModified.EntryID ||
		all[3].Base == nil || all[3].Compare == nil || all[3].Base.EntryID != baseType.EntryID || all[3].Compare.EntryID != compareType.EntryID {
		t.Fatalf("diff composite sides=%+v", all)
	}
	for _, item := range all {
		if item.Base != nil && item.Base.RecoveryPointID != fixture.ownedPointID {
			t.Fatalf("base composite scope=%+v", item.Base)
		}
		if item.Compare != nil && item.Compare.RecoveryPointID != fixture.unlinkedPointID {
			t.Fatalf("compare composite scope=%+v", item.Compare)
		}
	}
	if first.ProviderEvidence.Status != ProviderDiffUnavailable || first.ProviderEvidence.Reason == nil ||
		first.ProviderEvidence.Reason.Code != backupasset.CapabilityRepositoryOffline {
		t.Fatalf("offline provider evidence=%+v", first.ProviderEvidence)
	}
	payload, err := json.Marshal(append(first.Items, second.Items...))
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	for _, forbidden := range []string{
		"base-root/", "compare-root/", baseUnchanged.EntryID, compareUnchanged.EntryID,
		baseModified.Fingerprint, compareModified.Fingerprint, "normalized_path", "provider_locator",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("diff leaked %q: %s", forbidden, body)
		}
	}
}

func TestCatalogDiffRejectsUnownedSamePointAndStaleGenerationCursor(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	baseGeneration := seedCatalogServiceGeneration(t, db, fixture.ownedPointID, 1, true, GenerationComplete, now)
	compareGeneration := seedCatalogServiceGeneration(t, db, fixture.unlinkedPointID, 1, true, GenerationComplete, now)
	seedCatalogServiceEntry(t, db, baseGeneration, strings.Repeat("b", 64), nil, "a", "a", backupasset.CatalogEntryFile, 1, now)
	seedCatalogServiceEntry(t, db, compareGeneration, strings.Repeat("c", 64), nil, "b", "b", backupasset.CatalogEntryFile, 1, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	if _, err := service.Diff(context.Background(), scope, DiffRequest{
		BaseRecoveryPointID: fixture.ownedPointID, CompareRecoveryPointID: fixture.ownedPointID, Sort: DiffSortPathAsc,
	}); !errors.Is(err, ErrInvalidCatalogContract) {
		t.Fatalf("same-point diff error=%v", err)
	}
	if _, err := service.Diff(context.Background(), scope, DiffRequest{
		BaseRecoveryPointID: fixture.ownedPointID, CompareRecoveryPointID: fixture.unownedPointID, Sort: DiffSortPathAsc,
	}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("unowned compare diff error=%v", err)
	}
	page, err := service.Diff(context.Background(), scope, DiffRequest{
		BaseRecoveryPointID: fixture.ownedPointID, CompareRecoveryPointID: fixture.unlinkedPointID,
		Sort: DiffSortPathAsc, Limit: 1,
	})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("diff cursor page=%+v err=%v", page, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.CatalogGeneration{}).Where("id = ?", compareGeneration.ID).
			Updates(map[string]any{"state": GenerationSuperseded, "is_active": false}).Error; err != nil {
			return err
		}
		seedCatalogServiceGeneration(t, tx, fixture.unlinkedPointID, 2, true, GenerationComplete, now.Add(time.Minute))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Diff(context.Background(), scope, DiffRequest{
		BaseRecoveryPointID: fixture.ownedPointID, CompareRecoveryPointID: fixture.unlinkedPointID,
		Sort: DiffSortPathAsc, Limit: 1, Cursor: page.NextCursor,
	}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("stale diff cursor error=%v", err)
	}
}
