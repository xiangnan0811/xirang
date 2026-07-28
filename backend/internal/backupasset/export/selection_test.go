package export

import (
	"errors"
	"testing"
)

func TestFreezeSelectionSortsDeduplicatesAndBindsDigest(t *testing.T) {
	first := frozenItemFixture()
	second := frozenItemFixture()
	second.Ref.RecoveryPointID = "00000000000000000000000000000000"
	second.Ref.EntryID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second.CatalogGenerationID = "33333333333333333333333333333333"
	second.ArchiveComponents = []string{"other.txt"}

	selection, err := FreezeSelection([]FrozenItem{first, second, first}, nil, SelectionLimits{
		MaxItems: 10, MaxSourcePoints: 2, MaxLogicalBytes: 100,
	})
	if err != nil {
		t.Fatalf("FreezeSelection: %v", err)
	}
	if len(selection.Items) != 2 {
		t.Fatalf("deduplicated item count=%d want=2", len(selection.Items))
	}
	if selection.Items[0].Ref != second.Ref || selection.Items[1].Ref != first.Ref {
		t.Fatalf("selection order=%+v", selection.Items)
	}
	if len(selection.Digest) != 64 {
		t.Fatalf("selection digest=%q", selection.Digest)
	}

	reordered, err := FreezeSelection([]FrozenItem{first, second}, nil, SelectionLimits{
		MaxItems: 10, MaxSourcePoints: 2, MaxLogicalBytes: 100,
	})
	if err != nil || reordered.Digest != selection.Digest {
		t.Fatalf("reordered digest=%q err=%v want=%q", reordered.Digest, err, selection.Digest)
	}
	changed := second
	changed.SourceFingerprint += "-changed"
	different, err := FreezeSelection([]FrozenItem{first, changed}, nil, SelectionLimits{
		MaxItems: 10, MaxSourcePoints: 2, MaxLogicalBytes: 100,
	})
	if err != nil || different.Digest == selection.Digest {
		t.Fatalf("one-field change digest=%q err=%v", different.Digest, err)
	}
}

func TestFreezeSelectionLimitsFailClosedWithoutTruncation(t *testing.T) {
	first := frozenItemFixture()
	second := frozenItemFixture()
	second.Ref.EntryID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second.LogicalSize = 60
	second.ArchiveComponents = []string{"file-2.txt"}

	for _, limits := range []SelectionLimits{
		{MaxItems: 1, MaxSourcePoints: 2, MaxLogicalBytes: 1000},
		{MaxItems: 10, MaxSourcePoints: 2, MaxLogicalBytes: 100},
	} {
		if _, err := FreezeSelection([]FrozenItem{first, second}, nil, limits); !errors.Is(err, ErrSelectionLimit) {
			t.Fatalf("limits=%+v error=%v want ErrSelectionLimit", limits, err)
		}
	}
}

func TestSavedSearchBindingParticipatesInValidationNotSelectionDigest(t *testing.T) {
	items := []FrozenItem{frozenItemFixture()}
	limits := SelectionLimits{MaxItems: 10, MaxSourcePoints: 2, MaxLogicalBytes: 100}
	first, err := FreezeSelection(items, &SavedSearchCommitBindingV1{
		SavedSearchID: "44444444444444444444444444444444", ExpectedVersion: 1,
		CanonicalQueryDigest:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SearchGenerationDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}, limits)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FreezeSelection(items, &SavedSearchCommitBindingV1{
		SavedSearchID: "44444444444444444444444444444444", ExpectedVersion: 2,
		CanonicalQueryDigest:   "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		SearchGenerationDigest: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("saved-search commit binding changed exact item digest: %q != %q", first.Digest, second.Digest)
	}
}
