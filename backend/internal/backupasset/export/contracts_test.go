package export

import (
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestValidateCreateSelectionUsesClosedUnion(t *testing.T) {
	pointID := "11111111111111111111111111111111"
	entryID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name      string
		selection CreateSelectionV1
		wantErr   bool
	}{
		{
			name: "explicit",
			selection: CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit,
				Refs: []backupasset.AssetRef{{RecoveryPointID: pointID, EntryID: entryID}}},
		},
		{
			name: "saved search",
			selection: CreateSelectionV1{SchemaVersion: 1, Kind: SelectionSavedSearch,
				SavedSearchID: "22222222222222222222222222222222", SavedSearchVersion: 7},
		},
		{name: "mixed arms", selection: CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit,
			Refs:          []backupasset.AssetRef{{RecoveryPointID: pointID, EntryID: entryID}},
			SavedSearchID: "22222222222222222222222222222222", SavedSearchVersion: 7}, wantErr: true},
		{name: "entry only", selection: CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit,
			Refs: []backupasset.AssetRef{{EntryID: entryID}}}, wantErr: true},
		{name: "unknown kind", selection: CreateSelectionV1{SchemaVersion: 1, Kind: "future"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCreateSelection(test.selection)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateCreateSelection() error=%v wantErr=%t", err, test.wantErr)
			}
		})
	}
}

func TestValidateFrozenItemRejectsInvalidCompositeAndLinkMetadata(t *testing.T) {
	item := frozenItemFixture()
	if err := ValidateFrozenItem(item); err != nil {
		t.Fatalf("valid item: %v", err)
	}
	item.Ref.RecoveryPointID = ""
	if err := ValidateFrozenItem(item); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("entry-only item error=%v want ErrInvalidSelection", err)
	}

	link := frozenItemFixture()
	link.EntryType = backupasset.CatalogEntrySymlink
	link.LogicalSize = 0
	if err := ValidateFrozenItem(link); err != nil {
		t.Fatalf("symlink leaf without target metadata: %v", err)
	}
}

func TestFrozenSelectionPreservesRawNonfileFingerprint(t *testing.T) {
	limits := SelectionLimits{MaxItems: 2, MaxSourcePoints: 1, MaxLogicalBytes: 100}
	for _, entryType := range []backupasset.CatalogEntryType{
		backupasset.CatalogEntryDirectory, backupasset.CatalogEntrySymlink, backupasset.CatalogEntryHardlink,
		backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown, backupasset.CatalogEntryFile,
	} {
		for _, strength := range []string{"none", "weak", "strong"} {
			t.Run(string(entryType)+"/"+strength, func(t *testing.T) {
				item := frozenItemFixture()
				item.EntryType, item.FingerprintStrength, item.EntryFingerprint = entryType, strength, ""
				item.LogicalSize = 0
				selection, err := FreezeSelection([]FrozenItem{item}, nil, limits)
				allowed := entryType != backupasset.CatalogEntryFile && strength == "none"
				if !allowed {
					if !errors.Is(err, ErrInvalidSelection) {
						t.Fatalf("empty fingerprint accepted: %v", err)
					}
					return
				}
				if err != nil || len(selection.Items) != 1 || selection.Items[0].EntryFingerprint != "" {
					t.Fatalf("raw fingerprint not preserved: %+v, %v", selection, err)
				}
				item.EntryFingerprint = "later-fingerprint"
				changed, err := FreezeSelection([]FrozenItem{item}, nil, limits)
				if err != nil || changed.Digest == selection.Digest {
					t.Fatalf("fingerprint drift did not change digest: %+v, %v", changed, err)
				}
			})
		}
	}
}

func frozenItemFixture() FrozenItem {
	retention := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	return FrozenItem{
		SchemaVersion:              1,
		Ref:                        backupasset.AssetRef{RecoveryPointID: "11111111111111111111111111111111", EntryID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		CatalogGenerationID:        "22222222222222222222222222222222",
		SourceFingerprint:          "source-fingerprint-v1",
		EntryFingerprint:           "entry-fingerprint-v1",
		FingerprintStrength:        "strong",
		ProviderCapabilityRevision: 3,
		EntryType:                  backupasset.CatalogEntryFile,
		LogicalSize:                42,
		MediaType:                  "text/plain",
		RetentionUntil:             &retention,
		SelectionRootOrdinal:       0,
		ArchiveComponents:          []string{"root", "file.txt"},
	}
}
