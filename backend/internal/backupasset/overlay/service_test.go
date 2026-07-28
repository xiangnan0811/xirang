package overlay

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var overlayTestDBSequence atomic.Uint64

func TestSavedSearchOwnerEncryptionExactScopeAndSafeNotFound(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	pointID := strings.Repeat("a", 32)
	harness.points[pointID] = true
	query := assetsearch.SearchRequest{
		SchemaVersion: assetsearch.QuerySchemaVersion,
		Root:          assetsearch.QueryNode{Op: assetsearch.QueryOpTerm, Field: assetsearch.SearchFieldName, Text: "Private Report"},
		Scope:         assetsearch.SearchScope{Mode: assetsearch.SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          assetsearch.SearchSortRelevance, Limit: 25,
	}
	created, err := service.CreateSavedSearch(context.Background(), Actor{UserID: 101, Role: "operator"}, CreateSavedSearchRequest{
		Query: query, IdempotencyKey: "saved-search-key-0001",
	})
	if err != nil {
		t.Fatalf("CreateSavedSearch: %v", err)
	}
	var raw model.BackupAssetSavedSearch
	if err := harness.db.Session(&gorm.Session{SkipHooks: true}).Where("id = ?", created.ID).Take(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw.EncryptedAST, "enc:") || strings.Contains(strings.ToLower(raw.EncryptedAST), "private report") {
		t.Fatalf("saved AST is not encrypted at rest: %q", raw.EncryptedAST)
	}
	loaded, err := service.GetSavedSearch(context.Background(), 101, created.ID)
	if err != nil || loaded.Query.Root.Text != "private report" || loaded.Query.Scope.Mode != assetsearch.SearchScopeExactPoints {
		t.Fatalf("GetSavedSearch=%+v err=%v", loaded, err)
	}
	if _, err := service.GetSavedSearch(context.Background(), 202, created.ID); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("other-owner lookup got %v, want safe not found", err)
	}
	if _, err := service.GetSavedSearch(context.Background(), 101, strings.Repeat("f", 32)); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("missing lookup got %v, want safe not found", err)
	}

	replayed, err := service.CreateSavedSearch(context.Background(), Actor{UserID: 101, Role: "operator"}, CreateSavedSearchRequest{
		Query: query, IdempotencyKey: "saved-search-key-0001",
	})
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("idempotent saved replay=%+v err=%v", replayed, err)
	}
	query.Root.Text = "different"
	if _, err := service.CreateSavedSearch(context.Background(), Actor{UserID: 101, Role: "operator"}, CreateSavedSearchRequest{
		Query: query, IdempotencyKey: "saved-search-key-0001",
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency collision got %v", err)
	}
}

func TestFavoriteQuotaNaturalIdempotencyAndBulkAuthorizationRollback(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	service.config.FavoriteQuota = 2
	actor := Actor{UserID: 301, Role: "operator"}
	refs := []backupasset.AssetRef{
		{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("1", 64)},
		{RecoveryPointID: strings.Repeat("2", 32), EntryID: strings.Repeat("2", 64)},
		{RecoveryPointID: strings.Repeat("3", 32), EntryID: strings.Repeat("3", 64)},
	}
	harness.assets[refs[0]] = true
	harness.assets[refs[1]] = true
	first, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{Ref: refs[0], Label: "mine", IdempotencyKey: "favorite-key-0001"})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := service.assets.(*overlayAuthorizerFake)
	if authorizer.assetCalls != 1 || authorizer.assetTxCalls != 1 {
		t.Fatalf("favorite authorization was not transaction-bound: calls=%d tx_calls=%d", authorizer.assetCalls, authorizer.assetTxCalls)
	}
	duplicate, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{Ref: refs[0], Label: "mine", IdempotencyKey: "favorite-key-0002"})
	if err != nil || duplicate.ID != first.ID {
		t.Fatalf("natural duplicate=%+v err=%v", duplicate, err)
	}
	if _, err := service.AddFavorites(context.Background(), actor, []AddFavoriteRequest{{Ref: refs[1]}, {Ref: refs[2]}}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("bulk unowned target got %v", err)
	}
	var count int64
	if err := harness.db.Model(&model.BackupAssetFavorite{}).Where("owner_user_id = ?", actor.UserID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed bulk partially mutated %d favorites", count)
	}
	harness.assets[refs[2]] = true
	if _, err := service.AddFavorites(context.Background(), actor, []AddFavoriteRequest{{Ref: refs[1]}, {Ref: refs[2]}}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota overflow got %v", err)
	}
	if err := service.RemoveFavorite(context.Background(), actor.UserID, refs[2], "favorite-key-remove-01"); err != nil {
		t.Fatalf("absent remove is not idempotent: %v", err)
	}
}

