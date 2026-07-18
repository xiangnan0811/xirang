package overlay

import (
	"context"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
)

func TestLifecycleBreaksExactTombstonesOpaqueOverlaysAndDeletesRecent(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	audit := &overlayAuditSpy{}
	actor := Actor{UserID: 701, Role: "operator"}
	pointID := strings.Repeat("c", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("c", 64)}
	harness.points[pointID] = true
	harness.assets[ref] = true
	saved, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: savedQuery(pointID, "lifecycle"), IdempotencyKey: "lifecycle-saved-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	favorite, _ := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{Ref: ref, Label: "keep me", IdempotencyKey: "lifecycle-fav-0001"})
	tag, _ := service.CreateTag(context.Background(), actor.UserID, "keep-tag", "lifecycle-tag-0001")
	assignment, _ := service.AssignTag(context.Background(), actor, tag.ID, ref, "lifecycle-assign01")
	recent, _ := service.RecordRecent(context.Background(), actor, ref)
	service.audit = audit

	result, err := service.ReconcileSource(context.Background(), SourceLifecycle{
		RecoveryPointID: pointID, Reason: SourceExpired,
	}, 100)
	if err != nil {
		t.Fatalf("ReconcileSource: %v", err)
	}
	if result.SavedSearches != 1 || result.Favorites != 1 || result.TagAssignments != 1 || result.RecentsDeleted != 1 {
		t.Fatalf("lifecycle result=%+v", result)
	}
	loaded, _ := service.GetSavedSearch(context.Background(), actor.UserID, saved.ID)
	if loaded.State != SavedSearchBroken || loaded.StateReason != SavedReasonPointExpired || loaded.Query.Scope.Mode != assetsearch.SearchScopeExactPoints {
		t.Fatalf("saved exact scope widened or not broken: %+v", loaded)
	}
	if loaded.Version != saved.Version+1 {
		t.Fatalf("saved lifecycle transition did not advance version: before=%d after=%d", saved.Version, loaded.Version)
	}
	var favoriteRow model.BackupAssetFavorite
	if err := harness.db.Where("id = ?", favorite.ID).Take(&favoriteRow).Error; err != nil {
		t.Fatal(err)
	}
	if favoriteRow.State != string(OverlayTombstone) || favoriteRow.TombstoneReason != string(TombstoneSourceExpired) {
		t.Fatalf("favorite tombstone=%+v", favoriteRow)
	}
	if favoriteRow.Version != favorite.Version+1 {
		t.Fatalf("favorite lifecycle transition did not advance version: before=%d after=%d", favorite.Version, favoriteRow.Version)
	}
	var assignmentRow model.BackupAssetTagAssignment
	if err := harness.db.Where("id = ?", assignment.ID).Take(&assignmentRow).Error; err != nil {
		t.Fatal(err)
	}
	if assignmentRow.State != string(OverlayTombstone) || assignmentRow.TombstoneReason != string(TombstoneSourceExpired) {
		t.Fatalf("assignment tombstone=%+v", assignmentRow)
	}
	if assignmentRow.Version != assignment.Version+1 {
		t.Fatalf("tag-assignment lifecycle transition did not advance version: before=%d after=%d", assignment.Version, assignmentRow.Version)
	}
	var recentCount int64
	if err := harness.db.Model(&model.BackupAssetRecentAccess{}).Where("id = ?", recent.ID).Count(&recentCount).Error; err != nil || recentCount != 0 {
		t.Fatalf("recent survived source expiry count=%d err=%v", recentCount, err)
	}

	second, err := service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpired}, 100)
	if err != nil || second != (LifecycleResult{}) {
		t.Fatalf("lifecycle is not idempotent: result=%+v err=%v", second, err)
	}
	wantActions := []backupasset.AuditAction{
		backupasset.AuditActionSavedSearchBroken,
		backupasset.AuditActionFavoriteTombstone,
		backupasset.AuditActionTagAssignmentTombstone,
		backupasset.AuditActionOverlayCleanup,
	}
	if len(audit.inputs) != len(wantActions) {
		t.Fatalf("lifecycle audit count=%d inputs=%+v", len(audit.inputs), audit.inputs)
	}
	for index, action := range wantActions {
		if audit.inputs[index].Action != action || audit.inputs[index].RecoveryPointID != pointID || audit.inputs[index].Outcome != backupasset.AuditOutcomeSuccess {
			t.Fatalf("lifecycle audit[%d]=%+v", index, audit.inputs[index])
		}
	}
}

