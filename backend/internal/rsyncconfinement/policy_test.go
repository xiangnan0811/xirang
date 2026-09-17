package rsyncconfinement

import (
	"strings"
	"testing"
)

func TestParseRootsNormalizesAndDeduplicates(t *testing.T) {
	roots, err := ParseRoots(" /allowed/one/../one , /allowed/two, /allowed/two,, ")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(roots, ","), "/allowed/one,/allowed/two"; got != want {
		t.Fatalf("roots=%q want %q", got, want)
	}
}

func TestParseRootsRejectsRelativeAndMalformed(t *testing.T) {
	for _, raw := range []string{"relative", "/ok\x00bad", "/ok\nother"} {
		if _, err := ParseRoots(raw); err == nil {
			t.Fatalf("ParseRoots(%q) accepted invalid root", raw)
		}
	}
}

func TestValidatePathUsesComponents(t *testing.T) {
	roots := []string{"/allowed"}
	for _, path := range []string{"/allowed", "/allowed/child", "/allowed/child/../child"} {
		if err := ValidatePath(path, roots, "source"); err != nil {
			t.Fatalf("ValidatePath(%q): %v", path, err)
		}
	}
	for _, path := range []string{"/allowed-other/file", "/allowed/../outside", "relative/file"} {
		if err := ValidatePath(path, roots, "source"); err == nil {
			t.Fatalf("ValidatePath(%q) accepted escape", path)
		}
	}
}

func TestValidatePathUnrestrictedWhenRootsEmpty(t *testing.T) {
	for _, path := range []string{"relative/file", "/outside", "./nested"} {
		if err := ValidatePath(path, nil, "source"); err != nil {
			t.Fatalf("unrestricted path %q rejected: %v", path, err)
		}
	}
}