func TestTagOwnerNormalizationAssignmentAndSearchKeyGate(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actorA := Actor{UserID: 401, Role: "operator"}
	actorB := Actor{UserID: 402, Role: "operator"}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("4", 32), EntryID: strings.Repeat("4", 64)}
	harness.assets[ref] = true
	emptyRevision, err := service.Revision(context.Background(), actorA.UserID)
	if err != nil || len(emptyRevision) != 64 {
		t.Fatalf("empty tag revision=%q err=%v", emptyRevision, err)
	}
	tagA, err := service.CreateTag(context.Background(), actorA.UserID, "Ｒésumé", "tag-key-owner-a1")
	if err != nil {
		t.Fatal(err)
	}
	tagRevision, err := service.Revision(context.Background(), actorA.UserID)
	if err != nil || tagRevision == emptyRevision {
		t.Fatalf("tag create did not advance revision: before=%s after=%s err=%v", emptyRevision, tagRevision, err)
	}
	duplicate, err := service.CreateTag(context.Background(), actorA.UserID, "résumé", "tag-key-owner-a2")
	if err != nil || duplicate.ID != tagA.ID {
		t.Fatalf("normalized duplicate=%+v err=%v", duplicate, err)
	}
	tagB, err := service.CreateTag(context.Background(), actorB.UserID, "résumé", "tag-key-owner-b1")
	if err != nil || tagB.ID == tagA.ID {
		t.Fatalf("cross-owner tag isolation failed: a=%+v b=%+v err=%v", tagA, tagB, err)
	}
	afterOtherOwner, err := service.Revision(context.Background(), actorA.UserID)
	if err != nil || afterOtherOwner != tagRevision {
		t.Fatalf("other-owner tag changed revision: before=%s after=%s err=%v", tagRevision, afterOtherOwner, err)
	}
	assignment, err := service.AssignTag(context.Background(), actorA, tagA.ID, ref, "tag-assign-key-01")
	if err != nil {
		t.Fatal(err)
	}
	assignmentRevision, err := service.Revision(context.Background(), actorA.UserID)
	if err != nil || assignmentRevision == tagRevision {
		t.Fatalf("tag assignment did not advance revision: before=%s after=%s err=%v", tagRevision, assignmentRevision, err)
	}
	matched, err := service.Matches(context.Background(), actorA.UserID, ref, "RÉSUMÉ")
	if err != nil || !matched {
		t.Fatalf("tag match=%t err=%v", matched, err)
	}
	candidates, err := service.CandidateRefs(context.Background(), actorA.UserID, "RÉSUMÉ", []string{ref.RecoveryPointID}, 1)
	if err != nil || !reflect.DeepEqual(candidates, []backupasset.AssetRef{ref}) {
		t.Fatalf("tag candidates=%+v err=%v", candidates, err)
	}
	otherOwnerCandidates, err := service.CandidateRefs(context.Background(), actorB.UserID, "RÉSUMÉ", []string{ref.RecoveryPointID}, 1)
	if err != nil || len(otherOwnerCandidates) != 0 {
		t.Fatalf("other-owner tag candidates=%+v err=%v", otherOwnerCandidates, err)
	}
	authorizer := service.assets.(*overlayAuthorizerFake)
	if authorizer.assetCalls != authorizer.assetTxCalls {
		t.Fatalf("tag assignment authorization escaped transaction: calls=%d tx_calls=%d", authorizer.assetCalls, authorizer.assetTxCalls)
	}

	oldKey, _ := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	newKey, err := harness.ring.ReplaceRebuildable(context.Background(), backupasset.KeyDomainSearchToken, service.InvalidateSearchKey)
	if err != nil || newKey.Version != oldKey.Version+1 {
		t.Fatalf("replace Search key=%+v err=%v", newKey, err)
	}
	if _, err := service.Matches(context.Background(), actorA.UserID, ref, "résumé"); !errors.Is(err, ErrOverlayUnavailable) {
		t.Fatalf("tag lookup during rekey got %v", err)
	}
	if count, err := service.ReconcileTagKeys(context.Background(), 100); err != nil || count != 2 {
		t.Fatalf("ReconcileTagKeys count=%d err=%v", count, err)
	}
	matched, err = service.Matches(context.Background(), actorA.UserID, ref, "résumé")
	if err != nil || !matched || assignment.TagID != tagA.ID {
		t.Fatalf("tag match after rekey=%t assignment=%+v err=%v", matched, assignment, err)
	}
}

