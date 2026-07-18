package overlay

import (
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
)

func TestOverlayClosedContractsRejectUnknownProducts(t *testing.T) {
	for _, state := range []string{"active", "broken", "blocked"} {
		if _, err := ParseSavedSearchState(state); err != nil {
			t.Fatalf("parse saved state %q: %v", state, err)
		}
	}
	if _, err := ParseSavedSearchState("future"); !errors.Is(err, ErrInvalidOverlay) {
		t.Fatalf("unknown saved state got %v", err)
	}
	if err := ValidateOverlayRef(backupasset.AssetRef{RecoveryPointID: "bad", EntryID: "bad"}); !errors.Is(err, ErrInvalidOverlay) {
		t.Fatalf("invalid ref got %v", err)
	}
}

func TestOverlayModelMappersFailClosedOnCorruptProducts(t *testing.T) {
	now := time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("a", 32)
	entryID := strings.Repeat("b", 64)
	opaqueID := strings.Repeat("c", 32)

	_, err := savedSearchFromModel(model.BackupAssetSavedSearch{
		ID: opaqueID, OwnerUserID: 1, Version: 1, State: string(SavedSearchActive),
		StateReason: string(SavedReasonPointMissing), CreatedAt: now, UpdatedAt: now,
	}, assetsearch.SearchRequest{})
	if !errors.Is(err, ErrOverlayUnavailable) {
		t.Fatalf("active saved search with broken reason got %v", err)
	}
	if _, err := savedSearchFromModel(model.BackupAssetSavedSearch{
		ID: opaqueID, OwnerUserID: 1, Version: 1, State: string(SavedSearchBlocked),
		StateReason: string(SavedReasonASTSchemaUnsupported), CreatedAt: now, UpdatedAt: now,
	}, assetsearch.SearchRequest{}); err != nil {
		t.Fatalf("valid blocked saved-search product got %v", err)
	}

	_, err = favoriteFromModel(model.BackupAssetFavorite{
		ID: opaqueID, OwnerUserID: 1, RecoveryPointID: pointID, EntryID: entryID,
		State: string(OverlayActive), TombstoneReason: string(TombstoneSourceMissing), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrOverlayUnavailable) {
		t.Fatalf("active favorite with tombstone reason got %v", err)
	}

	_, err = tagFromModel(model.BackupAssetTagDefinition{
		ID: opaqueID, OwnerUserID: 1, EncryptedName: "tag", NameToken: strings.Repeat("d", 64),
		KeyVersion: 1, TokenState: "future", Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrOverlayUnavailable) {
		t.Fatalf("tag with unknown token state got %v", err)
	}

	_, err = assignmentFromModel(model.BackupAssetTagAssignment{
		ID: opaqueID, OwnerUserID: 1, TagID: strings.Repeat("d", 32), RecoveryPointID: pointID, EntryID: entryID,
		State: "future", Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrOverlayUnavailable) {
		t.Fatalf("assignment with unknown state got %v", err)
	}

	_, err = recentFromModel(model.BackupAssetRecentAccess{
		ID: opaqueID, OwnerUserID: 1, RecoveryPointID: pointID, EntryID: entryID,
		AccessCount: 0, LastAccessedAt: now, ExpiresAt: now.Add(time.Hour), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrOverlayUnavailable) {
		t.Fatalf("recent with non-positive count got %v", err)
	}
}
