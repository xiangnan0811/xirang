package fileaccess

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathStrictLocatorRejectsAmbiguousOrEscapingInput(t *testing.T) {
	invalid := []string{"", ".", "..", "a/../b", "a/./b", "/absolute", "a//b", "a/", "nul\x00byte", "C:\\windows", `\\server\share`, string([]byte{0xff})}
	for _, raw := range invalid {
		t.Run(strings.ReplaceAll(raw, "/", "_"), func(t *testing.T) {
			if _, err := ParseLocator(raw, ProviderPolicy); !errors.Is(err, ErrInvalidLocator) {
				t.Fatalf("ParseLocator(%q) error=%v", raw, err)
			}
		})
	}
	for _, valid := range []string{"file", "dir/file", "-leading", "文件.txt"} {
		if locator, err := ParseLocator(valid, ProviderPolicy); err != nil || locator.Path != valid {
			t.Fatalf("valid locator %q got %+v err=%v", valid, locator, err)
		}
	}
}

func TestPathLegacyAbsoluteAndRelativeStayWithinRoot(t *testing.T) {
	root := Root{Path: filepath.Join(string(filepath.Separator), "backup")}
	for _, raw := range []string{"/", "sub", "sub/file", filepath.Join(root.Path, "absolute")} {
		locator, err := ParseLocator(raw, LegacyPolicy)
		if err != nil {
			t.Fatalf("ParseLocator(%q): %v", raw, err)
		}
		_, absolute, err := Resolve(root, locator, LegacyPolicy)
		if err != nil || (absolute != root.Path && !strings.HasPrefix(absolute, root.Path+string(filepath.Separator))) {
			t.Fatalf("Resolve(%q)=%q err=%v", raw, absolute, err)
		}
	}
	for _, raw := range []string{"../outside", filepath.Join(string(filepath.Separator), "backup-evil", "file")} {
		locator, err := ParseLocator(raw, LegacyPolicy)
		if err == nil {
			_, _, err = Resolve(root, locator, LegacyPolicy)
		}
		if !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("escaping legacy path %q error=%v", raw, err)
		}
	}
}

func TestPathRootOverlapUsesComponentBoundary(t *testing.T) {
	root := Root{Path: "/backup"}
	if Contains(root.Path, "/backup-evil/file") {
		t.Fatal("overlapping prefix treated as contained")
	}
	if !Contains(root.Path, "/backup/file") || !Contains(root.Path, "/backup") {
		t.Fatal("valid root containment rejected")
	}
	if runtime.GOOS == "windows" {
		t.Log("component boundary uses platform path semantics")
	}
}