func TestRecentMergeRateQuotaAndFrozenTTL(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	audit := &overlayAuditSpy{}
	service.audit = audit
	service.config.RecentQuota = 2
	service.config.RecentWritesPerMinute = 2
	actor := Actor{UserID: 501, Role: "operator"}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("5", 32), EntryID: strings.Repeat("5", 64)}
	harness.assets[ref] = true
	first, err := service.RecordRecent(context.Background(), actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RecordRecent(context.Background(), actor, ref)
	if err != nil || second.ID != first.ID || second.AccessCount != 2 || !second.ExpiresAt.Equal(harness.clock.Now().Add(30*24*time.Hour)) {
		t.Fatalf("recent merge=%+v err=%v", second, err)
	}
	if _, err := service.RecordRecent(context.Background(), actor, ref); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("recent rate limit got %v", err)
	}
	if len(audit.inputs) != 2 || audit.inputs[0].Action != backupasset.AuditActionRecentRecord ||
		audit.inputs[0].RecoveryPointID != ref.RecoveryPointID || audit.inputs[0].EntryID != ref.EntryID {
		t.Fatalf("recent audit inputs=%+v", audit.inputs)
	}
	harness.clock.Advance(time.Minute)
	third, err := service.RecordRecent(context.Background(), actor, ref)
	if err != nil || third.AccessCount != 3 {
		t.Fatalf("recent window did not reset: %+v err=%v", third, err)
	}
	authorizer := service.assets.(*overlayAuthorizerFake)
	if authorizer.assetCalls != authorizer.assetTxCalls {
		t.Fatalf("recent authorization escaped transaction: calls=%d tx_calls=%d", authorizer.assetCalls, authorizer.assetTxCalls)
	}
}

func TestSavedSearchListUpdateUseDeleteAndCAS(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 601, Role: "operator"}
	pointID := strings.Repeat("6", 32)
	harness.points[pointID] = true

	created, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: savedQuery(pointID, "first"), IdempotencyKey: "saved-lifecycle-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListSavedSearches(context.Background(), actor.UserID, OverlayListRequest{Limit: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.ID {
		t.Fatalf("ListSavedSearches=%+v err=%v", page, err)
	}

	updated, err := service.UpdateSavedSearch(context.Background(), actor, created.ID, UpdateSavedSearchRequest{
		Query: savedQuery(pointID, "second"), ExpectedVersion: created.Version, IdempotencyKey: "saved-lifecycle-02",
	})
	if err != nil || updated.Version != created.Version+1 || updated.Query.Root.Text != "second" {
		t.Fatalf("UpdateSavedSearch=%+v err=%v", updated, err)
	}
	if _, err := service.UpdateSavedSearch(context.Background(), actor, created.ID, UpdateSavedSearchRequest{
		Query: savedQuery(pointID, "stale"), ExpectedVersion: created.Version, IdempotencyKey: "saved-lifecycle-03",
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale saved-search update got %v", err)
	}
	if _, err := service.UpdateSavedSearch(context.Background(), Actor{UserID: 602, Role: "operator"}, created.ID, UpdateSavedSearchRequest{
		Query: savedQuery(pointID, "other"), ExpectedVersion: updated.Version, IdempotencyKey: "saved-lifecycle-04",
	}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("other-owner saved-search update got %v", err)
	}

	used, err := service.UseSavedSearch(context.Background(), actor, created.ID)
	if err != nil || used.Query.Root.Text != "second" {
		t.Fatalf("UseSavedSearch=%+v err=%v", used, err)
	}
	audit := &overlayAuditSpy{}
	service.audit = audit
	harness.points[pointID] = false
	if _, err := service.UseSavedSearch(context.Background(), actor, created.ID); !errors.Is(err, ErrSavedSearchBroken) {
		t.Fatalf("unauthorized exact saved-search use got %v", err)
	}
	broken, err := service.GetSavedSearch(context.Background(), actor.UserID, created.ID)
	if err != nil || broken.State != SavedSearchBroken || broken.StateReason != SavedReasonScopeUnauthorized {
		t.Fatalf("saved search was not durably broken: %+v err=%v", broken, err)
	}
	if len(audit.inputs) != 1 || audit.inputs[0].Action != backupasset.AuditActionSavedSearchBroken ||
		audit.inputs[0].Actor.UserID != actor.UserID || audit.inputs[0].ItemCount != 1 ||
		audit.inputs[0].Fields[backupasset.AuditFieldReasonCode] != string(SavedReasonScopeUnauthorized) {
		t.Fatalf("scope-unauthorized saved-search audit=%+v", audit.inputs)
	}

	if err := service.DeleteSavedSearch(context.Background(), actor.UserID, created.ID, broken.Version, "saved-lifecycle-05"); err != nil {
		t.Fatalf("DeleteSavedSearch: %v", err)
	}
	if err := service.DeleteSavedSearch(context.Background(), actor.UserID, created.ID, broken.Version, "saved-lifecycle-05"); err != nil {
		t.Fatalf("DeleteSavedSearch replay: %v", err)
	}
	if _, err := service.GetSavedSearch(context.Background(), actor.UserID, created.ID); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("deleted saved search got %v", err)
	}
}

