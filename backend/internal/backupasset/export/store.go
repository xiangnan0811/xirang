package export

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	exportStoreLockName            = ".xirang-export.lock"
	exportStoreInventoryBatch      = 256
	exportStoreMaxInventoryEntries = 1 << 20
)

const (
	exportStoreRootResolve  = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS
	exportStoreChildResolve = exportStoreRootResolve | unix.RESOLVE_NO_XDEV
)

var (
	ErrStoreObjectAbsent = errors.New("export store object is absent")
	ErrStoreObjectUnsafe = errors.New("export store object is unsafe")
)

type StoreConfig struct {
	Root string
}

type storeRootIdentity struct {
	device  uint64
	inode   uint64
	mountID uint64
}

type storeFileIdentity struct {
	device uint64
	inode  uint64
}

type Store struct {
	root            string
	ownerUID        uint32
	rootIdentity    storeRootIdentity
	lockIdentity    storeFileIdentity
	publicationGate sync.RWMutex
	mu              sync.Mutex
	pins            map[string]uint
	pinChanged      *sync.Cond
	directory       *os.File
	lock            *os.File

	linkStagingDescriptor    func(int, string, int, string, int) error
	openStoreEntryDescriptor func(int, string, *unix.OpenHow) (int, error)
	syncPublishedDirectory   func(int) error
	syncPublishedFile        func(int) error
	// beforePublicationSweepGate is a package-test synchronization hook.
	beforePublicationSweepGate func()
}

type StagingObject struct {
	File         *os.File
	finalLocator string
	identity     storeFileIdentity
	store        *Store
}

type openedStoreEntry struct {
	name string
	file *os.File
	stat unix.Stat_t
}

func (staging StagingObject) Locator() string { return staging.finalLocator }

// HasNonLockEntries checks an existing configured root without creating a
// directory or lock. Disabled runtime maintenance uses it to decide whether a
// locked Store must be opened to remove orphaned ciphertext.
func HasNonLockEntries(config StoreConfig) (result bool, resultErr error) {
	ownerUID := uint32(os.Geteuid())
	root := filepath.Clean(config.Root)
	if !filepath.IsAbs(root) || forbiddenStoreRoot(root) {
		return false, ErrInvalidStore
	}
	directory, identity, err := openExportStoreRootPath(root, false, ownerUID)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, exportStoreSystemError("open root maintenance probe", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, exportStoreSystemError("close root maintenance probe", directory.Close()))
	}()
	if err := verifyExportStoreRootPath(root, directory, identity, ownerUID, true); err != nil {
		return false, err
	}
	probe := &Store{root: root, ownerUID: ownerUID, rootIdentity: identity, directory: directory}
	names, err := probe.inventoryNamesLocked(exportStoreMaxInventoryEntries)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		if name != exportStoreLockName {
			return true, nil
		}
	}
	return false, nil
}

