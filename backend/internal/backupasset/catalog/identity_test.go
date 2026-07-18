package catalog

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCatalogEntryIdentityIsRecoveryPointScopedAndTypeIndependent(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x42}, 32)
	firstPoint := "11111111111111111111111111111111"
	secondPoint := "22222222222222222222222222222222"

	fileIdentity, err := DeriveEntryIdentity(key, firstPoint, "docs/report.txt")
	if err != nil {
		t.Fatalf("derive file identity: %v", err)
	}
	typeChangedIdentity, err := DeriveEntryIdentity(key, firstPoint, "docs/report.txt")
	if err != nil {
		t.Fatalf("derive type-changed identity: %v", err)
	}
	if fileIdentity.EntryID != typeChangedIdentity.EntryID {
		t.Fatal("entry type/metadata change must not change the path identity")
	}
	if len(fileIdentity.EntryID) != 64 || strings.ToLower(fileIdentity.EntryID) != fileIdentity.EntryID {
		t.Fatalf("entry ID is not 64 lowercase hex: %q", fileIdentity.EntryID)
	}
	if fileIdentity.ParentEntryID == nil {
		t.Fatal("nested entry must have a parent entry ID")
	}
	parent, err := DeriveEntryIdentity(key, firstPoint, "docs")
	if err != nil {
		t.Fatalf("derive parent identity: %v", err)
	}
	if *fileIdentity.ParentEntryID != parent.EntryID {
		t.Fatalf("parent ID=%s want=%s", *fileIdentity.ParentEntryID, parent.EntryID)
	}

	otherPoint, err := DeriveEntryIdentity(key, secondPoint, "docs/report.txt")
	if err != nil {
		t.Fatalf("derive cross-point identity: %v", err)
	}
	if fileIdentity.EntryID == otherPoint.EntryID {
		t.Fatal("same path in different RecoveryPoints must have different IDs")
	}
}

func TestCatalogEntryIdentityTopLevelHasNullParentAndPreservesProviderCase(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x24}, 32)
	pointID := "11111111111111111111111111111111"
	top, err := DeriveEntryIdentity(key, pointID, "Report.txt")
	if err != nil {
		t.Fatalf("derive top-level identity: %v", err)
	}
	if top.ParentEntryID != nil || top.Name != "Report.txt" || top.NormalizedPath != "Report.txt" {
		t.Fatalf("unexpected top-level identity: %#v", top)
	}
	lower, err := DeriveEntryIdentity(key, pointID, "report.txt")
	if err != nil {
		t.Fatalf("derive case-distinct identity: %v", err)
	}
	if top.EntryID == lower.EntryID {
		t.Fatal("Catalog identity must not case-fold provider names")
	}
}

func TestCatalogEntryIdentityRejectsUnsafePathsAndMissingKey(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x11}, 32)
	pointID := "11111111111111111111111111111111"
	for _, path := range []string{"", "/absolute", "trailing/", "a//b", "a/./b", "a/../b", "a\\b", "a/\x00b", ".", ".."} {
		path := path
		t.Run(path, func(t *testing.T) {
			if _, err := DeriveEntryIdentity(key, pointID, path); !errors.Is(err, ErrUnsafeEntryPath) {
				t.Fatalf("path %q error=%v", path, err)
			}
		})
	}
	if _, err := DeriveEntryIdentity(nil, pointID, "safe"); !errors.Is(err, ErrIdentityKeyUnavailable) {
		t.Fatalf("missing key error=%v", err)
	}
	if _, err := DeriveEntryIdentity(key, "bad-point", "safe"); !errors.Is(err, ErrInvalidAssetReference) {
		t.Fatalf("bad point error=%v", err)
	}
}

func TestCatalogEntryIdentityRegistryRejectsDuplicateAndCollision(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x33}, 32)
	pointID := "11111111111111111111111111111111"
	identity, err := DeriveEntryIdentity(key, pointID, "docs/report.txt")
	if err != nil {
		t.Fatalf("derive identity: %v", err)
	}
	registry := NewIdentityRegistry()
	if err := registry.Add(identity); err != nil {
		t.Fatalf("add first identity: %v", err)
	}
	if err := registry.Add(identity); !errors.Is(err, ErrDuplicateEntry) {
		t.Fatalf("duplicate error=%v", err)
	}
	collision := identity
	collision.NormalizedPath = "docs/other.txt"
	collision.Name = "other.txt"
	if err := registry.Add(collision); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("collision error=%v", err)
	}
}