func TestValidateSavedSearchForExportTxClosesFinalPageRace(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 611, Role: "operator"}
	pointID := strings.Repeat("7", 32)
	harness.points[pointID] = true
	created, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: savedQuery(pointID, "before"), IdempotencyKey: "saved-export-race-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := SavedSearchQueryDigest(created.Query, service.overlayQueryLimits())
	if err != nil {
		t.Fatal(err)
	}
	binding := SavedSearchExportBinding{
		ID: created.ID, OwnerUserID: actor.UserID, ExpectedVersion: created.Version, CanonicalQueryDigest: digest,
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		return service.ValidateSavedSearchForExportTx(context.Background(), tx, binding)
	}); err != nil {
		t.Fatalf("validate unchanged saved search: %v", err)
	}

	updated, err := service.UpdateSavedSearch(context.Background(), actor, created.ID, UpdateSavedSearchRequest{
		Query: savedQuery(pointID, "after"), ExpectedVersion: created.Version, IdempotencyKey: "saved-export-race-02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		return service.ValidateSavedSearchForExportTx(context.Background(), tx, binding)
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale final-page binding error=%v want conflict", err)
	}

	updatedDigest, err := SavedSearchQueryDigest(updated.Query, service.overlayQueryLimits())
	if err != nil {
		t.Fatal(err)
	}
	wrongOwner := SavedSearchExportBinding{
		ID: updated.ID, OwnerUserID: actor.UserID + 1, ExpectedVersion: updated.Version, CanonicalQueryDigest: updatedDigest,
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		return service.ValidateSavedSearchForExportTx(context.Background(), tx, wrongOwner)
	}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("cross-owner export validation error=%v want safe not found", err)
	}
}

func TestFavoriteTagAndRecentListMutationLifecycle(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 901, Role: "operator"}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("9", 32), EntryID: strings.Repeat("9", 64)}
	harness.assets[ref] = true

	favorite, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
		Ref: ref, Label: "mine", IdempotencyKey: "favorite-lifecycle-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	favorites, err := service.ListFavorites(context.Background(), actor.UserID, OverlayListRequest{Limit: 20})
	if err != nil || len(favorites.Items) != 1 || favorites.Items[0].ID != favorite.ID {
		t.Fatalf("ListFavorites=%+v err=%v", favorites, err)
	}
	if err := service.RemoveFavorite(context.Background(), actor.UserID, ref, "favorite-lifecycle-02"); err != nil {
		t.Fatalf("RemoveFavorite: %v", err)
	}
	if err := service.RemoveFavorite(context.Background(), actor.UserID, ref, "favorite-lifecycle-02"); err != nil {
		t.Fatalf("RemoveFavorite replay: %v", err)
	}

	tag, err := service.CreateTag(context.Background(), actor.UserID, "Finance", "tag-lifecycle-0001")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := service.ListTags(context.Background(), actor.UserID, OverlayListRequest{Limit: 20})
	if err != nil || len(tags.Items) != 1 || tags.Items[0].ID != tag.ID {
		t.Fatalf("ListTags=%+v err=%v", tags, err)
	}
	updatedTag, err := service.UpdateTag(context.Background(), actor.UserID, tag.ID, UpdateTagRequest{
		Name: "Finance 2026", ExpectedVersion: tag.Version, IdempotencyKey: "tag-lifecycle-0002",
	})
	if err != nil || updatedTag.Version != tag.Version+1 || updatedTag.Name != "finance 2026" {
		t.Fatalf("UpdateTag=%+v err=%v", updatedTag, err)
	}
	if _, err := service.UpdateTag(context.Background(), actor.UserID, tag.ID, UpdateTagRequest{
		Name: "stale", ExpectedVersion: tag.Version, IdempotencyKey: "tag-lifecycle-0003",
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale tag update got %v", err)
	}
	if _, err := service.AssignTag(context.Background(), actor, tag.ID, ref, "tag-lifecycle-0004"); err != nil {
		t.Fatal(err)
	}
	if err := service.UnassignTag(context.Background(), actor.UserID, tag.ID, ref, "tag-lifecycle-0005"); err != nil {
		t.Fatalf("UnassignTag: %v", err)
	}
	if err := service.UnassignTag(context.Background(), actor.UserID, tag.ID, ref, "tag-lifecycle-0005"); err != nil {
		t.Fatalf("UnassignTag replay: %v", err)
	}
	if err := service.DeleteTag(context.Background(), actor.UserID, tag.ID, updatedTag.Version, "tag-lifecycle-0006"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}

	if _, err := service.RecordRecent(context.Background(), actor, ref); err != nil {
		t.Fatal(err)
	}
	recents, err := service.ListRecent(context.Background(), actor.UserID, OverlayListRequest{Limit: 20})
	if err != nil || len(recents.Items) != 1 || recents.Items[0].Ref != ref {
		t.Fatalf("ListRecent=%+v err=%v", recents, err)
	}
	cleared, err := service.ClearRecent(context.Background(), actor.UserID, "recent-lifecycle-01")
	if err != nil || cleared != 1 {
		t.Fatalf("ClearRecent=%d err=%v", cleared, err)
	}
	replayed, err := service.ClearRecent(context.Background(), actor.UserID, "recent-lifecycle-01")
	if err != nil || replayed != cleared {
		t.Fatalf("ClearRecent replay=%d err=%v", replayed, err)
	}
}