func TestLifecycleMissingSourceNeverReactivatesTombstone(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 702, Role: "operator"}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("d", 32), EntryID: strings.Repeat("d", 64)}
	harness.assets[ref] = true
	favorite, _ := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{Ref: ref, IdempotencyKey: "missing-source-fav1"})
	_, _ = service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: ref.RecoveryPointID, Reason: SourceMissing}, 10)
	harness.assets[ref] = true
	var row model.BackupAssetFavorite
	if err := harness.db.Where("id = ?", favorite.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != string(OverlayTombstone) {
		t.Fatalf("source return silently reactivated favorite: %+v", row)
	}
}

func TestLifecycleReconcileHonorsPerOverlayBatchLimit(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 703, Role: "operator"}
	pointID := strings.Repeat("e", 32)
	harness.points[pointID] = true
	tag, err := service.CreateTag(context.Background(), actor.UserID, "batch-tag", "batch-limit-tag-01")
	if err != nil {
		t.Fatal(err)
	}
	for index, entryCharacter := range []string{"1", "2"} {
		ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat(entryCharacter, 64)}
		harness.assets[ref] = true
		if _, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
			Ref: ref, IdempotencyKey: "batch-limit-favorite-0" + entryCharacter,
		}); err != nil {
			t.Fatalf("add favorite %d: %v", index, err)
		}
		if _, err := service.AssignTag(context.Background(), actor, tag.ID, ref, "batch-limit-assign-0"+entryCharacter); err != nil {
			t.Fatalf("assign tag %d: %v", index, err)
		}
	}

	first, err := service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpired}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Favorites != 1 || first.TagAssignments != 1 {
		t.Fatalf("first lifecycle batch exceeded limit: %+v", first)
	}
	second, err := service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpired}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.Favorites != 1 || second.TagAssignments != 1 {
		t.Fatalf("second lifecycle batch did not process remainder: %+v", second)
	}
}

func TestLifecycleReconcileInvalidSourcesDiscoversTerminalAndMissingPoints(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 704, Role: "operator"}
	expiredPointID := strings.Repeat("7", 32)
	missingPointID := strings.Repeat("8", 32)
	for _, pointID := range []string{expiredPointID, missingPointID} {
		ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat(pointID[:1], 64)}
		harness.assets[ref] = true
		if _, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
			Ref: ref, IdempotencyKey: "invalid-source-fav-" + pointID[:1],
		}); err != nil {
			t.Fatal(err)
		}
	}
	now := harness.clock.Now()
	if err := harness.db.Create(&model.RecoveryPoint{
		ID: expiredPointID, RepositoryID: strings.Repeat("9", 32), Semantics: string(backupasset.PointXirangManifest),
		State: string(backupasset.RecoveryPointExpired), LineageJSON: "{}", ConsistencyJSON: "{}", FidelityJSON: "{}",
		CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	count, err := service.ReconcileInvalidSources(context.Background(), 10)
	if err != nil || count != 2 {
		t.Fatalf("ReconcileInvalidSources count=%d err=%v", count, err)
	}
	var active int64
	if err := harness.db.Model(&model.BackupAssetFavorite{}).Where("state = ?", OverlayActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("invalid-source favorites remained active=%d err=%v", active, err)
	}
	if count, err := service.ReconcileInvalidSources(context.Background(), 10); err != nil || count != 0 {
		t.Fatalf("repeated invalid-source reconcile count=%d err=%v", count, err)
	}
}

func TestLifecycleCleanupExpiredRecentReleasesQuotaAndIsIdempotent(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 705, Role: "operator"}
	oldRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("1", 64)}
	newRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("2", 32), EntryID: strings.Repeat("2", 64)}
	harness.assets[oldRef] = true
	harness.assets[newRef] = true
	if _, err := service.RecordRecent(context.Background(), actor, oldRef); err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(29 * 24 * time.Hour)
	if _, err := service.RecordRecent(context.Background(), actor, newRef); err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(2 * 24 * time.Hour)
	count, err := service.CleanupExpiredRecent(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("CleanupExpiredRecent count=%d err=%v", count, err)
	}
	var rows []model.BackupAssetRecentAccess
	if err := harness.db.Where("owner_user_id = ?", actor.UserID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RecoveryPointID != newRef.RecoveryPointID {
		t.Fatalf("expired cleanup rows=%+v", rows)
	}
	var usage model.BackupAssetOverlayUsage
	if err := harness.db.Where("owner_user_id = ?", actor.UserID).Take(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.RecentCount != 1 {
		t.Fatalf("expired cleanup usage=%+v", usage)
	}
	if count, err := service.CleanupExpiredRecent(context.Background(), 1); err != nil || count != 0 {
		t.Fatalf("repeated expired cleanup count=%d err=%v", count, err)
	}
}

type overlayAuditSpy struct {
	inputs []backupasset.AuditEventInput
}

func (spy *overlayAuditSpy) Write(_ context.Context, input backupasset.AuditEventInput) error {
	spy.inputs = append(spy.inputs, input)
	return nil
}