func OpenStore(config StoreConfig) (result *Store, resultErr error) {
	ownerUID := uint32(os.Geteuid())
	root := filepath.Clean(config.Root)
	if !filepath.IsAbs(root) || forbiddenStoreRoot(root) {
		return nil, ErrInvalidStore
	}

	directory, identity, err := openExportStoreRootPath(root, true, ownerUID)
	if err != nil {
		return nil, exportStoreSystemError("open root", err)
	}
	var lock *os.File
	locked := false
	defer func() {
		if result != nil {
			return
		}
		if locked && lock != nil {
			resultErr = errors.Join(resultErr, exportStoreSystemError(
				"unlock after open failure", unix.Flock(int(lock.Fd()), unix.LOCK_UN),
			))
		}
		if lock != nil {
			resultErr = errors.Join(resultErr, exportStoreSystemError("close lock after open failure", lock.Close()))
		}
		resultErr = errors.Join(resultErr, exportStoreSystemError("close root after open failure", directory.Close()))
	}()

	if err := verifyExportStoreRootPath(root, directory, identity, ownerUID, false); err != nil {
		return nil, err
	}
	if err := unix.Fchmod(int(directory.Fd()), 0o700); err != nil {
		return nil, exportStoreSystemError("restrict root mode", err)
	}
	if err := unix.Fsync(int(directory.Fd())); err != nil {
		return nil, exportStoreSystemError("sync root mode", err)
	}
	if err := verifyExportStoreRootPath(root, directory, identity, ownerUID, true); err != nil {
		return nil, err
	}
	lockFD, err := unix.Openat2(int(directory.Fd()), exportStoreLockName, &unix.OpenHow{
		Flags:   unix.O_RDWR | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Mode:    0o600,
		Resolve: exportStoreChildResolve,
	})
	if err != nil {
		return nil, exportStoreSystemError("open lock", err)
	}
	lock = os.NewFile(uintptr(lockFD), "export-store-lock")
	if lock == nil {
		return nil, errors.Join(
			exportStoreInvariantError("wrap lock descriptor"),
			exportStoreSystemError("close unwrapped lock descriptor", unix.Close(lockFD)),
		)
	}
	var lockStat unix.Stat_t
	if err := unix.Fstat(lockFD, &lockStat); err != nil {
		return nil, exportStoreSystemError("inspect lock", err)
	}
	if !validExportStoreOwnedRegular(lockStat, identity.device, ownerUID, 1, false) {
		return nil, exportStoreInvariantError("unsafe lock entry")
	}
	if err := unix.Fchmod(lockFD, 0o600); err != nil {
		return nil, exportStoreSystemError("restrict lock mode", err)
	}
	if err := unix.Fstat(lockFD, &lockStat); err != nil {
		return nil, exportStoreSystemError("reinspect restricted lock", err)
	}
	if !validExportStoreOwnedRegular(lockStat, identity.device, ownerUID, 1, true) {
		return nil, exportStoreInvariantError("unsafe lock entry")
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, exportStoreSystemError("acquire process lock", err)
	}
	locked = true

	store := &Store{
		root:                     root,
		ownerUID:                 ownerUID,
		rootIdentity:             identity,
		lockIdentity:             storeFileIdentity{device: uint64(lockStat.Dev), inode: lockStat.Ino},
		directory:                directory,
		lock:                     lock,
		linkStagingDescriptor:    unix.Linkat,
		openStoreEntryDescriptor: unix.Openat2,
		syncPublishedDirectory:   unix.Fsync,
		syncPublishedFile:        unix.Fsync,
	}
	store.pins = make(map[string]uint)
	store.pinChanged = sync.NewCond(&store.mu)
	if err := store.verifyRootIdentityLocked(); err != nil {
		return nil, err
	}
	if err := unix.Fsync(int(directory.Fd())); err != nil {
		return nil, exportStoreSystemError("sync locked root", err)
	}
	if err := store.verifyRootIdentityLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *Store) CreateStaging(exportID, attemptID string) (StagingObject, error) {
	return store.createStaging(exportID, attemptID, "", ".xre")
}

func (store *Store) CreateItemSpool(exportID, attemptID, itemAttemptID string) (StagingObject, error) {
	return store.createStaging(exportID, attemptID, itemAttemptID, ".xrs")
}

func (store *Store) createStaging(exportID, attemptID, itemAttemptID, suffix string) (StagingObject, error) {
	if store == nil || !lowerHex(exportID, 32) || !lowerHex(attemptID, 32) ||
		(suffix == ".xrs" && !lowerHex(itemAttemptID, 32)) || (suffix != ".xrs" && itemAttemptID != "") {
		return StagingObject{}, ErrInvalidStore
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return StagingObject{}, fmt.Errorf("generate export store locator: %w", err)
	}
	opaque := hex.EncodeToString(identifier)

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.verifyRootIdentityLocked(); err != nil {
		return StagingObject{}, err
	}
	fd, err := unix.Openat2(int(store.directory.Fd()), ".", &unix.OpenHow{
		Flags:   unix.O_WRONLY | unix.O_TMPFILE | unix.O_CLOEXEC,
		Mode:    0o600,
		Resolve: exportStoreChildResolve,
	})
	if err != nil {
		return StagingObject{}, exportStoreSystemError("create staging object", err)
	}
	file := os.NewFile(uintptr(fd), "export-store-staging")
	if file == nil {
		return StagingObject{}, errors.Join(
			exportStoreInvariantError("wrap staging descriptor"),
			exportStoreSystemError("close unwrapped staging descriptor", unix.Close(fd)),
		)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return StagingObject{}, errors.Join(
			exportStoreSystemError("inspect staging object", err),
			exportStoreSystemError("close invalid staging object", file.Close()),
		)
	}
	if !validExportStoreOwnedRegular(stat, store.rootIdentity.device, store.ownerUID, 0, true) {
		return StagingObject{}, errors.Join(
			exportStoreInvariantError("unsafe staging object"),
			exportStoreSystemError("close unsafe staging object", file.Close()),
		)
	}
	if err := store.verifyRootIdentityLocked(); err != nil {
		return StagingObject{}, errors.Join(err, exportStoreSystemError("close staging after root change", file.Close()))
	}
	return StagingObject{
		File: file, finalLocator: opaque + suffix,
		identity: storeFileIdentity{device: uint64(stat.Dev), inode: stat.Ino}, store: store,
	}, nil
}