func TestOverlayModelsPersistNoSourceMetadataOrRetentionHold(t *testing.T) {
	fields := reflect.VisibleFields(reflect.TypeOf(model.BackupAssetFavorite{}))
	for _, field := range fields {
		for _, forbidden := range []string{"Path", "Name", "MIME", "Hash", "Hold", "Retention", "Provider"} {
			if strings.Contains(field.Name, forbidden) {
				t.Fatalf("favorite model persists forbidden source metadata field %s", field.Name)
			}
		}
	}
}

func TestOverlayConfigControlsQueryLabelAndIdempotencyBounds(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	service.config.LabelMaxBytes = 4
	service.config.QueryLimits.MaxPageSize = 10
	if err := service.TransitionIdempotencySettings(DefaultConfig().IdempotencyTTL, 32, func() error { return nil }); err != nil {
		t.Fatalf("configure idempotency bounds: %v", err)
	}
	actor := Actor{UserID: 701, Role: "operator"}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("7", 32), EntryID: strings.Repeat("7", 64)}
	harness.assets[ref] = true
	if _, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
		Ref: ref, Label: "12345", IdempotencyKey: "favorite-key-0701",
	}); !errors.Is(err, ErrInvalidOverlay) {
		t.Fatalf("configured label limit got %v, want invalid overlay", err)
	}

	pointID := strings.Repeat("8", 32)
	harness.points[pointID] = true
	query := savedQuery(pointID, "bounded")
	query.Limit = 11
	if _, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: query, IdempotencyKey: "saved-search-key-0701",
	}); !errors.Is(err, ErrInvalidOverlay) {
		t.Fatalf("configured query limit got %v, want invalid overlay", err)
	}

	query.Limit = 10
	accepted, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: query, IdempotencyKey: "abcdefghijklmnop~",
	})
	if err != nil || accepted.ID == "" {
		t.Fatalf("documented idempotency alphabet rejected: saved=%+v err=%v", accepted, err)
	}
	if _, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: query, IdempotencyKey: strings.Repeat("a", 33),
	}); !errors.Is(err, ErrInvalidOverlay) {
		t.Fatalf("configured idempotency maximum got %v, want invalid overlay", err)
	}
}

func TestOverlayIdempotencySettingsTransitionPersistsBeforeAtomicallySwapping(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	oldKey := strings.Repeat("a", 64)
	newKey := strings.Repeat("b", 32)
	if !service.validIdempotencyKey(oldKey) {
		t.Fatal("default idempotency key bound rejected the control key")
	}

	persistCalls := 0
	if err := service.TransitionIdempotencySettings(2*time.Hour, len(newKey), func() error {
		persistCalls++
		if !service.validIdempotencyKey(oldKey) {
			t.Fatal("idempotency settings swapped before persistence completed")
		}
		return nil
	}); err != nil {
		t.Fatalf("transition idempotency settings: %v", err)
	}
	if persistCalls != 1 {
		t.Fatalf("persistence calls=%d, want one", persistCalls)
	}
	if service.validIdempotencyKey(oldKey) || !service.validIdempotencyKey(newKey) {
		t.Fatal("successful transition did not atomically replace idempotency key bounds")
	}
	if _, err := service.ClearRecent(context.Background(), 741, newKey); err != nil {
		t.Fatalf("create receipt with transitioned key bound: %v", err)
	}
	var receipt model.BackupAssetOverlayIdempotency
	if err := harness.db.Where("owner_user_id = ? AND action = ? AND key_hash = ?", 741, actionRecentClear, hashIdempotencyKey(newKey)).Take(&receipt).Error; err != nil {
		t.Fatalf("load transitioned receipt: %v", err)
	}
	if want := harness.clock.Now().Add(2 * time.Hour); !receipt.ExpiresAt.Equal(want) {
		t.Fatalf("transitioned receipt expiry=%s, want %s", receipt.ExpiresAt, want)
	}

	persistErr := errors.New("FAKE_OVERLAY_IDEMPOTENCY_PERSIST_FAILURE_FOR_TEST_ONLY")
	if err := service.TransitionIdempotencySettings(3*time.Hour, 16, func() error {
		if !service.validIdempotencyKey(newKey) {
			t.Fatal("idempotency settings swapped before failed persistence returned")
		}
		return persistErr
	}); !errors.Is(err, persistErr) {
		t.Fatalf("failed transition error=%v, want %v", err, persistErr)
	}
	if !service.validIdempotencyKey(newKey) {
		t.Fatal("failed persistence changed idempotency key bounds")
	}
	fallbackKey := strings.Repeat("c", 32)
	if _, err := service.ClearRecent(context.Background(), 742, fallbackKey); err != nil {
		t.Fatalf("create receipt after failed transition: %v", err)
	}
	var rollbackReceipt model.BackupAssetOverlayIdempotency
	if err := harness.db.Where("owner_user_id = ? AND action = ? AND key_hash = ?", 742, actionRecentClear, hashIdempotencyKey(fallbackKey)).Take(&rollbackReceipt).Error; err != nil {
		t.Fatalf("load post-failure receipt: %v", err)
	}
	if want := harness.clock.Now().Add(2 * time.Hour); !rollbackReceipt.ExpiresAt.Equal(want) {
		t.Fatalf("post-failure receipt expiry=%s, want retained %s", rollbackReceipt.ExpiresAt, want)
	}
}

