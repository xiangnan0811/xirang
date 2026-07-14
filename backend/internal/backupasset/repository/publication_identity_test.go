package repository

import (
	"strings"
	"testing"
)

func TestDeriveRecoveryPointIDIsStableAndRunScoped(t *testing.T) {
	linkID := "0123456789abcdef0123456789abcdef"
	first, err := deriveRecoveryPointID(linkID, 42)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveRecoveryPointID(linkID, 42)
	if err != nil || first != second || len(first) != 32 {
		t.Fatalf("unstable point id: %q %q %v", first, second, err)
	}
	if first != "f8d8a903c42c38398811387e4c201a28" {
		t.Fatalf("point ID=%q, want exact published vector", first)
	}
	third, err := deriveRecoveryPointID(linkID, 43)
	if err != nil || third == first {
		t.Fatalf("run-scoped id not unique: %q %v", third, err)
	}
	for _, input := range []struct {
		linkID string
		runID  uint
	}{
		{"", 42},
		{strings.Repeat("A", 32), 42},
		{strings.Repeat("a", 31), 42},
		{linkID, 0},
	} {
		if _, err := deriveRecoveryPointID(input.linkID, input.runID); err == nil {
			t.Fatalf("invalid identity input %#v was accepted", input)
		}
	}
}

func TestPublicationTagsAreCanonicalAndOpaque(t *testing.T) {
	linkID := "0123456789abcdef0123456789abcdef"
	pointID := "f8d8a903c42c38398811387e4c201a28"
	tags, err := deriveResticPublicationTags(linkID, pointID)
	if err != nil {
		t.Fatal(err)
	}
	want := [2]string{
		"xirang.link.v1.0123456789abcdef0123456789abcdef",
		"xirang.point.v1.f8d8a903c42c38398811387e4c201a28",
	}
	if tags != want {
		t.Fatalf("tags=%q, want %q", tags, want)
	}
	for _, forbidden := range []string{",", " ", "\t", "\n", "Task", "0123456789ABCDEF"} {
		if strings.Contains(tags[0], forbidden) || strings.Contains(tags[1], forbidden) {
			t.Fatalf("tags leaked or accepted unsafe material %q: %q", forbidden, tags)
		}
	}
	for _, input := range [][2]string{
		{"xirang.link.v1." + linkID, "xirang.point.v1." + pointID},
		{linkID + ",another", pointID},
		{strings.ToUpper(linkID), pointID},
		{linkID, strings.ToUpper(pointID)},
		{linkID, "task-run-42"},
	} {
		if _, err := deriveResticPublicationTags(input[0], input[1]); err == nil {
			t.Fatalf("unsafe tag inputs %#v were accepted", input)
		}
	}
}
