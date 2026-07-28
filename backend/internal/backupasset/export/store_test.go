package export

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestExportStoreStagesSealsAndPurgesOpaqueObjects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	staging, err := store.CreateStaging(
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(locator) != locator || filepath.Dir(locator) != "." {
		t.Fatalf("locator leaked path=%q", locator)
	}
	data, err := os.ReadFile(filepath.Join(root, locator))
	if err != nil || string(data) != "ciphertext" {
		t.Fatalf("sealed data=%q err=%v", data, err)
	}
	if err := store.Purge(locator); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, locator)); !os.IsNotExist(err) {
		t.Fatalf("purged locator stat error=%v", err)
	}
}

func TestExportStoreHasNonLockEntriesDoesNotCreateAnEmptyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	hasEntries, err := HasNonLockEntries(StoreConfig{Root: root})
	if err != nil || hasEntries {
		t.Fatalf("absent root entries=%v err=%v", hasEntries, err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty probe created root: %v", err)
	}

	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hasEntries, err = HasNonLockEntries(StoreConfig{Root: root})
	if err != nil || hasEntries {
		t.Fatalf("lock-only root entries=%v err=%v", hasEntries, err)
	}
	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Seal(staging); err != nil {
		t.Fatal(err)
	}
	hasEntries, err = HasNonLockEntries(StoreConfig{Root: root})
	if err != nil || !hasEntries {
		t.Fatalf("sealed root entries=%v err=%v", hasEntries, err)
	}
}

func TestExportStorePurgeBatchReturnsOnlyAfterLockedInventoryProvesAbsence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	locators := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		staging, createErr := store.CreateStaging(
			strings.Repeat(fmt.Sprintf("%x", index+1), 32),
			strings.Repeat(fmt.Sprintf("%x", index+3), 32),
		)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := staging.File.Write([]byte("ciphertext")); writeErr != nil {
			t.Fatal(writeErr)
		}
		locator, sealErr := store.Seal(staging)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		locators = append(locators, locator)
	}

	if err := store.PurgeBatch(locators); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != exportStoreLockName {
		t.Fatalf("post-purge inventory=%v", entries)
	}
	if err := store.PurgeBatch(locators); err != nil {
		t.Fatalf("idempotent purge: %v", err)
	}
}