func TestOverlayFeatureDisabledBeforeAuthorizationKeyOrDatabaseAccess(t *testing.T) {
	_, harness := newOverlayTestHarness(t)
	authorizer := &overlayAuthorizerFake{assets: harness.assets, points: harness.points}
	service, err := NewService(ServiceDependencies{
		DB: harness.db, Keys: harness.ring, Assets: authorizer, Points: authorizer,
		Now: harness.clock.Now, Config: DefaultConfig(), FeatureEnabled: func() (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("9", 32), EntryID: strings.Repeat("9", 64)}
	harness.assets[ref] = true
	if _, err := service.AddFavorite(context.Background(), Actor{UserID: 801, Role: "operator"}, AddFavoriteRequest{
		Ref: ref, Label: "hidden", IdempotencyKey: "disabled-overlay-01",
	}); !errors.Is(err, catalog.ErrFeatureDisabled) {
		t.Fatalf("disabled Overlay got %v, want feature disabled", err)
	}
	if authorizer.assetCalls != 0 || authorizer.pointCalls != 0 {
		t.Fatalf("disabled Overlay reached authorization: asset=%d points=%d", authorizer.assetCalls, authorizer.pointCalls)
	}
}

func TestOverlayQuotaRaceSQLite(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	runOverlayQuotaRaceContract(t, service, harness)
}

func TestOverlayBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("REQUIRE_POSTGRES_OVERLAY_TEST") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_OVERLAY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	service, harness := newOverlayPostgresTestHarness(t, dsn)
	var usageCreateArrivals atomic.Int32
	releaseUsageCreates := make(chan struct{})
	if err := harness.db.Callback().Create().Before("gorm:create").Register("overlay_postgres_usage_create_barrier", func(tx *gorm.DB) {
		if tx.Statement.Table != "backup_asset_overlay_usage" {
			return
		}
		if usageCreateArrivals.Add(1) == 2 {
			close(releaseUsageCreates)
		}
		select {
		case <-releaseUsageCreates:
		case <-time.After(2 * time.Second):
			_ = tx.AddError(errors.New("overlay usage create barrier timed out"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	runOverlayQuotaRaceContract(t, service, harness)
	if err := harness.db.Callback().Create().Remove("overlay_postgres_usage_create_barrier"); err != nil {
		t.Fatal(err)
	}
	if usageCreateArrivals.Load() != 2 {
		t.Fatalf("PostgreSQL quota race did not force two missing-row creates: %d", usageCreateArrivals.Load())
	}
	now := harness.clock.Now()
	for index, pointID := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		if err := harness.db.Create(&model.RecoveryPoint{
			ID: pointID, RepositoryID: fmt.Sprintf("%032x", index+1), Semantics: string(backupasset.PointXirangManifest),
			State: string(backupasset.RecoveryPointCommitted), LineageJSON: "{}", ConsistencyJSON: "{}", FidelityJSON: "{}",
			CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
			PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	missingRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("c", 32), EntryID: strings.Repeat("3", 64)}
	harness.assets[missingRef] = true
	if _, err := service.AddFavorite(context.Background(), Actor{UserID: 9102, Role: "operator"}, AddFavoriteRequest{
		Ref: missingRef, IdempotencyKey: "postgres-missing-source-favorite",
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := service.ReconcileInvalidSources(context.Background(), 10); err != nil || count != 1 {
		t.Fatalf("PostgreSQL lifecycle reconcile count=%d err=%v", count, err)
	}

	var receiptQueries atomic.Int32
	if err := harness.db.Callback().Query().After("gorm:query").Register("overlay_postgres_receipt_query_delay", func(tx *gorm.DB) {
		if tx.Statement.Table == "backup_asset_overlay_idempotency" && receiptQueries.Add(1) == 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}); err != nil {
		t.Fatal(err)
	}
	actor := Actor{UserID: 9103, Role: "operator"}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("d", 32), EntryID: strings.Repeat("4", 64)}
	harness.assets[ref] = true
	start := make(chan struct{})
	results := make(chan Favorite, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			favorite, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
				Ref: ref, IdempotencyKey: "postgres-concurrent-replay-key",
			})
			results <- favorite
			errorsCh <- err
		}()
	}
	close(start)
	first, second := <-results, <-results
	firstErr, secondErr := <-errorsCh, <-errorsCh
	if err := harness.db.Callback().Query().Remove("overlay_postgres_receipt_query_delay"); err != nil {
		t.Fatal(err)
	}
	if firstErr != nil || secondErr != nil || first.ID == "" || first.ID != second.ID {
		t.Fatalf("PostgreSQL concurrent replay first=%+v err=%v second=%+v err=%v", first, firstErr, second, secondErr)
	}

	actor = Actor{UserID: 9104, Role: "operator"}
	expiredRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("e", 32), EntryID: strings.Repeat("5", 64)}
	activeRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("f", 32), EntryID: strings.Repeat("6", 64)}
	harness.assets[expiredRef] = true
	harness.assets[activeRef] = true
	if _, err := service.RecordRecent(context.Background(), actor, expiredRef); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordRecent(context.Background(), actor, activeRef); err != nil {
		t.Fatal(err)
	}
	var recentQueries atomic.Int32
	if err := harness.db.Callback().Query().After("gorm:query").Register("overlay_postgres_recent_query_delay", func(tx *gorm.DB) {
		if tx.Statement.Table == "backup_asset_recent_access" && recentQueries.Add(1) == 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	reconcileErrors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.ReconcileSource(context.Background(), SourceLifecycle{
				RecoveryPointID: expiredRef.RecoveryPointID, Reason: SourceExpired,
			}, 10)
			reconcileErrors <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-reconcileErrors; err != nil {
			t.Fatalf("PostgreSQL concurrent recent reconcile: %v", err)
		}
	}
	if err := harness.db.Callback().Query().Remove("overlay_postgres_recent_query_delay"); err != nil {
		t.Fatal(err)
	}
	var recentCount int64
	if err := harness.db.Model(&model.BackupAssetRecentAccess{}).Where("owner_user_id = ?", actor.UserID).Count(&recentCount).Error; err != nil {
		t.Fatal(err)
	}
	var usage model.BackupAssetOverlayUsage
	if err := harness.db.Where("owner_user_id = ?", actor.UserID).Take(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if recentCount != 1 || usage.RecentCount != 1 {
		t.Fatalf("PostgreSQL concurrent recent reconcile rows=%d usage=%+v", recentCount, usage)
	}

	tagActor := Actor{UserID: 9105, Role: "operator"}
	tagRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("7", 64)}
	harness.assets[tagRef] = true
	tag, err := service.CreateTag(context.Background(), tagActor.UserID, "finance", "postgres-tag-create-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AssignTag(context.Background(), tagActor, tag.ID, tagRef, "postgres-tag-assign-key"); err != nil {
		t.Fatal(err)
	}
	candidates, err := service.CandidateRefs(context.Background(), tagActor.UserID, "FINANCE", []string{tagRef.RecoveryPointID}, 1)
	if err != nil || !reflect.DeepEqual(candidates, []backupasset.AssetRef{tagRef}) {
		t.Fatalf("PostgreSQL tag candidates=%+v err=%v", candidates, err)
	}
}

func runOverlayQuotaRaceContract(t *testing.T, service *Service, harness *overlayTestHarness) {
	t.Helper()
	service.config.FavoriteQuota = 1
	actor := Actor{UserID: 9101, Role: "operator"}
	refs := []backupasset.AssetRef{
		{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("1", 64)},
		{RecoveryPointID: strings.Repeat("b", 32), EntryID: strings.Repeat("2", 64)},
	}
	for _, ref := range refs {
		harness.assets[ref] = true
	}
	start := make(chan struct{})
	errorsCh := make(chan error, len(refs))
	for index, ref := range refs {
		index, ref := index, ref
		go func() {
			<-start
			_, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
				Ref: ref, IdempotencyKey: fmt.Sprintf("overlay-quota-favorite-%02d", index),
			})
			errorsCh <- err
		}()
	}
	close(start)
	var successes, quotaFailures int
	for range refs {
		err := <-errorsCh
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrQuotaExceeded):
			quotaFailures++
		default:
			t.Fatalf("unexpected quota race error: %v", err)
		}
	}
	var favoriteCount int64
	if err := harness.db.Model(&model.BackupAssetFavorite{}).Where("owner_user_id = ?", actor.UserID).Count(&favoriteCount).Error; err != nil {
		t.Fatal(err)
	}
	if successes != 1 || quotaFailures != 1 || favoriteCount != 1 {
		t.Fatalf("quota race successes=%d quota=%d rows=%d", successes, quotaFailures, favoriteCount)
	}
}