func (store *Store) Seal(staging StagingObject) (string, error) {
	locator, releasePublication, err := store.sealWithPublicationPin(staging)
	if err != nil {
		return "", err
	}
	releasePublication()
	return locator, nil
}

// sealWithPublicationPin prevents an unreferenced sweep from observing a
// durable file before its owning DB transaction has recorded the locator.
func (store *Store) sealWithPublicationPin(staging StagingObject) (locator string, release func(), resultErr error) {
	if store == nil || !store.validStagingObject(staging) {
		return "", nil, ErrInvalidStore
	}
	store.publicationGate.RLock()
	publicationGateHeld := true
	defer func() {
		if resultErr != nil && publicationGateHeld {
			store.publicationGate.RUnlock()
		}
	}()
	store.mu.Lock()
	defer store.mu.Unlock()
	defer func() {
		if closeErr := staging.File.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, exportStoreSystemError("close staging after seal", closeErr))
			if release != nil {
				store.releasePinnedLocatorLocked(staging.finalLocator)
				release = nil
			}
			locator = ""
		}
	}()

	if err := store.verifyRootIdentityLocked(); err != nil {
		return "", nil, err
	}
	if _, err := store.verifyAnonymousStagingLocked(staging); err != nil {
		return "", nil, err
	}
	if err := staging.File.Sync(); err != nil {
		return "", nil, exportStoreSystemError("sync staging object", err)
	}
	stagingStat, err := store.verifyAnonymousStagingLocked(staging)
	if err != nil {
		return "", nil, err
	}
	if err := store.verifyRootIdentityLocked(); err != nil {
		return "", nil, err
	}
	if err := store.linkAnonymousStagingLocked(staging.File, staging.finalLocator); err != nil {
		return "", nil, exportStoreSystemError("publish staging object", err)
	}
	if err := store.verifyPublishedEntryLocked(staging.finalLocator, staging.File, stagingStat); err != nil {
		return "", nil, err
	}
	if err := store.syncPublishedFile(int(staging.File.Fd())); err != nil {
		return "", nil, exportStoreSystemError("sync published file", err)
	}
	if err := store.syncPublishedDirectory(int(store.directory.Fd())); err != nil {
		return "", nil, exportStoreSystemError("sync published object", err)
	}
	if err := store.verifyPublishedEntryLocked(staging.finalLocator, staging.File, stagingStat); err != nil {
		return "", nil, err
	}
	if err := store.verifyRootIdentityLocked(); err != nil {
		return "", nil, err
	}
	store.pins[staging.finalLocator]++
	var once sync.Once
	release = func() {
		once.Do(func() {
			store.mu.Lock()
			defer store.mu.Unlock()
			store.releasePinnedLocatorLocked(staging.finalLocator)
			store.publicationGate.RUnlock()
		})
	}
	return staging.finalLocator, release, nil
}