func TestExportStorePurgeUnreferencedScansEntireDescriptorInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const objectCount = exportStoreInventoryBatch + 1
	referencedLocator := fmt.Sprintf("%032x.xre", objectCount-1)
	for index := 0; index < objectCount; index++ {
		locator := fmt.Sprintf("%032x.xre", index)
		if err := os.WriteFile(filepath.Join(root, locator), []byte("ciphertext"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	purged, err := store.PurgeUnreferenced(map[string]struct{}{referencedLocator: {}})
	if err != nil || purged != objectCount-1 {
		t.Fatalf("purge full inventory count=%d err=%v", purged, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != exportStoreLockName || entries[1].Name() != referencedLocator {
		t.Fatalf("retained inventory=%v", entries)
	}
}

func TestExportStoreMultiEntryPurgeKeepsTargetDescriptorsBounded(t *testing.T) {
	for _, operation := range []string{"purge batch", "purge unreferenced"} {
		t.Run(operation, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })

			const targetCount = 64
			locators := make([]string, 0, targetCount)
			targetIdentities := make(map[storeFileIdentity]struct{}, targetCount)
			for index := 0; index < targetCount; index++ {
				locator := fmt.Sprintf("%032x.xre", index)
				path := filepath.Join(root, locator)
				if err := os.WriteFile(path, []byte("ciphertext"), 0o600); err != nil {
					t.Fatal(err)
				}
				var stat unix.Stat_t
				if err := unix.Lstat(path, &stat); err != nil {
					t.Fatal(err)
				}
				locators = append(locators, locator)
				targetIdentities[storeFileIdentity{device: uint64(stat.Dev), inode: stat.Ino}] = struct{}{}
			}

			observedFDs := make(map[int]struct{}, targetCount)
			peakTargetFDs := 0
			store.openStoreEntryDescriptor = func(directory int, name string, how *unix.OpenHow) (int, error) {
				fd, openErr := unix.Openat2(directory, name, how)
				if openErr != nil {
					return fd, openErr
				}
				observedFDs[fd] = struct{}{}
				activeTargetFDs := 0
				for observedFD := range observedFDs {
					var stat unix.Stat_t
					if statErr := unix.Fstat(observedFD, &stat); errors.Is(statErr, unix.EBADF) {
						continue
					} else if statErr != nil {
						t.Fatalf("inspect observed purge descriptor %d: %v", observedFD, statErr)
					}
					if _, target := targetIdentities[storeFileIdentity{
						device: uint64(stat.Dev), inode: stat.Ino,
					}]; target {
						activeTargetFDs++
					}
				}
				peakTargetFDs = max(peakTargetFDs, activeTargetFDs)
				return fd, nil
			}

			switch operation {
			case "purge batch":
				err = store.PurgeBatch(locators)
			case "purge unreferenced":
				var purged int
				purged, err = store.PurgeUnreferenced(nil)
				if purged != targetCount {
					t.Errorf("purged=%d, want %d", purged, targetCount)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if peakTargetFDs > 1 {
				t.Fatalf("peak simultaneously open target descriptors=%d, want <=1", peakTargetFDs)
			}
		})
	}
}

func TestExportStoreSealsAuthenticatedItemSpoolWithoutPlaintext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	staging, err := store.CreateItemSpool(
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
	)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := bytes.Repeat([]byte("private-provider-bytes"), 16)
	dek := bytes.Repeat([]byte{4}, 32)
	binding := CipherBinding{
		ExportID:        "11111111111111111111111111111111",
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArchiveProfile:  "zip:balanced", FormatVersion: 1,
		AttemptFenceDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Purpose:            CipherPurposeItemSpool, ObjectID: "33333333333333333333333333333333",
	}
	result, err := EncryptStreamWithNonce(
		context.Background(), staging.File, bytes.NewReader(plaintext), dek, binding, 64, bytes.Repeat([]byte{8}, 8),
	)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(locator) != ".xrs" || filepath.Base(locator) != locator || staging.Locator() != locator {
		t.Fatalf("spool locator=%q staging=%q", locator, staging.Locator())
	}
	raw, err := os.ReadFile(filepath.Join(root, locator))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, plaintext) || int64(len(raw)) != result.CiphertextBytes {
		t.Fatalf("spool plaintext/cipher size leak len=%d result=%+v", len(raw), result)
	}
	reader, err := store.OpenSealed(locator)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close sealed spool: %v", err)
		}
	}()
	var decoded bytes.Buffer
	if _, err := DecryptStream(context.Background(), &decoded, reader, dek, binding); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Bytes(), plaintext) {
		t.Fatal("spool plaintext mismatch")
	}

	if err := store.Purge(locator); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, locator)); err != nil {
		t.Fatal(err)
	}
	if reader, err := store.OpenSealed(locator); !errors.Is(err, ErrInvalidStore) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("symlink sealed open error=%v", err)
	}
}

func TestExportStoreOpenSealedClassifiesAuthoritativeAbsence(t *testing.T) {
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	reader, err := store.OpenSealed(strings.Repeat("a", 32) + ".xre")
	if reader != nil {
		_ = reader.Close()
		t.Fatal("missing sealed object returned a reader")
	}
	if !errors.Is(err, ErrStoreObjectAbsent) || !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrInvalidStore) {
		t.Fatalf("missing sealed object error=%v, want authoritative absence", err)
	}
}

func TestExportStoreCloseWaitsForPinnedReader(t *testing.T) {
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	staging, err := store.CreateStaging(strings.Repeat("a", 32), strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("pinned ciphertext")); err != nil {
		t.Fatal(err)
	}
	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	reader, release, err := store.pinSealed(locator)
	if err != nil {
		t.Fatal(err)
	}
	if reader == nil {
		t.Fatal("pinned reader is nil")
	}

	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Store closed while reader remained pinned: %v", err)
	case <-time.After(time.Second):
	}
	if err := release(); err != nil {
		t.Fatalf("release pinned reader: %v", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close Store after reader release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Store close remained blocked after reader release")
	}
}

