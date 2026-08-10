//go:build linux

package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type strictRootHandle struct{ fd int }

type strictTreeIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
}

type pinnedStrictTree struct {
	mu           sync.Mutex
	rootPath     string
	treeRelative string
	root         *strictRootHandle
	treeFD       int
	rootIdentity strictTreeIdentity
	treeIdentity strictTreeIdentity
	closed       bool
}

func openStrictRoot(path string) (*strictRootHandle, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("%w: strict root must be absolute", ErrOutsideRoot)
	}
	fd, err := unix.Open(clean, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, classifyOpenError(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		_ = unix.Close(fd)
		return nil, ErrSymlinkDenied
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return nil, ErrNotDirectory
	}
	return &strictRootHandle{fd: fd}, nil
}

func (root *strictRootHandle) Close() error { return unix.Close(root.fd) }

// OpenPinnedStrictTree opens and retains the managed root and final tree
// descriptors. The returned capability intentionally retains the root paths
// only for internal revalidation and never exposes either path to its caller.
func OpenPinnedStrictTree(ctx context.Context, root Root, tree Locator) (PinnedStrictTree, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	relative, _, err := Resolve(root, tree, ProviderPolicy)
	if err != nil {
		return nil, err
	}
	strictRoot, err := openStrictRoot(root.Path)
	if err != nil {
		return nil, err
	}
	rootIdentity, err := strictIdentityForFD(strictRoot.fd)
	if err != nil {
		_ = strictRoot.Close()
		return nil, err
	}
	treeFD, err := openStrictTreeDirectory(strictRoot.fd, relative)
	if err != nil {
		_ = strictRoot.Close()
		return nil, classifyPinnedTreeOpenError(err)
	}
	treeIdentity, err := strictIdentityForFD(treeFD)
	if err != nil {
		_ = unix.Close(treeFD)
		_ = strictRoot.Close()
		return nil, err
	}
	return &pinnedStrictTree{
		rootPath:     filepath.Clean(root.Path),
		treeRelative: relative,
		root:         strictRoot,
		treeFD:       treeFD,
		rootIdentity: rootIdentity,
		treeIdentity: treeIdentity,
	}, nil
}

func (tree *pinnedStrictTree) OpenDeclaredRegular(ctx context.Context, entry Entry) (ReadHandle, ContentStat, error) {
	if err := contextError(ctx); err != nil {
		return nil, ContentStat{}, err
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if err := tree.revalidateLocked(ctx); err != nil {
		return nil, ContentStat{}, err
	}
	if entry.Type != EntryFile || entry.Size < 0 || entry.Locator.root || entry.SourceRevision == "" {
		return nil, ContentStat{}, ErrNotRegular
	}
	locator, err := ParseLocator(entry.Locator.Path, ProviderPolicy)
	if err != nil || filepath.Base(locator.Path) != entry.Name {
		return nil, ContentStat{}, ErrInvalidLocator
	}
	fd, err := unix.Openat2(tree.treeFD, locator.Path, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return nil, ContentStat{}, classifyPinnedTreeRevalidationError(err)
	}
	file := os.NewFile(uintptr(fd), locator.Path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ContentStat{}, ErrSourceChanged
	}
	actual := entryFromFileInfo(entry.Name, locator, info)
	if actual.Type != EntryFile || actual.SourceRevision != entry.SourceRevision || actual.Size != entry.Size {
		_ = file.Close()
		return nil, ContentStat{}, ErrSourceChanged
	}
	return newLocalReadHandle(ctx, file, io.LimitReader(file, entry.Size), snapshotFileInfo(info)), contentStatFromFileInfo(info), nil
}

func (tree *pinnedStrictTree) Revalidate(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	return tree.revalidateLocked(ctx)
}

func (tree *pinnedStrictTree) revalidateLocked(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if tree == nil || tree.closed || tree.root == nil || tree.treeFD < 0 {
		return ErrSourceChanged
	}
	if identity, err := strictIdentityForFD(tree.root.fd); err != nil || identity != tree.rootIdentity {
		return ErrSourceChanged
	}
	if identity, err := strictIdentityForFD(tree.treeFD); err != nil || identity != tree.treeIdentity {
		return ErrSourceChanged
	}
	currentRoot, err := openStrictRoot(tree.rootPath)
	if err != nil {
		return classifyPinnedTreeRevalidationError(err)
	}
	defer func() { _ = currentRoot.Close() }()
	currentRootIdentity, err := strictIdentityForFD(currentRoot.fd)
	if err != nil || currentRootIdentity != tree.rootIdentity {
		return ErrSourceChanged
	}
	currentTreeFD, err := openStrictTreeDirectory(currentRoot.fd, tree.treeRelative)
	if err != nil {
		return classifyPinnedTreeRevalidationError(err)
	}
	defer func() { _ = unix.Close(currentTreeFD) }()
	currentTreeIdentity, err := strictIdentityForFD(currentTreeFD)
	if err != nil || currentTreeIdentity != tree.treeIdentity {
		return ErrSourceChanged
	}
	return nil
}

func (tree *pinnedStrictTree) Close() error {
	if tree == nil {
		return nil
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.closed {
		return nil
	}
	tree.closed = true
	var closeErr error
	if tree.treeFD >= 0 {
		closeErr = unix.Close(tree.treeFD)
		tree.treeFD = -1
	}
	if tree.root != nil {
		if err := tree.root.Close(); closeErr == nil && err != nil {
			closeErr = err
		}
		tree.root = nil
	}
	return closeErr
}

func openStrictTreeDirectory(rootFD int, relative string) (int, error) {
	return unix.Openat2(rootFD, relative, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
}

func strictIdentityForFD(fd int) (strictTreeIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return strictTreeIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return strictTreeIdentity{}, ErrNotDirectory
	}
	return strictTreeIdentity{device: uint64(stat.Dev), inode: stat.Ino, mode: stat.Mode}, nil
}

func classifyPinnedTreeOpenError(err error) error {
	if strictKernelUnavailable(err) {
		return ErrStrictUnavailable
	}
	return classifyOpenError(err)
}

func classifyPinnedTreeRevalidationError(err error) error {
	if strictKernelUnavailable(err) {
		return ErrStrictUnavailable
	}
	return ErrSourceChanged
}

func strictKernelUnavailable(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL)
}

func (root *strictRootHandle) OpenRegular(relative string) (*os.File, error) {
	fd, err := unix.Openat2(root.fd, relative, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), relative), nil
}