var _ = catalog.AuthorizationScope{}

type overlayTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *overlayTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *overlayTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

type overlayAuthorizerFake struct {
	mu           sync.Mutex
	assets       map[backupasset.AssetRef]bool
	points       map[string]bool
	assetCalls   int
	assetTxCalls int
	pointCalls   int
}

func (authorizer *overlayAuthorizerFake) AuthorizeAsset(_ context.Context, tx *gorm.DB, actor Actor, ref backupasset.AssetRef) error {
	authorizer.mu.Lock()
	authorizer.assetCalls++
	if tx != nil {
		authorizer.assetTxCalls++
	}
	authorizer.mu.Unlock()
	if !validActor(actor) || !authorizer.assets[ref] {
		return backupasset.ErrForbidden
	}
	return nil
}

func (authorizer *overlayAuthorizerFake) AuthorizePoints(_ context.Context, actor Actor, pointIDs []string) error {
	authorizer.mu.Lock()
	authorizer.pointCalls++
	authorizer.mu.Unlock()
	if !validActor(actor) {
		return backupasset.ErrForbidden
	}
	for _, pointID := range pointIDs {
		if !authorizer.points[pointID] {
			return backupasset.ErrForbidden
		}
	}
	return nil
}

type overlayTestHarness struct {
	db     *gorm.DB
	ring   *backupasset.Keyring
	clock  *overlayTestClock
	assets map[backupasset.AssetRef]bool
	points map[string]bool
}