func TestExportStorePurgeUnreferencedWaitsOnlyForPinnedTargets(t *testing.T) {
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createSealed := func(exportID, attemptID, payload string) string {
		t.Helper()
		staging, err := store.CreateStaging(exportID, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := staging.File.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		locator, err := store.Seal(staging)
		if err != nil {
			t.Fatal(err)
		}
		return locator
	}
	pinnedLocator := createSealed(strings.Repeat("a", 32), strings.Repeat("b", 32), "pinned ciphertext")
	orphanLocator := createSealed(strings.Repeat("c", 32), strings.Repeat("d", 32), "orphan ciphertext")
	_, release, err := store.pinSealed(pinnedLocator)
	if err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeUnreferenced(map[string]struct{}{pinnedLocator: {}})
	if err != nil || purged != 1 {
		t.Fatalf("purge unreferenced while preserving pinned artifact purged=%d err=%v", purged, err)
	}
	if _, err := os.Stat(filepath.Join(store.root, orphanLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrelated orphan remained after purge: %v", err)
	}

	type purgeResult struct {
		count int
		err   error
	}
	purgeDone := make(chan purgeResult, 1)
	go func() {
		count, err := store.PurgeUnreferenced(nil)
		purgeDone <- purgeResult{count: count, err: err}
	}()
	select {
	case result := <-purgeDone:
		t.Fatalf("purge unreferenced completed while target remained pinned: %+v", result)
	case <-time.After(time.Second):
	}
	if err := release(); err != nil {
		t.Fatalf("release pinned orphan target: %v", err)
	}
	select {
	case result := <-purgeDone:
		if result.err != nil || result.count != 1 {
			t.Fatalf("purge unreferenced after target release=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("purge unreferenced remained blocked after target release")
	}
}

func TestExportStoreOpenSealedClassifiesUnsafeObjectSeparatelyFromRootFailure(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	locator := strings.Repeat("b", 32) + ".xre"
	external := filepath.Join(base, "external")
	if err := os.WriteFile(external, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(external, filepath.Join(root, locator)); err != nil {
		t.Fatal(err)
	}
	reader, err := store.OpenSealed(locator)
	if reader != nil {
		_ = reader.Close()
		t.Fatal("unsafe sealed object returned a reader")
	}
	if !errors.Is(err, ErrStoreObjectUnsafe) || !errors.Is(err, ErrInvalidStore) || errors.Is(err, ErrStoreObjectAbsent) {
		t.Fatalf("unsafe sealed object error=%v", err)
	}
}

func TestExportStoreProcessLockIsExclusiveAndReleasable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	first, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if second, err := OpenStore(StoreConfig{Root: root}); !errors.Is(err, ErrInvalidStore) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second process lock error=%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExportStorePrepareSchemaDownRequiresLockedEmptyInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var callbacks atomic.Int32
	down := func() error {
		callbacks.Add(1)
		return nil
	}
	if err := store.PrepareSchemaDown(down); err != nil || callbacks.Load() != 1 {
		t.Fatalf("empty root down error=%v callbacks=%d", err, callbacks.Load())
	}

	unknown := filepath.Join(root, "unknown")
	if err := os.WriteFile(unknown, []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareSchemaDown(down); !errors.Is(err, ErrInvalidStore) || callbacks.Load() != 1 {
		t.Fatalf("unknown root entry error=%v callbacks=%d", err, callbacks.Load())
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}

	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareSchemaDown(down); !errors.Is(err, ErrInvalidStore) || callbacks.Load() != 1 {
		t.Fatalf("owned root entry error=%v callbacks=%d", err, callbacks.Load())
	}
	if err := store.Purge(locator); err != nil {
		t.Fatal(err)
	}
	moved := root + ".moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareSchemaDown(down); !errors.Is(err, ErrInvalidStore) || callbacks.Load() != 1 {
		t.Fatalf("replaced root error=%v callbacks=%d", err, callbacks.Load())
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareSchemaDown(down); !errors.Is(err, ErrInvalidStore) || callbacks.Load() != 1 {
		t.Fatalf("closed store down error=%v callbacks=%d", err, callbacks.Load())
	}
}

var _ io.ReadCloser = (*os.File)(nil)

func TestExportStoreRejectsForbiddenAndSymlinkRoots(t *testing.T) {
	for _, root := range []string{"/data", "/backup", "/logs", "relative"} {
		if store, err := OpenStore(StoreConfig{Root: root}); err == nil {
			_ = store.Close()
			t.Fatalf("forbidden root %q accepted", root)
		}
	}
	dir := t.TempDir()
	realRoot := filepath.Join(dir, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(dir, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenStore(StoreConfig{Root: linkRoot}); err == nil {
		_ = store.Close()
		t.Fatal("symlink root accepted")
	}
}

func TestExportStoreOwnershipValidatorsUsePinnedEffectiveUID(t *testing.T) {
	pinnedUID := uint32(os.Geteuid())
	otherUID := pinnedUID + 1
	if otherUID == pinnedUID {
		otherUID = pinnedUID - 1
	}

	root := unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Nlink: 2, Uid: pinnedUID}
	if !validExportStoreOwnedRoot(root, pinnedUID, true) {
		t.Fatal("pinned owner-private root rejected")
	}
	root.Uid = otherUID
	if validExportStoreOwnedRoot(root, pinnedUID, false) {
		t.Fatal("foreign-owned root accepted before chmod")
	}

	object := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Dev: 7, Nlink: 0, Uid: pinnedUID}
	if !validExportStoreOwnedRegular(object, 7, pinnedUID, 0, true) {
		t.Fatal("pinned anonymous staging object rejected")
	}
	object.Uid = otherUID
	if validExportStoreOwnedRegular(object, 7, pinnedUID, 0, true) {
		t.Fatal("foreign-owned store object accepted")
	}
	object.Uid = pinnedUID
	object.Mode = unix.S_IFREG | 0o666
	if !validExportStoreOwnedRegular(object, 7, pinnedUID, 0, false) {
		t.Fatal("owner-matched pre-chmod object rejected")
	}
	if validExportStoreOwnedRegular(object, 7, pinnedUID, 0, true) {
		t.Fatal("non-private object accepted after validation")
	}
}

func TestExportStoreRejectsRenamedRootReplacementWithoutWritesOrDeletes(t *testing.T) {
	testExportStoreRejectsRootReplacement(t, false)
}

func TestExportStoreRejectsRootSymlinkReplacementWithoutExternalMutation(t *testing.T) {
	testExportStoreRejectsRootReplacement(t, true)
}

func testExportStoreRejectsRootReplacement(t *testing.T, symlinkReplacement bool) {
	t.Helper()
	for _, operation := range []string{
		"create staging",
		"create item spool",
		"seal",
		"discard staging",
		"open sealed",
		"purge",
		"purge batch",
		"purge unreferenced",
		"prepare schema down",
		"close",
	} {
		t.Run(operation, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })

			var staging StagingObject
			var locator string
			switch operation {
			case "seal", "discard staging":
				staging, err = store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := staging.File.Write([]byte("original ciphertext")); err != nil {
					t.Fatal(err)
				}
				locator = staging.Locator()
			case "open sealed", "purge", "purge batch":
				sealed, createErr := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
				if createErr != nil {
					t.Fatal(createErr)
				}
				if _, writeErr := sealed.File.Write([]byte("original sealed ciphertext")); writeErr != nil {
					t.Fatal(writeErr)
				}
				locator, err = store.Seal(sealed)
				if err != nil {
					t.Fatal(err)
				}
			case "purge unreferenced":
				locator = strings.Repeat("a", 32) + ".xre"
			}

			movedRoot := root + ".original"
			if err := os.Rename(root, movedRoot); err != nil {
				t.Fatal(err)
			}
			replacementRoot := root
			if symlinkReplacement {
				replacementRoot = filepath.Join(base, "external")
				if err := os.Mkdir(replacementRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(replacementRoot, root); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(replacementRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if symlinkReplacement {
					_ = os.Remove(root)
				} else {
					_ = os.RemoveAll(root)
				}
				if _, statErr := os.Lstat(movedRoot); statErr == nil {
					if renameErr := os.Rename(movedRoot, root); renameErr != nil {
						t.Errorf("restore original root: %v", renameErr)
					}
				}
				_ = store.Close()
			})

			if err := os.WriteFile(filepath.Join(replacementRoot, exportStoreLockName), []byte("replacement lock"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(replacementRoot, "keep"), []byte("replacement sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "seal", "discard staging":
				if err := os.WriteFile(
					filepath.Join(replacementRoot, ".stage-"+strings.Repeat("f", 32)),
					[]byte("replacement staging ciphertext"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "open sealed", "purge", "purge batch", "purge unreferenced":
				if err := os.WriteFile(filepath.Join(replacementRoot, locator), []byte("replacement sealed ciphertext"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			beforeOriginal := snapshotExportStoreTree(t, movedRoot)
			beforeReplacement := snapshotExportStoreTree(t, replacementRoot)
			var operationErr error
			switch operation {
			case "create staging":
				created, createErr := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
				operationErr = createErr
				if created.File != nil {
					_ = created.File.Close()
				}
			case "create item spool":
				created, createErr := store.CreateItemSpool(
					strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32),
				)
				operationErr = createErr
				if created.File != nil {
					_ = created.File.Close()
				}
			case "seal":
				_, operationErr = store.Seal(staging)
			case "discard staging":
				operationErr = store.DiscardStaging(staging)
			case "open sealed":
				var reader *os.File
				reader, operationErr = store.OpenSealed(locator)
				if reader != nil {
					_ = reader.Close()
				}
			case "purge":
				operationErr = store.Purge(locator)
			case "purge batch":
				operationErr = store.PurgeBatch([]string{locator})
			case "purge unreferenced":
				_, operationErr = store.PurgeUnreferenced(nil)
			case "prepare schema down":
				var callbacks atomic.Int32
				operationErr = store.PrepareSchemaDown(func() error {
					callbacks.Add(1)
					return nil
				})
				if callbacks.Load() != 0 {
					t.Errorf("schema-down callback count=%d", callbacks.Load())
				}
			case "close":
				operationErr = store.Close()
			}
			if !errors.Is(operationErr, ErrInvalidStore) {
				t.Errorf("%s error=%v, want ErrInvalidStore", operation, operationErr)
			}
			assertExportStoreTreeUnchanged(t, movedRoot, beforeOriginal)
			assertExportStoreTreeUnchanged(t, replacementRoot, beforeReplacement)
		})
	}
}

func TestExportStoreStagingIsAnonymousAndDiscardOnlyClosesDescriptor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	before := snapshotExportStoreTree(t, root)
	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	assertExportStoreTreeUnchanged(t, root, before)
	var stat unix.Stat_t
	if err := unix.Fstat(int(staging.File.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Nlink != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Errorf("anonymous staging stat=%+v", stat)
	}
	if _, err := staging.File.Write([]byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	if err := store.DiscardStaging(staging); err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("discarded staging descriptor error=%v", err)
	}
	assertExportStoreTreeUnchanged(t, root, before)
}

func TestExportStoreSealDoesNotReplaceExistingFinalLocator(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("new ciphertext")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, staging.Locator()), []byte("existing ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotExportStoreTree(t, root)

	if _, err := store.Seal(staging); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("seal existing final locator error=%v", err)
	}
	assertExportStoreTreeUnchanged(t, root, before)
}

func TestExportStoreSealPreservesPublishedOrphanWhenPostLinkFileSyncFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := []byte("post-link file-sync orphan")
	if _, err := staging.File.Write(ciphertext); err != nil {
		t.Fatal(err)
	}
	var staged unix.Stat_t
	if err := unix.Fstat(int(staging.File.Fd()), &staged); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(root, "unrelated-sentinel")
	if err := os.WriteFile(sentinelPath, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeSentinel := snapshotExportStoreTree(t, root)[filepath.Base(sentinelPath)]
	stagingFD := int(staging.File.Fd())
	var fileSyncCalls atomic.Int32
	store.syncPublishedFile = func(fd int) error {
		if fd != stagingFD {
			t.Errorf("published file fd=%d, want %d", fd, stagingFD)
		}
		fileSyncCalls.Add(1)
		return unix.EIO
	}
	var directorySyncCalls atomic.Int32
	store.syncPublishedDirectory = func(fd int) error {
		directorySyncCalls.Add(1)
		return unix.Fsync(fd)
	}
	expectedLocator := staging.Locator()

	locator, err := store.Seal(staging)
	if !errors.Is(err, ErrInvalidStore) || !errors.Is(err, unix.EIO) || locator != "" {
		t.Fatalf("post-link file-sync failure locator=%q err=%v", locator, err)
	}
	if fileSyncCalls.Load() != 1 || directorySyncCalls.Load() != 0 {
		t.Fatalf(
			"post-link sync calls file=%d directory=%d, want file=1 directory=0",
			fileSyncCalls.Load(), directorySyncCalls.Load(),
		)
	}
	if _, err := staging.File.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("post-link file-sync failure staging descriptor error=%v", err)
	}
	sealedPath := filepath.Join(root, expectedLocator)
	sealed, err := os.ReadFile(sealedPath)
	if err != nil || !bytes.Equal(sealed, ciphertext) {
		t.Fatalf("published orphan ciphertext=%q err=%v", sealed, err)
	}
	var published unix.Stat_t
	if err := unix.Lstat(sealedPath, &published); err != nil {
		t.Fatal(err)
	}
	if !sameExportStoreFile(staged, published) || published.Nlink != 1 {
		t.Fatalf("published orphan stat=%+v staged=%+v", published, staged)
	}
	afterSentinel := snapshotExportStoreTree(t, root)[filepath.Base(sentinelPath)]
	if afterSentinel != beforeSentinel {
		t.Fatalf("unrelated sentinel changed before=%q after=%q", beforeSentinel, afterSentinel)
	}
}

func TestExportStoreSealPreservesPublishedOrphanWhenDirectorySyncFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := []byte("authenticated orphan ciphertext")
	if _, err := staging.File.Write(ciphertext); err != nil {
		t.Fatal(err)
	}
	var staged unix.Stat_t
	if err := unix.Fstat(int(staging.File.Fd()), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Nlink != 0 {
		t.Fatalf("pre-publication staging nlink=%d, want 0", staged.Nlink)
	}
	sentinelPath := filepath.Join(root, "unrelated-sentinel")
	if err := os.WriteFile(sentinelPath, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeSentinel := snapshotExportStoreTree(t, root)[filepath.Base(sentinelPath)]
	var syncCalls atomic.Int32
	store.syncPublishedDirectory = func(fd int) error {
		if fd != int(store.directory.Fd()) {
			t.Errorf("published directory fd=%d, want %d", fd, store.directory.Fd())
		}
		syncCalls.Add(1)
		return unix.EIO
	}
	expectedLocator := staging.Locator()

	locator, err := store.Seal(staging)
	if !errors.Is(err, ErrInvalidStore) || !errors.Is(err, unix.EIO) || locator != "" {
		t.Fatalf("post-link sync failure locator=%q err=%v", locator, err)
	}
	if syncCalls.Load() != 1 {
		t.Fatalf("post-link directory sync calls=%d, want 1", syncCalls.Load())
	}
	if _, err := staging.File.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("post-link failure staging descriptor error=%v", err)
	}
	sealedPath := filepath.Join(root, expectedLocator)
	sealed, err := os.ReadFile(sealedPath)
	if err != nil || !bytes.Equal(sealed, ciphertext) {
		t.Fatalf("published orphan ciphertext=%q err=%v", sealed, err)
	}
	var published unix.Stat_t
	if err := unix.Lstat(sealedPath, &published); err != nil {
		t.Fatal(err)
	}
	if !sameExportStoreFile(staged, published) || published.Nlink != 1 {
		t.Fatalf("published orphan stat=%+v staged=%+v", published, staged)
	}
	afterSentinel := snapshotExportStoreTree(t, root)[filepath.Base(sentinelPath)]
	if afterSentinel != beforeSentinel {
		t.Fatalf("unrelated sentinel changed before=%q after=%q", beforeSentinel, afterSentinel)
	}
}

func TestExportStoreSealFallsBackToProcDescriptorLinkOnlyAfterENOENT(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := []byte("fallback-linked ciphertext")
	if _, err := staging.File.Write(ciphertext); err != nil {
		t.Fatal(err)
	}
	type linkCall struct {
		oldDirectory int
		oldPath      string
		newDirectory int
		newPath      string
		flags        int
	}
	calls := make([]linkCall, 0, 2)
	stagingFD := int(staging.File.Fd())
	rootFD := int(store.directory.Fd())
	store.linkStagingDescriptor = func(oldDirectory int, oldPath string, newDirectory int, newPath string, flags int) error {
		calls = append(calls, linkCall{
			oldDirectory: oldDirectory, oldPath: oldPath,
			newDirectory: newDirectory, newPath: newPath, flags: flags,
		})
		if len(calls) == 1 {
			return unix.ENOENT
		}
		return unix.Linkat(oldDirectory, oldPath, newDirectory, newPath, flags)
	}

	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []linkCall{
		{oldDirectory: stagingFD, oldPath: "", newDirectory: rootFD, newPath: locator, flags: unix.AT_EMPTY_PATH},
		{
			oldDirectory: unix.AT_FDCWD, oldPath: fmt.Sprintf("/proc/self/fd/%d", stagingFD),
			newDirectory: rootFD, newPath: locator, flags: unix.AT_SYMLINK_FOLLOW,
		},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("descriptor link calls=%+v, want %+v", calls, wantCalls)
	}
	sealed, err := os.ReadFile(filepath.Join(root, locator))
	if err != nil || !bytes.Equal(sealed, ciphertext) {
		t.Fatalf("fallback-linked ciphertext=%q err=%v", sealed, err)
	}
}

func TestExportStoreSealDoesNotFallBackAfterNonENOENTLinkErrors(t *testing.T) {
	for _, injected := range []error{
		unix.EPERM, unix.EACCES, unix.EINVAL, unix.EEXIST, unix.EXDEV, unix.EIO,
	} {
		t.Run(injected.Error(), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := staging.File.Write([]byte("ciphertext")); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			store.linkStagingDescriptor = func(int, string, int, string, int) error {
				calls.Add(1)
				return injected
			}
			expectedLocator := staging.Locator()

			locator, err := store.Seal(staging)
			if !errors.Is(err, ErrInvalidStore) || !errors.Is(err, injected) || locator != "" {
				t.Fatalf("link error=%v locator=%q result=%v", injected, locator, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("link error=%v calls=%d, want 1", injected, calls.Load())
			}
			if _, err := staging.File.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("link error=%v staging descriptor error=%v", injected, err)
			}
			if _, err := os.Lstat(filepath.Join(root, expectedLocator)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("link error=%v final entry error=%v", injected, err)
			}
		})
	}
}

func TestExportStoreSealAndDiscardRejectLinkedAnonymousDescriptor(t *testing.T) {
	for _, operation := range []string{"seal", "discard"} {
		t.Run(operation, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := staging.File.Write([]byte("ciphertext")); err != nil {
				t.Fatal(err)
			}
			var anonymous unix.Stat_t
			if err := unix.Fstat(int(staging.File.Fd()), &anonymous); err != nil {
				t.Fatal(err)
			}
			if anonymous.Nlink != 0 {
				t.Errorf("pre-link staging nlink=%d, want 0", anonymous.Nlink)
			}
			external := filepath.Join(base, "external")
			if err := os.Mkdir(external, 0o700); err != nil {
				t.Fatal(err)
			}
			linkStagingDescriptorForTest(t, staging.File, external, "linked")
			var linked unix.Stat_t
			if err := unix.Fstat(int(staging.File.Fd()), &linked); err != nil {
				t.Fatal(err)
			}
			if linked.Nlink != 1 {
				t.Errorf("linked staging nlink=%d, want 1", linked.Nlink)
			}
			beforeRoot := snapshotExportStoreTree(t, root)
			beforeExternal := snapshotExportStoreTree(t, external)

			if operation == "seal" {
				_, err = store.Seal(staging)
			} else {
				err = store.DiscardStaging(staging)
			}
			if !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("%s staging hardlink error=%v", operation, err)
			}
			assertExportStoreTreeUnchanged(t, root, beforeRoot)
			assertExportStoreTreeUnchanged(t, external, beforeExternal)
		})
	}
}

func TestExportStoreSealsAnonymousDescriptorWithoutMutatingMaliciousStageEntries(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
			if err != nil {
				t.Fatal(err)
			}
			ciphertext := []byte("authenticated ciphertext")
			if _, err := staging.File.Write(ciphertext); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(base, "external")
			if err := os.Mkdir(external, 0o700); err != nil {
				t.Fatal(err)
			}
			maliciousName := ".stage-" + strings.Repeat("a", 32)
			maliciousPath := filepath.Join(root, maliciousName)
			target := filepath.Join(external, "target")
			if err := os.WriteFile(target, []byte("external sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "regular":
				err = os.WriteFile(maliciousPath, []byte("malicious staging"), 0o600)
			case "symlink":
				err = os.Symlink(target, maliciousPath)
			case "hardlink":
				err = os.Link(target, maliciousPath)
			case "directory":
				err = os.Mkdir(maliciousPath, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeRoot := snapshotExportStoreTree(t, root)
			beforeMalicious, found := beforeRoot[maliciousName]
			if !found {
				t.Fatal("malicious staging entry missing before seal")
			}
			beforeExternal := snapshotExportStoreTree(t, external)

			locator, err := store.Seal(staging)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := os.ReadFile(filepath.Join(root, locator))
			if err != nil || !bytes.Equal(sealed, ciphertext) {
				t.Fatalf("sealed ciphertext=%q err=%v", sealed, err)
			}
			afterMalicious, found := snapshotExportStoreTree(t, root)[maliciousName]
			if !found || afterMalicious != beforeMalicious {
				t.Errorf("malicious staging entry changed before=%q after=%q", beforeMalicious, afterMalicious)
			}
			assertExportStoreTreeUnchanged(t, external, beforeExternal)
		})
	}
}

func TestExportStorePurgeUnreferencedRejectsMaliciousNamedStageWithoutMutation(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := staging.File.Write([]byte("sealed orphan")); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Seal(staging); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(base, "external")
			if err := os.Mkdir(external, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(external, "target")
			if err := os.WriteFile(target, []byte("external sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			maliciousPath := filepath.Join(root, ".stage-"+strings.Repeat("b", 32))
			switch kind {
			case "regular":
				err = os.WriteFile(maliciousPath, []byte("malicious staging"), 0o600)
			case "symlink":
				err = os.Symlink(target, maliciousPath)
			case "hardlink":
				err = os.Link(target, maliciousPath)
			case "directory":
				err = os.Mkdir(maliciousPath, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeRoot := snapshotExportStoreTree(t, root)
			beforeExternal := snapshotExportStoreTree(t, external)

			purged, err := store.PurgeUnreferenced(nil)
			if !errors.Is(err, ErrInvalidStore) || purged != 0 {
				t.Errorf("purge malicious named staging count=%d err=%v", purged, err)
			}
			assertExportStoreTreeUnchanged(t, root, beforeRoot)
			assertExportStoreTreeUnchanged(t, external, beforeExternal)
		})
	}
}

func TestExportStoreRejectsSealedSpecialEntriesWithoutMutation(t *testing.T) {
	for _, operation := range []string{"open", "purge", "purge batch", "purge unreferenced"} {
		t.Run(operation+" sealed directory", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			locator := strings.Repeat("a", 32) + ".xre"
			if err := os.Mkdir(filepath.Join(root, locator), 0o700); err != nil {
				t.Fatal(err)
			}
			before := snapshotExportStoreTree(t, root)

			switch operation {
			case "open":
				var reader *os.File
				reader, err = store.OpenSealed(locator)
				if reader != nil {
					_ = reader.Close()
				}
			case "purge":
				err = store.Purge(locator)
			case "purge batch":
				err = store.PurgeBatch([]string{locator})
			case "purge unreferenced":
				_, err = store.PurgeUnreferenced(nil)
			}
			if !errors.Is(err, ErrInvalidStore) {
				t.Errorf("%s sealed directory error=%v, want ErrInvalidStore", operation, err)
			}
			assertExportStoreTreeUnchanged(t, root, before)
		})
	}
}

func TestExportStoreRejectsSealedSymlinkWithoutExternalMutation(t *testing.T) {
	testExportStoreRejectsSealedLink(t, true)
}

func TestExportStoreRejectsSealedHardlinkWithoutExternalMutation(t *testing.T) {
	testExportStoreRejectsSealedLink(t, false)
}

func testExportStoreRejectsSealedLink(t *testing.T, symlink bool) {
	t.Helper()
	for _, operation := range []string{"open", "purge", "purge batch", "purge unreferenced"} {
		t.Run(operation, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "exports")
			store, err := OpenStore(StoreConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			external := filepath.Join(base, "external")
			if err := os.Mkdir(external, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(external, "ciphertext")
			if err := os.WriteFile(target, []byte("external ciphertext"), 0o600); err != nil {
				t.Fatal(err)
			}
			locator := strings.Repeat("a", 32) + ".xre"
			entry := filepath.Join(root, locator)
			if symlink {
				err = os.Symlink(target, entry)
			} else {
				err = os.Link(target, entry)
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeRoot := snapshotExportStoreTree(t, root)
			beforeExternal := snapshotExportStoreTree(t, external)

			switch operation {
			case "open":
				var reader *os.File
				reader, err = store.OpenSealed(locator)
				if reader != nil {
					_ = reader.Close()
				}
			case "purge":
				err = store.Purge(locator)
			case "purge batch":
				err = store.PurgeBatch([]string{locator})
			case "purge unreferenced":
				_, err = store.PurgeUnreferenced(nil)
			}
			if !errors.Is(err, ErrInvalidStore) {
				t.Errorf("%s sealed link error=%v, want ErrInvalidStore", operation, err)
			}
			assertExportStoreTreeUnchanged(t, root, beforeRoot)
			assertExportStoreTreeUnchanged(t, external, beforeExternal)
		})
	}
}

func snapshotExportStoreTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("missing stat for %s", relative)
		}
		value := fmt.Sprintf(
			"mode=%s size=%d dev=%d inode=%d nlink=%d",
			info.Mode(), info.Size(), stat.Dev, stat.Ino, stat.Nlink,
		)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += " target=" + target
		case info.Mode().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" contents=%x", contents)
		}
		snapshot[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertExportStoreTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotExportStoreTree(t, root)
	if !reflect.DeepEqual(after, before) {
		t.Errorf("store tree %q changed\nbefore=%v\nafter=%v", root, before, after)
	}
}

func linkStagingDescriptorForTest(t *testing.T, file *os.File, directory, name string) {
	t.Helper()
	directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Close(directoryFD); err != nil {
			t.Errorf("close descriptor-link directory: %v", err)
		}
	})
	if err := unix.Linkat(int(file.Fd()), "", directoryFD, name, unix.AT_EMPTY_PATH); err != nil {
		if !errors.Is(err, unix.ENOENT) {
			t.Fatalf("link staging descriptor directly: %v", err)
		}
		procPath := fmt.Sprintf("/proc/self/fd/%d", file.Fd())
		if err := unix.Linkat(unix.AT_FDCWD, procPath, directoryFD, name, unix.AT_SYMLINK_FOLLOW); err != nil {
			t.Fatalf("link staging descriptor through procfs: %v", err)
		}
	}
}