func (store *Store) DiscardStaging(staging StagingObject) (resultErr error) {
	if store == nil || !store.validStagingObject(staging) {
		return ErrInvalidStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	defer func() {
		resultErr = errors.Join(resultErr, exportStoreSystemError("close discarded staging object", staging.File.Close()))
	}()

	if err := store.verifyRootIdentityLocked(); err != nil {
		return err
	}
	if _, err := store.verifyAnonymousStagingLocked(staging); err != nil {
		return err
	}
	return store.verifyRootIdentityLocked()
}

func (store *Store) OpenSealed(locator string) (*os.File, error) {
	if store == nil || !validStoreLocator(locator) {
		return nil, ErrInvalidStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.openSealedLocked(locator)
}

// pinSealed keeps the opened object's locator pinned until the returned release
// function is called. Other locators remain available to Store operations.
func (store *Store) pinSealed(locator string) (*os.File, func() error, error) {
	if store == nil || !validStoreLocator(locator) {
		return nil, nil, ErrInvalidStore
	}
	store.mu.Lock()
	reader, err := store.openSealedLocked(locator)
	if err != nil {
		store.mu.Unlock()
		return nil, nil, err
	}
	store.pins[locator]++
	store.mu.Unlock()
	var once sync.Once
	var closeErr error
	release := func() error {
		once.Do(func() {
			store.mu.Lock()
			defer store.mu.Unlock()
			defer store.releasePinnedLocatorLocked(locator)
			closeErr = reader.Close()
		})
		return closeErr
	}
	return reader, release, nil
}

func (store *Store) openSealedLocked(locator string) (*os.File, error) {
	if err := store.verifyRootIdentityLocked(); err != nil {
		return nil, err
	}
	entry, exists, err := store.openRegularEntryLocked(locator)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := store.verifyRootIdentityLocked(); err != nil {
			return nil, err
		}
		return nil, errors.Join(ErrStoreObjectAbsent, os.ErrNotExist)
	}
	if err := store.verifyRootIdentityLocked(); err != nil {
		return nil, errors.Join(err, exportStoreSystemError("close sealed object after root change", entry.file.Close()))
	}
	if err := store.verifyOpenedEntryLocked(entry); err != nil {
		closeErr := exportStoreSystemError("close changed sealed object", entry.file.Close())
		if errors.Is(err, ErrStoreObjectAbsent) {
			if identityErr := store.verifyRootIdentityLocked(); identityErr != nil {
				return nil, errors.Join(identityErr, closeErr)
			}
		}
		return nil, errors.Join(err, closeErr)
	}
	return entry.file, nil
}

func (store *Store) Purge(locator string) error {
	return store.PurgeBatch([]string{locator})
}

// PurgeBatch returns only after every named object has been unlinked, the
// parent directory has been synced, and a locked inventory proves absence.
// The name-based unlinkat sequence relies on the pinned-euid-owned 0700 root,
// Store mutex, locator pins, and advisory process lock to exclude other UIDs
// and cooperating writers. A hostile same-credential writer is process
// compromise outside this store's concurrency contract.
func (store *Store) PurgeBatch(locators []string) error {
	if store == nil {
		return ErrInvalidStore
	}
	unique := make([]string, 0, len(locators))
	seen := make(map[string]struct{}, len(locators))
	for _, locator := range locators {
		if !validStoreLocator(locator) {
			return ErrInvalidStore
		}
		if _, duplicate := seen[locator]; duplicate {
			continue
		}
		seen[locator] = struct{}{}
		unique = append(unique, locator)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.verifyRootIdentityLocked(); err != nil {
		return err
	}
	store.waitForLocatorsUnpinnedLocked(unique)
	if err := store.verifyRootIdentityLocked(); err != nil {
		return err
	}
	if err := store.validateStoreEntriesLocked(
		unique, false, "close purge validation object",
	); err != nil {
		return err
	}
	_, err := store.unlinkEntriesLocked(unique, unique)
	return err
}

func (store *Store) PurgeUnreferenced(referenced map[string]struct{}) (purged int, resultErr error) {
	if store == nil {
		return 0, ErrInvalidStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for {
		if err := store.verifyRootIdentityLocked(); err != nil {
			return 0, err
		}
		names, err := store.inventoryNamesLocked(exportStoreMaxInventoryEntries)
		if err != nil {
			return 0, err
		}
		entries := make([]string, 0, len(names))
		targets := make([]string, 0, len(names))
		for _, name := range names {
			if name == exportStoreLockName {
				continue
			}
			_, keep := referenced[name]
			if !validStoreLocator(name) {
				return 0, exportStoreInvariantError("invalid inventory entry")
			}
			entries = append(entries, name)
			if !keep {
				targets = append(targets, name)
			}
		}
		if store.hasPinnedLocatorsLocked(targets) {
			store.pinChanged.Wait()
			continue
		}
		if err := store.validateStoreEntriesLocked(
			entries, true, "close orphan validation object",
		); err != nil {
			return 0, err
		}
		return store.unlinkEntriesLocked(targets, targets)
	}
}

// PrepareSchemaDown proves that the still-locked Export root contains no
// ciphertext or staging object before the paired database migration runs.
func (store *Store) PrepareSchemaDown(down func() error) error {
	if store == nil || down == nil {
		return ErrInvalidStore
	}
	store.publicationGate.Lock()
	defer store.publicationGate.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.verifyRootIdentityLocked(); err != nil {
		return err
	}
	store.waitForAllPinsLocked()
	if err := store.verifyRootIdentityLocked(); err != nil {
		return err
	}
	if err := store.provePristineRootLocked(); err != nil {
		return err
	}
	downErr := down()
	return errors.Join(downErr, store.provePristineRootLocked())
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.publicationGate.Lock()
	defer store.publicationGate.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.waitForAllPinsLocked()
	if store.lock == nil && store.directory == nil {
		return nil
	}
	if store.lock == nil || store.directory == nil {
		lock := store.lock
		directory := store.directory
		store.lock = nil
		store.directory = nil
		return errors.Join(
			exportStoreInvariantError("partially closed store"),
			closeOptionalStoreFile(lock, "close partial store lock"),
			closeOptionalStoreFile(directory, "close partial store root"),
		)
	}
	preCloseErr := store.verifyRootIdentityLocked()
	unlockErr := exportStoreSystemError("release process lock", unix.Flock(int(store.lock.Fd()), unix.LOCK_UN))
	postUnlockErr := store.verifyRootIdentityLocked()
	lockCloseErr := exportStoreSystemError("close process lock", store.lock.Close())
	rootCloseErr := exportStoreSystemError("close root descriptor", store.directory.Close())
	store.lock = nil
	store.directory = nil
	return errors.Join(preCloseErr, unlockErr, postUnlockErr, lockCloseErr, rootCloseErr)
}

func (store *Store) closed() bool {
	if store == nil {
		return true
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.lock == nil || store.directory == nil
}

func (store *Store) hasPinnedLocatorsLocked(locators []string) bool {
	for _, locator := range locators {
		if store.pins[locator] != 0 {
			return true
		}
	}
	return false
}

func (store *Store) waitForLocatorsUnpinnedLocked(locators []string) {
	for store.hasPinnedLocatorsLocked(locators) {
		store.pinChanged.Wait()
	}
}

func (store *Store) waitForAllPinsLocked() {
	for len(store.pins) != 0 {
		store.pinChanged.Wait()
	}
}

func (store *Store) releasePinnedLocatorLocked(locator string) {
	if store.pins[locator] <= 1 {
		delete(store.pins, locator)
	} else {
		store.pins[locator]--
	}
	store.pinChanged.Broadcast()
}

type storeReferenceLoader func(context.Context) (map[string]struct{}, error)

// purgeUnreferencedResolved keeps a DB reference snapshot and the root
// inventory mutually exclusive with the Seal-to-persistence interval.
func (store *Store) purgeUnreferencedResolved(ctx context.Context, loadReferences storeReferenceLoader) (int, error) {
	if store == nil || loadReferences == nil {
		return 0, ErrInvalidStore
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if beforeGate := store.beforePublicationSweepGate; beforeGate != nil {
		beforeGate()
	}
	store.publicationGate.Lock()
	defer store.publicationGate.Unlock()
	referenced, err := loadReferences(ctx)
	if err != nil {
		return 0, err
	}
	return store.PurgeUnreferenced(referenced)
}

func (store *Store) validStagingObject(staging StagingObject) bool {
	return staging.File != nil && staging.store == store && validStoreLocator(staging.finalLocator) &&
		staging.identity.device == store.rootIdentity.device && staging.identity.inode != 0
}

func (store *Store) verifyRootIdentityLocked() error {
	if store == nil || store.directory == nil || store.lock == nil {
		return exportStoreInvariantError("store is closed")
	}
	if err := verifyExportStoreRootPath(
		store.root, store.directory, store.rootIdentity, store.ownerUID, true,
	); err != nil {
		return err
	}
	return store.verifyLockIdentityLocked()
}

func verifyExportStoreRootPath(
	path string, directory *os.File, expected storeRootIdentity, ownerUID uint32, requireRestricted bool,
) error {
	var held unix.Stat_t
	if directory == nil {
		return exportStoreInvariantError("root descriptor is absent")
	}
	if err := unix.Fstat(int(directory.Fd()), &held); err != nil {
		return exportStoreSystemError("inspect held root", err)
	}
	heldMountID, err := exportStoreMountID(int(directory.Fd()))
	if err != nil {
		return exportStoreSystemError("inspect held root mount", err)
	}
	if !validExportStoreOwnedRoot(held, ownerUID, requireRestricted) || uint64(held.Dev) != expected.device ||
		held.Ino != expected.inode || heldMountID != expected.mountID ||
		held.Uid != ownerUID {
		return exportStoreInvariantError("held root identity changed")
	}

	reopened, current, err := openExportStoreRootPath(path, false, ownerUID)
	if err != nil {
		return exportStoreSystemError("reopen configured root", err)
	}
	closeErr := exportStoreSystemError("close configured root check", reopened.Close())
	if current != expected {
		return errors.Join(exportStoreInvariantError("configured root identity changed"), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func (store *Store) verifyLockIdentityLocked() error {
	var held unix.Stat_t
	if err := unix.Fstat(int(store.lock.Fd()), &held); err != nil {
		return exportStoreSystemError("inspect held lock", err)
	}
	if !validExportStoreOwnedRegular(held, store.rootIdentity.device, store.ownerUID, 1, true) ||
		uint64(held.Dev) != store.lockIdentity.device || held.Ino != store.lockIdentity.inode {
		return exportStoreInvariantError("held lock identity changed")
	}
	var entry unix.Stat_t
	if err := unix.Fstatat(int(store.directory.Fd()), exportStoreLockName, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return exportStoreSystemError("inspect lock entry", err)
	}
	if !validExportStoreOwnedRegular(entry, store.rootIdentity.device, store.ownerUID, 1, true) ||
		!sameExportStoreFile(held, entry) {
		return exportStoreInvariantError("lock entry identity changed")
	}
	return nil
}

func (store *Store) verifyAnonymousStagingLocked(staging StagingObject) (unix.Stat_t, error) {
	var opened unix.Stat_t
	if err := unix.Fstat(int(staging.File.Fd()), &opened); err != nil {
		return unix.Stat_t{}, exportStoreSystemError("inspect open staging object", err)
	}
	if !validExportStoreOwnedRegular(opened, store.rootIdentity.device, store.ownerUID, 0, true) ||
		uint64(opened.Dev) != staging.identity.device || opened.Ino != staging.identity.inode {
		return unix.Stat_t{}, exportStoreInvariantError("open staging identity changed")
	}
	return opened, nil
}

func (store *Store) linkAnonymousStagingLocked(file *os.File, locator string) error {
	fd := int(file.Fd())
	rootFD := int(store.directory.Fd())
	if err := store.linkStagingDescriptor(fd, "", rootFD, locator, unix.AT_EMPTY_PATH); err != nil {
		if !errors.Is(err, unix.ENOENT) {
			return err
		}
		procPath := fmt.Sprintf("/proc/self/fd/%d", fd)
		return store.linkStagingDescriptor(
			unix.AT_FDCWD, procPath, rootFD, locator, unix.AT_SYMLINK_FOLLOW,
		)
	}
	return nil
}

func (store *Store) verifyPublishedEntryLocked(locator string, file *os.File, expected unix.Stat_t) error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return exportStoreSystemError("inspect published object descriptor", err)
	}
	if !validExportStoreOwnedRegular(opened, store.rootIdentity.device, store.ownerUID, 1, true) ||
		uint64(opened.Dev) != store.rootIdentity.device ||
		!sameExportStoreFile(expected, opened) {
		return exportStoreInvariantError("published object descriptor changed")
	}
	var entry unix.Stat_t
	if err := unix.Fstatat(int(store.directory.Fd()), locator, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return exportStoreSystemError("inspect published entry", err)
	}
	if !validExportStoreOwnedRegular(entry, store.rootIdentity.device, store.ownerUID, 1, true) ||
		!sameExportStoreFile(opened, entry) {
		return exportStoreInvariantError("published entry identity changed")
	}
	return nil
}

func (store *Store) openRegularEntryLocked(name string) (openedStoreEntry, bool, error) {
	var entryStat unix.Stat_t
	if err := unix.Fstatat(int(store.directory.Fd()), name, &entryStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return openedStoreEntry{}, false, nil
		}
		return openedStoreEntry{}, false, exportStoreSystemError("inspect store object", err)
	}
	if !validExportStoreOwnedRegular(entryStat, store.rootIdentity.device, store.ownerUID, 1, true) {
		return openedStoreEntry{}, false, exportStoreObjectUnsafeError("unsafe store object")
	}
	fd, err := store.openStoreEntryDescriptor(int(store.directory.Fd()), name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: exportStoreChildResolve,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return openedStoreEntry{}, false, nil
		}
		return openedStoreEntry{}, false, exportStoreSystemError("open store object", err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return openedStoreEntry{}, false, errors.Join(
			exportStoreSystemError("inspect opened store object", err),
			exportStoreSystemError("close untrusted store object", unix.Close(fd)),
		)
	}
	if !validExportStoreOwnedRegular(opened, store.rootIdentity.device, store.ownerUID, 1, true) ||
		!sameExportStoreFile(entryStat, opened) {
		return openedStoreEntry{}, false, errors.Join(
			exportStoreObjectUnsafeError("opened store object identity changed"),
			exportStoreSystemError("close changed store object", unix.Close(fd)),
		)
	}
	file := os.NewFile(uintptr(fd), "export-store-object")
	if file == nil {
		return openedStoreEntry{}, false, errors.Join(
			exportStoreInvariantError("wrap store object descriptor"),
			exportStoreSystemError("close unwrapped store object", unix.Close(fd)),
		)
	}
	return openedStoreEntry{name: name, file: file, stat: opened}, true, nil
}

func (store *Store) verifyOpenedEntryLocked(entry openedStoreEntry) error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(entry.file.Fd()), &opened); err != nil {
		return exportStoreSystemError("reinspect opened store object", err)
	}
	if !validExportStoreOwnedRegular(opened, store.rootIdentity.device, store.ownerUID, 1, true) ||
		!sameExportStoreFile(entry.stat, opened) {
		return exportStoreObjectUnsafeError("opened store object changed")
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(store.directory.Fd()), entry.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return errors.Join(ErrStoreObjectAbsent, os.ErrNotExist)
		}
		return exportStoreSystemError("reinspect named store object", err)
	}
	if !validExportStoreOwnedRegular(named, store.rootIdentity.device, store.ownerUID, 1, true) ||
		!sameExportStoreFile(opened, named) {
		return exportStoreObjectUnsafeError("named store object changed")
	}
	return nil
}

func (store *Store) validateStoreEntriesLocked(
	names []string, requirePresent bool, closeOperation string,
) error {
	if err := store.verifyRootIdentityLocked(); err != nil {
		return err
	}
	for _, name := range names {
		entry, exists, err := store.openRegularEntryLocked(name)
		if err != nil {
			return err
		}
		if !exists {
			if requirePresent {
				return exportStoreInvariantError("inventory entry disappeared")
			}
			continue
		}
		validationErr := store.verifyOpenedEntryLocked(entry)
		closeErr := exportStoreSystemError(closeOperation, entry.file.Close())
		if validationErr != nil || closeErr != nil {
			return errors.Join(validationErr, closeErr)
		}
	}
	return store.verifyRootIdentityLocked()
}

func (store *Store) unlinkEntriesLocked(
	targets []string, expectedAbsent []string,
) (purged int, resultErr error) {
	if err := store.verifyRootIdentityLocked(); err != nil {
		return 0, err
	}
	for _, target := range targets {
		entry, exists, err := store.openRegularEntryLocked(target)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			continue
		}
		if !exists {
			continue
		}
		var entryErr error
		if err := store.verifyOpenedEntryLocked(entry); err != nil {
			entryErr = errors.Join(entryErr, err)
		}
		if entryErr == nil {
			if err := unix.Unlinkat(int(store.directory.Fd()), entry.name, 0); err != nil {
				entryErr = errors.Join(entryErr, exportStoreSystemError("unlink store object", err))
			} else {
				purged++
				var unlinked unix.Stat_t
				if err := unix.Fstat(int(entry.file.Fd()), &unlinked); err != nil {
					entryErr = errors.Join(entryErr, exportStoreSystemError("inspect unlinked store object", err))
				} else if !sameExportStoreFile(entry.stat, unlinked) || unlinked.Nlink != 0 {
					entryErr = errors.Join(entryErr, exportStoreInvariantError("store unlink identity changed"))
				}
			}
		}
		entryErr = errors.Join(
			entryErr,
			exportStoreSystemError("close purged store object", entry.file.Close()),
		)
		resultErr = errors.Join(resultErr, entryErr)
	}
	resultErr = errors.Join(resultErr, exportStoreSystemError("sync purged store objects", unix.Fsync(int(store.directory.Fd()))))
	inventory, inventoryErr := store.inventoryNamesLocked(exportStoreMaxInventoryEntries)
	resultErr = errors.Join(resultErr, inventoryErr)
	absent := make(map[string]struct{}, len(expectedAbsent))
	for _, name := range expectedAbsent {
		absent[name] = struct{}{}
	}
	for _, name := range inventory {
		if _, mustBeAbsent := absent[name]; mustBeAbsent {
			resultErr = errors.Join(resultErr, exportStoreInvariantError("purged store object remains"))
		}
	}
	resultErr = errors.Join(resultErr, store.verifyRootIdentityLocked())
	return purged, resultErr
}

func (store *Store) inventoryNamesLocked(maximum int) (names []string, resultErr error) {
	if maximum <= 0 || maximum > exportStoreMaxInventoryEntries {
		return nil, exportStoreInvariantError("invalid inventory limit")
	}
	fd, err := unix.Openat2(int(store.directory.Fd()), ".", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: exportStoreChildResolve,
	})
	if err != nil {
		return nil, exportStoreSystemError("open root inventory", err)
	}
	directory := os.NewFile(uintptr(fd), "export-store-inventory")
	if directory == nil {
		return nil, errors.Join(
			exportStoreInvariantError("wrap inventory descriptor"),
			exportStoreSystemError("close unwrapped inventory descriptor", unix.Close(fd)),
		)
	}
	defer func() {
		resultErr = errors.Join(resultErr, exportStoreSystemError("close root inventory", directory.Close()))
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, exportStoreSystemError("inspect root inventory", err)
	}
	if !validExportStoreOwnedRoot(stat, store.ownerUID, true) ||
		uint64(stat.Dev) != store.rootIdentity.device || stat.Ino != store.rootIdentity.inode {
		return nil, exportStoreInvariantError("inventory root identity changed")
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, exportStoreSystemError("rewind root inventory", err)
	}
	names = make([]string, 0)
	for {
		entries, err := directory.ReadDir(exportStoreInventoryBatch)
		for _, entry := range entries {
			names = append(names, entry.Name())
			if len(names) > maximum {
				return nil, exportStoreInvariantError("root inventory exceeds limit")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, exportStoreSystemError("read root inventory", err)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (store *Store) provePristineRootLocked() error {
	if err := store.verifyRootIdentityLocked(); err != nil {
		return err
	}
	names, err := store.inventoryNamesLocked(2)
	if err != nil {
		return err
	}
	if len(names) != 1 || names[0] != exportStoreLockName {
		return exportStoreInvariantError("root inventory is not pristine")
	}
	return store.verifyRootIdentityLocked()
}

func openExportStoreRootPath(path string, create bool, ownerUID uint32) (*os.File, storeRootIdentity, error) {
	slashFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, storeRootIdentity{}, err
	}
	currentFD := slashFD
	components := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	if len(components) == 0 || components[0] == "" {
		return nil, storeRootIdentity{}, errors.Join(unix.EINVAL, unix.Close(currentFD))
	}
	for _, component := range components {
		nextFD, openErr := unix.Openat2(currentFD, component, &unix.OpenHow{
			Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Resolve: exportStoreRootResolve,
		})
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(currentFD, component, 0o700)
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return nil, storeRootIdentity{}, errors.Join(mkdirErr, unix.Close(currentFD))
			}
			if mkdirErr == nil {
				if syncErr := unix.Fsync(currentFD); syncErr != nil {
					return nil, storeRootIdentity{}, errors.Join(syncErr, unix.Close(currentFD))
				}
			}
			nextFD, openErr = unix.Openat2(currentFD, component, &unix.OpenHow{
				Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
				Resolve: exportStoreRootResolve,
			})
		}
		if openErr != nil {
			return nil, storeRootIdentity{}, errors.Join(openErr, unix.Close(currentFD))
		}
		if closeErr := unix.Close(currentFD); closeErr != nil {
			return nil, storeRootIdentity{}, errors.Join(closeErr, unix.Close(nextFD))
		}
		currentFD = nextFD
	}
	var stat unix.Stat_t
	if err := unix.Fstat(currentFD, &stat); err != nil {
		return nil, storeRootIdentity{}, errors.Join(err, unix.Close(currentFD))
	}
	if !validExportStoreOwnedRoot(stat, ownerUID, false) {
		return nil, storeRootIdentity{}, errors.Join(unix.ENOTDIR, unix.Close(currentFD))
	}
	mountID, err := exportStoreMountID(currentFD)
	if err != nil {
		return nil, storeRootIdentity{}, errors.Join(err, unix.Close(currentFD))
	}
	file := os.NewFile(uintptr(currentFD), "export-store-root")
	if file == nil {
		return nil, storeRootIdentity{}, errors.Join(unix.EBADF, unix.Close(currentFD))
	}
	return file, storeRootIdentity{device: uint64(stat.Dev), inode: stat.Ino, mountID: mountID}, nil
}

func exportStoreMountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, unix.ENOTSUP
	}
	return stat.Mnt_id, nil
}