func newOverlayTestHarness(t *testing.T) (*Service, *overlayTestHarness) {
	t.Helper()
	configureOverlayTestEnvironment(t)
	clock := &overlayTestClock{now: time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)}
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"), overlayTestDBSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: clock.Now, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return buildOverlayTestHarness(t, db, clock)
}

func newOverlayPostgresTestHarness(t *testing.T, dsn string) (*Service, *overlayTestHarness) {
	t.Helper()
	configureOverlayTestEnvironment(t)
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL overlay base: %v", err)
	}
	schemaName := fmt.Sprintf("xirang_overlay_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schemaName).Error; err != nil {
		t.Fatalf("create PostgreSQL overlay schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL overlay schema: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	baseSQLDB, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schemaName + " CASCADE").Error
		_ = baseSQLDB.Close()
	})
	clock := &overlayTestClock{now: time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)}
	return buildOverlayTestHarness(t, db, clock)
}

func configureOverlayTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_OVERLAY_SERVICE_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func buildOverlayTestHarness(t *testing.T, db *gorm.DB, clock *overlayTestClock) (*Service, *overlayTestHarness) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.RecoveryPoint{},
		&model.WrappedDomainKey{}, &model.BackupAssetSavedSearch{}, &model.BackupAssetSavedSearchScopePoint{},
		&model.BackupAssetFavorite{}, &model.BackupAssetTagDefinition{}, &model.BackupAssetTagAssignment{},
		&model.BackupAssetRecentAccess{}, &model.BackupAssetOverlayUsage{}, &model.BackupAssetOverlayIdempotency{},
	); err != nil {
		t.Fatalf("migrate overlay test DB: %v", err)
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX idx_overlay_test_key_active ON wrapped_domain_keys(domain) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX idx_overlay_test_saved_scope ON backup_asset_saved_search_scope_points(saved_search_id, recovery_point_id)`,
		`CREATE UNIQUE INDEX idx_overlay_test_favorite ON backup_asset_favorites(owner_user_id, recovery_point_id, entry_id)`,
		`CREATE UNIQUE INDEX idx_overlay_test_tag_name ON backup_asset_tag_definitions(owner_user_id, name_token)`,
		`CREATE UNIQUE INDEX idx_overlay_test_tag_owner ON backup_asset_tag_definitions(id, owner_user_id)`,
		`CREATE UNIQUE INDEX idx_overlay_test_assignment ON backup_asset_tag_assignments(owner_user_id, tag_id, recovery_point_id, entry_id)`,
		`CREATE UNIQUE INDEX idx_overlay_test_recent ON backup_asset_recent_access(owner_user_id, recovery_point_id, entry_id)`,
		`CREATE UNIQUE INDEX idx_overlay_test_receipt ON backup_asset_overlay_idempotency(owner_user_id, action, key_hash)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create overlay test index: %v", err)
		}
	}
	ring := backupasset.NewKeyring(db, clock.Now)
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatal(err)
	}
	assets := make(map[backupasset.AssetRef]bool)
	points := make(map[string]bool)
	authorizer := &overlayAuthorizerFake{assets: assets, points: points}
	service, err := NewService(ServiceDependencies{
		DB: db, Keys: ring, Assets: authorizer, Points: authorizer, Now: clock.Now, Config: DefaultConfig(),
		FeatureEnabled: func() (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, &overlayTestHarness{db: db, ring: ring, clock: clock, assets: assets, points: points}
}

func savedQuery(pointID, text string) assetsearch.SearchRequest {
	return assetsearch.SearchRequest{
		SchemaVersion: assetsearch.QuerySchemaVersion,
		Root:          assetsearch.QueryNode{Op: assetsearch.QueryOpTerm, Field: assetsearch.SearchFieldName, Text: text},
		Scope:         assetsearch.SearchScope{Mode: assetsearch.SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          assetsearch.SearchSortRelevance, Limit: 25,
	}
}