func (root *strictRootHandle) List(ctx context.Context, relative string, request PageRequest) (EntryPage, error) {
	fd, err := unix.Openat2(root.fd, relative, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return EntryPage{}, classifyOpenError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	collector := newEntryCollector(request)
	buffer := make([]byte, 16<<10)
	for {
		if err := contextError(ctx); err != nil {
			return EntryPage{}, err
		}
		count, readErr := unix.ReadDirent(fd, buffer)
		if readErr != nil {
			return EntryPage{}, readErr
		}
		if count == 0 {
			break
		}
		_, _, names := unix.ParseDirent(buffer[:count], -1, nil)
		for _, name := range names {
			var stat unix.Stat_t
			if infoErr := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); infoErr != nil {
				return EntryPage{}, infoErr
			}
			if err := collector.Add(entryFromUnixStat(name, joinLocator(relative, name), stat)); err != nil {
				return EntryPage{}, err
			}
		}
	}
	return collector.Page(), nil
}

func (root *strictRootHandle) Lstat(relative string) (Entry, error) {
	parent, name := filepath.Split(relative)
	if name == "" {
		name = "."
	}
	parent = filepath.Clean(parent)
	if parent == "" {
		parent = "."
	}
	parentFD, err := unix.Openat2(root.fd, parent, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return Entry{}, classifyOpenError(err)
	}
	defer func() { _ = unix.Close(parentFD) }()
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Entry{}, err
	}
	return entryFromUnixStat(filepath.Base(relative), Locator{Path: relative}, stat), nil
}

func entryFromUnixStat(name string, locator Locator, stat unix.Stat_t) Entry {
	entryType := unixModeEntryType(stat.Mode)
	modTime := time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC()
	snapshot := fileSnapshot{size: stat.Size, mode: unixModeFileMode(stat.Mode), modTime: modTime.UnixNano(), device: uint64(stat.Dev), inode: stat.Ino}
	return Entry{Name: name, Locator: locator, Type: entryType, Size: stat.Size, Mode: snapshot.mode.String(), ModTime: modTime, OpaqueDigest: entryDigest(name, entryType, snapshot), SourceRevision: snapshot.revision()}
}

func unixModeEntryType(mode uint32) EntryType {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return EntryFile
	case unix.S_IFDIR:
		return EntryDirectory
	case unix.S_IFLNK:
		return EntrySymlink
	default:
		return EntrySpecial
	}
}

func unixModeFileMode(mode uint32) os.FileMode {
	value := os.FileMode(mode & 0o777)
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		value |= os.ModeDir
	case unix.S_IFLNK:
		value |= os.ModeSymlink
	case unix.S_IFIFO:
		value |= os.ModeNamedPipe
	case unix.S_IFSOCK:
		value |= os.ModeSocket
	case unix.S_IFCHR:
		value |= os.ModeCharDevice | os.ModeDevice
	case unix.S_IFBLK:
		value |= os.ModeDevice
	}
	return value
}

func isStrictSymlinkError(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) || errors.Is(err, syscall.EMLINK)
}