func validExportStoreRoot(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Nlink > 0
}

func validExportStoreOwnedRoot(stat unix.Stat_t, ownerUID uint32, requireRestricted bool) bool {
	return validExportStoreRoot(stat) && stat.Uid == ownerUID && (!requireRestricted || stat.Mode&0o077 == 0)
}

func validExportStoreOwnedRegular(
	stat unix.Stat_t, rootDevice uint64, ownerUID uint32, expectedLinks uint64, requireRestricted bool,
) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && uint64(stat.Dev) == rootDevice &&
		stat.Uid == ownerUID && uint64(stat.Nlink) == expectedLinks &&
		(!requireRestricted || stat.Mode&0o077 == 0)
}

func sameExportStoreFile(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

func validStoreLocator(locator string) bool {
	if locator == "" || filepath.Base(locator) != locator || strings.ContainsAny(locator, "/\\\x00") {
		return false
	}
	extension := filepath.Ext(locator)
	return (extension == ".xre" || extension == ".xrs") && lowerHex(strings.TrimSuffix(locator, extension), 32)
}

func forbiddenStoreRoot(root string) bool {
	if root == string(filepath.Separator) {
		return true
	}
	for _, forbidden := range []string{"/data", "/backup", "/logs"} {
		if root == forbidden || strings.HasPrefix(root, forbidden+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func closeOptionalStoreFile(file *os.File, operation string) error {
	if file == nil {
		return nil
	}
	return exportStoreSystemError(operation, file.Close())
}

func exportStoreInvariantError(operation string) error {
	return fmt.Errorf("%w: %s", ErrInvalidStore, operation)
}

func exportStoreObjectUnsafeError(operation string) error {
	return errors.Join(ErrStoreObjectUnsafe, exportStoreInvariantError(operation))
}

func exportStoreSystemError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidStore) {
		return err
	}
	return errors.Join(ErrInvalidStore, fmt.Errorf("%s: %w", operation, err))
}
