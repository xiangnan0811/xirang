package fileaccess

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

type localSFTPBackend struct {
	openHook   func(string)
	lstatHook  func(string, int)
	lstatCalls int
	opened     *trackedSFTPFile
}

func (*localSFTPBackend) RealPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
func (backend *localSFTPBackend) Lstat(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	backend.lstatCalls++
	if backend.lstatHook != nil {
		backend.lstatHook(path, backend.lstatCalls)
	}
	return info, err
}
func (backend *localSFTPBackend) Open(path string) (SFTPFile, error) {
	if backend.openHook != nil {
		backend.openHook(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	backend.opened = &trackedSFTPFile{File: file}
	return backend.opened, nil
}

type trackedSFTPFile struct {
	*os.File
	closed atomic.Bool
}

func (file *trackedSFTPFile) Close() error { file.closed.Store(true); return file.File.Close() }

type localBoundedEnumerator struct {
	bounded bool
	names   []string
	hook    func()
}

func (enumerator localBoundedEnumerator) Bounded() bool { return enumerator.bounded }
func (enumerator localBoundedEnumerator) Enumerate(context.Context, string, EnumerationLimits) ([]string, error) {
	if enumerator.hook != nil {
		enumerator.hook()
	}
	return append([]string(nil), enumerator.names...), nil
}

func TestSFTPStrictRequiresBoundedEnumerator(t *testing.T) {
	tree := NewSFTPTree(&localSFTPBackend{}, nil, nil)
	_, err := tree.List(context.Background(), Root{Path: t.TempDir()}, RootLocator(), ProviderPolicy, PageRequest{Limit: 1, MaxItems: 10, MaxBytes: 1024})
	if !errors.Is(err, ErrStrictUnavailable) {
		t.Fatalf("strict unbounded list error=%v", err)
	}
}

func TestSFTPStrictListAndOpenRejectSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires privileges on Windows")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	backend := &localSFTPBackend{}
	tree := NewSFTPTree(backend, localBoundedEnumerator{bounded: true, names: []string{"link", "file"}}, nil)
	page, err := tree.List(context.Background(), Root{Path: root}, RootLocator(), ProviderPolicy, PageRequest{Limit: 10, MaxItems: 10, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !sort.SliceIsSorted(page.Items, func(i, j int) bool { return page.Items[i].Name < page.Items[j].Name }) || len(page.Items) != 2 || page.Items[1].Type != EntrySymlink {
		t.Fatalf("unexpected page: %+v", page)
	}
	locator, _ := ParseLocator("link", ProviderPolicy)
	if _, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, locator, ProviderPolicy); !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("strict symlink error=%v", err)
	}
	legacy, _ := ParseLocator(filepath.Join(root, "link"), LegacyPolicy)
	handle, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, legacy, LegacyPolicy)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := io.ReadAll(handle)
	if err := handle.Close(); err != nil || string(value) != "value" {
		t.Fatalf("legacy symlink value=%q close=%v", value, err)
	}
}

func TestSFTPStrictListDoesNotTraverseDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires privileges on Windows")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "directory", "file"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("directory", filepath.Join(root, "link-directory")); err != nil {
		t.Fatal(err)
	}
	locator, err := ParseLocator("link-directory", ProviderPolicy)
	if err != nil {
		t.Fatal(err)
	}
	tree := NewSFTPTree(&localSFTPBackend{}, localBoundedEnumerator{bounded: true, names: []string{"file"}}, nil)
	if _, err := tree.List(context.Background(), Root{Path: root}, locator, ProviderPolicy, PageRequest{Limit: 10, MaxItems: 10, MaxBytes: 1024}); !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("strict directory symlink list error=%v", err)
	}
}

func TestSFTPListRejectsDirectoryReplacementAfterEnumeration(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	locator, err := ParseLocator("directory", ProviderPolicy)
	if err != nil {
		t.Fatal(err)
	}
	tree := NewSFTPTree(&localSFTPBackend{}, localBoundedEnumerator{bounded: true, hook: func() {
		if err := os.Remove(directory); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}}, nil)
	if _, err := tree.List(context.Background(), Root{Path: root}, locator, ProviderPolicy, PageRequest{Limit: 10, MaxItems: 10, MaxBytes: 1024}); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("replaced directory list error=%v", err)
	}
}

func TestSFTPListDetectsEntryContentChangeDuringObservation(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file")
	if err := os.WriteFile(filePath, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &localSFTPBackend{}
	backend.lstatHook = func(path string, call int) {
		if call == 3 && path == filePath {
			if err := os.WriteFile(path, []byte("after-after"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	tree := NewSFTPTree(backend, localBoundedEnumerator{bounded: true, names: []string{"file"}}, nil)
	if _, err := tree.List(context.Background(), Root{Path: root}, RootLocator(), ProviderPolicy, PageRequest{Limit: 10, MaxItems: 10, MaxBytes: 1024}); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed SFTP entry listing error=%v", err)
	}
}

func TestSFTPOpenDetectsPreOpenPostChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &localSFTPBackend{}
	backend.openHook = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("after-after"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree := NewSFTPTree(backend, localBoundedEnumerator{bounded: true}, nil)
	locator, _ := ParseLocator("file", ProviderPolicy)
	if _, _, err := tree.OpenRegular(context.Background(), Root{Path: root}, locator, ProviderPolicy); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed source error=%v", err)
	}
}

func TestSFTPLstatDetectsPathReplacementDuringObservation(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file")
	if err := os.WriteFile(filePath, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &localSFTPBackend{}
	backend.lstatHook = func(path string, call int) {
		if call != 1 || path != filePath {
			return
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree := NewSFTPTree(backend, localBoundedEnumerator{bounded: true}, nil)
	locator, _ := ParseLocator("file", ProviderPolicy)
	if _, err := tree.Lstat(context.Background(), Root{Path: root}, locator, ProviderPolicy); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("replaced SFTP stat error=%v", err)
	}
}

func TestSFTPRangeAndCancellationCloseHandle(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &localSFTPBackend{}
	tree := NewSFTPTree(backend, localBoundedEnumerator{bounded: true}, nil)
	locator, _ := ParseLocator("file", ProviderPolicy)
	handle, _, err := tree.OpenRange(context.Background(), Root{Path: root}, locator, ProviderPolicy, ByteRange{Offset: 3, Length: 3})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := io.ReadAll(handle)
	if err := handle.Close(); err != nil || string(value) != "345" {
		t.Fatalf("range value=%q close=%v", value, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	handle, _, err = tree.OpenRegular(ctx, Root{Path: root}, locator, ProviderPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if handle == nil {
		t.Fatal("SFTP open returned a nil read handle")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for !backend.opened.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !backend.opened.closed.Load() {
		t.Fatal("canceled SFTP read did not close file")
	}
}

func TestSFTPEnumeratorLimitFailsWithoutPartialSuccess(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree := NewSFTPTree(&localSFTPBackend{}, localBoundedEnumerator{bounded: true, names: []string{"a", "b", "c"}}, nil)
	if page, err := tree.List(context.Background(), Root{Path: root}, RootLocator(), ProviderPolicy, PageRequest{Limit: 1, MaxItems: 2, MaxBytes: 1024}); !errors.Is(err, ErrResourceLimit) || len(page.Items) != 0 {
		t.Fatalf("limit page=%+v err=%v", page, err)
	}
}
