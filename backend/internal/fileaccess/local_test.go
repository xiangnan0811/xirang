package fileaccess

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalStrictListTypesAndBoundedOrder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local access is Linux-only")
	}
	root := t.TempDir()
	for _, name := range []string{"z.txt", "a.txt", "m.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	tree := NewLocalTree()
	page, err := tree.List(context.Background(), Root{Path: root}, RootLocator(), ProviderPolicy, PageRequest{Limit: 2, MaxItems: 100, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Name != "a.txt" || page.Items[1].Name != "dir" || !page.HasMore {
		t.Fatalf("unexpected first page: %+v", page)
	}
	page, err = tree.List(context.Background(), Root{Path: root}, RootLocator(), ProviderPolicy, PageRequest{Limit: 10, AfterName: "dir", MaxItems: 100, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0].Name != "link" || page.Items[0].Type != EntrySymlink {
		t.Fatalf("unexpected continuation: %+v", page)
	}
}

func TestLocalStrictOpenRejectsSymlinkEscapeAndSpecialFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local access is Linux-only")
	}
	root := t.TempDir()
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "outside"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(external, "outside"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	tree := NewLocalTree()
	locator, _ := ParseLocator("escape", ProviderPolicy)
	if _, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, locator, ProviderPolicy); !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("escape symlink error=%v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator, _ = ParseLocator("directory", ProviderPolicy)
	if _, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, locator, ProviderPolicy); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("directory open error=%v", err)
	}
}

func TestLocalLegacyFollowsOnlyInternalSymlink(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "outside"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(root, "safe-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(external, "outside"), filepath.Join(root, "escape-link")); err != nil {
		t.Fatal(err)
	}
	tree := NewLocalTree()
	safe, _ := ParseLocator("safe-link", LegacyPolicy)
	handle, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, safe, LegacyPolicy)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := io.ReadAll(handle)
	if err := handle.Close(); err != nil || string(value) != "inside" {
		t.Fatalf("safe link value=%q close=%v", value, err)
	}
	escape, _ := ParseLocator("escape-link", LegacyPolicy)
	if _, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, escape, LegacyPolicy); err == nil {
		t.Fatal("escaping legacy symlink opened")
	}
}

func TestLocalStrictOpenBindsRootAcrossRenameAndReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local access is Linux-only")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := NewLocalTree()
	tree.beforeOpen = func() {
		moved := filepath.Join(parent, "moved")
		if err := os.Rename(root, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "file"), []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	locator, _ := ParseLocator("file", ProviderPolicy)
	handle, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, locator, ProviderPolicy)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := io.ReadAll(handle)
	if err := handle.Close(); err != nil || string(value) != "original" {
		t.Fatalf("root binding value=%q close=%v", value, err)
	}
}

func TestLocalStrictOpenDetectsSourceChangeOnCloseAndSupportsRange(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local access is Linux-only")
	}
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := NewLocalTree()
	locator, _ := ParseLocator("file", ProviderPolicy)
	rangeHandle, _, err := tree.OpenRange(context.Background(), Root{Path: root}, locator, ProviderPolicy, ByteRange{Offset: 2, Length: 4})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := io.ReadAll(rangeHandle)
	if err := rangeHandle.Close(); err != nil || string(value) != "2345" {
		t.Fatalf("range value=%q close=%v", value, err)
	}

	handle, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, locator, ProviderPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed source close error=%v", err)
	}
}
