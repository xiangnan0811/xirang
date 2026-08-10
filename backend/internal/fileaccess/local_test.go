package fileaccess

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPinnedStrictTreeNeverReturnsPath(t *testing.T) {
	treeType := reflect.TypeOf((*PinnedStrictTree)(nil)).Elem()
	for _, forbidden := range []string{"Path", "Root", "Locator", "String"} {
		if _, exists := treeType.MethodByName(forbidden); exists {
			t.Fatalf("PinnedStrictTree exposes forbidden %s accessor", forbidden)
		}
	}
	for _, required := range []string{"Close", "OpenDeclaredRegular", "Revalidate"} {
		if _, exists := treeType.MethodByName(required); !exists {
			t.Fatalf("PinnedStrictTree missing %s", required)
		}
	}
	for index := 0; index < treeType.NumMethod(); index++ {
		method := treeType.Method(index)
		for output := 0; output < method.Type.NumOut(); output++ {
			if method.Type.Out(output).Kind() == reflect.String {
				t.Fatalf("PinnedStrictTree.%s returns a string path", method.Name)
			}
		}
	}
}

func TestPinnedStrictTreeRejectsRootSwap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pinned strict tree requires Linux")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "managed-root")
	treePath := filepath.Join(root, "points", "point-a", "tree")
	filePath := filepath.Join(treePath, "entry")
	if err := os.MkdirAll(treePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFromFileInfo("entry", Locator{Path: "entry"}, info)
	pinned, err := OpenPinnedStrictTree(context.Background(), Root{Path: root}, Locator{Path: "points/point-a/tree"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()

	if err := os.Rename(root, filepath.Join(parent, "managed-root-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(treePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := pinned.OpenDeclaredRegular(context.Background(), entry); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("root replacement open error = %v, want ErrSourceChanged", err)
	}
}

func TestPinnedStrictTreeRejectsFinalTreeSwap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pinned strict tree requires Linux")
	}
	root := t.TempDir()
	treePath := filepath.Join(root, "points", "point-a", "tree")
	filePath := filepath.Join(treePath, "entry")
	if err := os.MkdirAll(treePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFromFileInfo("entry", Locator{Path: "entry"}, info)
	pinned, err := OpenPinnedStrictTree(context.Background(), Root{Path: root}, Locator{Path: "points/point-a/tree"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()

	if err := os.Rename(treePath, treePath+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(treePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := pinned.OpenDeclaredRegular(context.Background(), entry); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("final tree replacement open error = %v, want ErrSourceChanged", err)
	}
}

func TestPinnedStrictTreeRejectsLinkSwap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pinned strict tree requires Linux")
	}
	root := t.TempDir()
	treePath := filepath.Join(root, "points", "point-a", "tree")
	filePath := filepath.Join(treePath, "entry")
	if err := os.MkdirAll(treePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFromFileInfo("entry", Locator{Path: "entry"}, info)
	pinned, err := OpenPinnedStrictTree(context.Background(), Root{Path: root}, Locator{Path: "points/point-a/tree"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()

	if err := os.Rename(treePath, treePath+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(treePath+"-original", treePath); err != nil {
		t.Fatal(err)
	}

	if _, _, err := pinned.OpenDeclaredRegular(context.Background(), entry); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("link replacement open error = %v, want ErrSourceChanged", err)
	}
}

func TestPinnedStrictTreeRejectsEntrySwap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pinned strict tree requires Linux")
	}
	root := t.TempDir()
	treePath := filepath.Join(root, "points", "point-a", "tree")
	filePath := filepath.Join(treePath, "entry")
	if err := os.MkdirAll(treePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFromFileInfo("entry", Locator{Path: "entry"}, info)
	pinned, err := OpenPinnedStrictTree(context.Background(), Root{Path: root}, Locator{Path: "points/point-a/tree"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()

	if err := os.Rename(filePath, filePath+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := pinned.OpenDeclaredRegular(context.Background(), entry); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("entry replacement open error = %v, want ErrSourceChanged", err)
	}
}

func TestPinnedStrictTreeOpensDeclaredRegularAndCloses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pinned strict tree requires Linux")
	}
	root := t.TempDir()
	treePath := filepath.Join(root, "points", "point-a", "tree")
	filePath := filepath.Join(treePath, "entry")
	if err := os.MkdirAll(treePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("declared"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFromFileInfo("entry", Locator{Path: "entry"}, info)
	pinned, err := OpenPinnedStrictTree(context.Background(), Root{Path: root}, Locator{Path: "points/point-a/tree"})
	if err != nil {
		t.Fatal(err)
	}

	handle, stat, err := pinned.OpenDeclaredRegular(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	value, readErr := io.ReadAll(handle)
	if closeErr := handle.Close(); readErr != nil || closeErr != nil || string(value) != "declared" || stat.Size != int64(len(value)) {
		t.Fatalf("declared entry read=%q readErr=%v closeErr=%v stat=%+v", value, readErr, closeErr, stat)
	}
	if err := pinned.Close(); err != nil {
		t.Fatalf("close pinned tree: %v", err)
	}
	if _, _, err := pinned.OpenDeclaredRegular(context.Background(), entry); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("open after close error = %v, want ErrSourceChanged", err)
	}
	if err := pinned.Close(); err != nil {
		t.Fatalf("second pinned close: %v", err)
	}
}

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

func TestLocalStrictOpenRejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local access is Linux-only")
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "file"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "managed-tree")
	if err := os.Symlink(external, root); err != nil {
		t.Fatal(err)
	}
	locator, err := ParseLocator("file", ProviderPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewLocalTree().OpenRegular(context.Background(), Root{Path: root}, locator, ProviderPolicy); !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("symlink root open error=%v, want symlink denied", err)
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
