//go:build linux

package provider

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"xirang/backend/internal/backupasset"

	"golang.org/x/sys/unix"
)

const rsyncManagedTreeResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV

func openRsyncManagedTree(path string) (*rsyncManagedTree, error) {
	tree, err := openRsyncManagedTreeBase(path)
	if err != nil {
		return nil, err
	}
	if err := ensureRsyncManagedTreeControlDir(tree, "staging"); err != nil {
		_ = tree.Close()
		return nil, err
	}
	if err := ensureRsyncManagedTreeControlDir(tree, "points"); err != nil {
		_ = tree.Close()
		return nil, err
	}
	return tree, nil
}

// openRsyncManagedTreeReadOnly never creates a control directory. Readers use
// it so a malformed or replaced root cannot be repaired or initialized by a
// browse operation before its authenticated marker is validated.
func openRsyncManagedTreeReadOnly(path string) (*rsyncManagedTree, error) {
	tree, err := openRsyncManagedTreeBase(path)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"staging", "points"} {
		fd, openErr := openRsyncManagedTreeChildDir(tree.rootFD, name)
		if openErr != nil {
			_ = tree.Close()
			return nil, openErr
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			_ = tree.Close()
			return nil, rsyncManagedTreeSystemError(closeErr)
		}
	}
	return tree, nil
}

func openRsyncManagedTreeBase(path string) (*rsyncManagedTree, error) {
	rootPath, err := normalizeRsyncManagedRoot(path)
	if err != nil {
		return nil, err
	}
	fd, device, inode, mountID, err := openTrustedRsyncManagedRoot(rootPath)
	if err != nil {
		return nil, err
	}
	tree := &rsyncManagedTree{
		rootPath: rootPath, rootFD: fd, rootDevice: device, rootInode: inode, rootMountID: mountID,
		fsync: unix.Fsync, linkat: unix.Linkat, renameat2: unix.Renameat2,
		statfs: func(fd int, result *rsyncTreeFilesystemStats) error {
			var stat unix.Statfs_t
			if err := unix.Fstatfs(fd, &stat); err != nil {
				return err
			}
			result.BlockSize = stat.Bsize
			result.AvailableBlocks = stat.Bavail
			result.FreeInodes = stat.Ffree
			return nil
		},
	}
	return tree, nil
}

func (tree *rsyncManagedTree) Close() error {
	if tree == nil || tree.rootFD < 0 {
		return nil
	}
	err := unix.Close(tree.rootFD)
	tree.rootFD = -1
	return err
}

func (tree *rsyncManagedTree) VerifyRootIdentity() error {
	if tree == nil || tree.rootFD < 0 {
		return errRsyncManagedTreeUnsupported
	}
	var current unix.Stat_t
	if err := unix.Fstat(tree.rootFD, &current); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	currentMountID, err := rsyncManagedTreeMountID(tree.rootFD)
	if err != nil {
		return err
	}
	if uint64(current.Dev) != tree.rootDevice || current.Ino != tree.rootInode || currentMountID != tree.rootMountID {
		return fmt.Errorf("%w: managed root descriptor changed", errRsyncManagedTreeUnsafe)
	}
	fd, device, inode, mountID, err := openTrustedRsyncManagedRoot(tree.rootPath)
	if err != nil {
		return err
	}
	closeErr := unix.Close(fd)
	if closeErr != nil {
		return rsyncManagedTreeSystemError(closeErr)
	}
	if device != tree.rootDevice || inode != tree.rootInode || mountID != tree.rootMountID {
		return fmt.Errorf("%w: managed root path changed", errRsyncManagedTreeUnsafe)
	}
	return nil
}

func (tree *rsyncManagedTree) CreateFreshStaging(component string) error {
	if !validRsyncManagedTreeComponent(component) {
		return fmt.Errorf("%w: invalid staging component", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	if err := unix.Mkdirat(stagingFD, component, 0o700); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, component)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(attemptFD) }()
	empty, err := rsyncManagedTreeDirectoryEmpty(attemptFD)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("%w: newly created staging is nonempty", errRsyncManagedTreeUnsafe)
	}
	if err := tree.sync(attemptFD); err != nil {
		return err
	}
	if err := tree.sync(stagingFD); err != nil {
		return err
	}
	return tree.sync(tree.rootFD)
}

func (tree *rsyncManagedTree) VerifyFreshStaging(component string) error {
	if !validRsyncManagedTreeComponent(component) {
		return fmt.Errorf("%w: invalid staging component", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, component)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(attemptFD) }()
	empty, err := rsyncManagedTreeDirectoryEmpty(attemptFD)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("%w: staging is nonempty", errRsyncManagedTreeUnsafe)
	}
	return nil
}

func (tree *rsyncManagedTree) CreateFreshStagingTree(component string) error {
	if err := tree.CreateFreshStaging(component); err != nil {
		return err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, component)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(attemptFD) }()
	if err := unix.Mkdirat(attemptFD, "tree", 0o700); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	treeFD, err := openRsyncManagedTreeChildDir(attemptFD, "tree")
	if err != nil {
		return err
	}
	if err := tree.sync(treeFD); err != nil {
		_ = unix.Close(treeFD)
		return err
	}
	if err := unix.Close(treeFD); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	if err := tree.sync(attemptFD); err != nil {
		return err
	}
	if err := tree.sync(stagingFD); err != nil {
		return err
	}
	return tree.sync(tree.rootFD)
}

func (tree *rsyncManagedTree) openStagingTree(component string) (int, error) {
	if !validRsyncManagedTreeComponent(component) {
		return -1, fmt.Errorf("%w: invalid staging component", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return -1, err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, component)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(attemptFD) }()
	return openRsyncManagedTreeChildDir(attemptFD, "tree")
}

func (tree *rsyncManagedTree) openFinalTree(component string) (int, error) {
	if !validRsyncManagedTreeComponent(component) {
		return -1, fmt.Errorf("%w: invalid final component", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return -1, err
	}
	pointsFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "points")
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(pointsFD) }()
	pointFD, err := openRsyncManagedTreeChildDir(pointsFD, component)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(pointFD) }()
	return openRsyncManagedTreeChildDir(pointFD, "tree")
}

func (tree *rsyncManagedTree) stagingTreePath(component string) (string, error) {
	fd, err := tree.openStagingTree(component)
	if err != nil {
		return "", err
	}
	if err := unix.Close(fd); err != nil {
		return "", rsyncManagedTreeSystemError(err)
	}
	return filepath.Join(tree.rootPath, "staging", component, "tree"), nil
}

func (tree *rsyncManagedTree) finalTreePath(component string) (string, error) {
	fd, err := tree.openFinalTree(component)
	if err != nil {
		return "", err
	}
	if err := unix.Close(fd); err != nil {
		return "", rsyncManagedTreeSystemError(err)
	}
	return filepath.Join(tree.rootPath, "points", component, "tree"), nil
}

func (tree *rsyncManagedTree) WriteStagingMetadata(component, name string, payload []byte) error {
	if !validRsyncManagedTreeComponent(component) || !validRsyncManagedTreeComponent(name) || len(payload) > maxRsyncManagedTreeMetadataBytes {
		return fmt.Errorf("%w: invalid managed staging metadata", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, component)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(attemptFD) }()
	fd, err := unix.Openat(attemptFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	for offset := 0; offset < len(payload); {
		count, writeErr := unix.Write(fd, payload[offset:])
		if count > 0 {
			offset += count
		}
		if writeErr != nil {
			_ = unix.Close(fd)
			return rsyncManagedTreeSystemError(writeErr)
		}
		if count == 0 && len(payload) > offset {
			_ = unix.Close(fd)
			return fmt.Errorf("%w: short managed staging metadata write", errRsyncManagedTreeUnsafe)
		}
	}
	if err := tree.sync(fd); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := unix.Close(fd); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	if err := tree.sync(attemptFD); err != nil {
		return err
	}
	if err := tree.sync(stagingFD); err != nil {
		return err
	}
	return tree.sync(tree.rootFD)
}

func (tree *rsyncManagedTree) readFinalMetadata(component, name string) ([]byte, error) {
	if !validRsyncManagedTreeComponent(component) || !validRsyncManagedTreeComponent(name) {
		return nil, fmt.Errorf("%w: invalid managed final metadata", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return nil, err
	}
	pointsFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "points")
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(pointsFD) }()
	pointFD, err := openRsyncManagedTreeChildDir(pointsFD, component)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(pointFD) }()
	fd, err := unix.Openat2(pointFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: rsyncManagedTreeResolve})
	if err != nil {
		return nil, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, rsyncManagedTreeSystemError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size < 0 || stat.Size > maxTaggedPublicationPayloadBytes {
		return nil, fmt.Errorf("%w: invalid managed final metadata", errRsyncManagedTreeUnsafe)
	}
	payload := make([]byte, int(stat.Size))
	for offset := 0; offset < len(payload); {
		count, readErr := unix.Read(fd, payload[offset:])
		if count > 0 {
			offset += count
		}
		if readErr != nil {
			return nil, rsyncManagedTreeSystemError(readErr)
		}
		if count == 0 && offset < len(payload) {
			return nil, fmt.Errorf("%w: short managed final metadata read", errRsyncManagedTreeUnsafe)
		}
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return nil, err
	}
	return payload, nil
}

// finalComponentExists distinguishes an absent final from a partially created
// or malformed final directory. Reconciliation must not treat the latter as a
// safe absence because a no-replace rename may already have occurred.
func (tree *rsyncManagedTree) finalComponentExists(component string) (bool, error) {
	if !validRsyncManagedTreeComponent(component) {
		return false, fmt.Errorf("%w: invalid managed final component", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return false, err
	}
	pointsFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "points")
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(pointsFD) }()
	pointFD, err := openRsyncManagedTreeChildDir(pointsFD, component)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if closeErr := unix.Close(pointFD); closeErr != nil {
		return false, rsyncManagedTreeSystemError(closeErr)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return false, err
	}
	return true, nil
}

func (tree *rsyncManagedTree) readStagingMetadata(component, name string) ([]byte, error) {
	if !validRsyncManagedTreeComponent(component) || !validRsyncManagedTreeComponent(name) {
		return nil, fmt.Errorf("%w: invalid managed staging metadata", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return nil, err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, component)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(attemptFD) }()
	fd, err := unix.Openat2(attemptFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: rsyncManagedTreeResolve})
	if err != nil {
		return nil, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, rsyncManagedTreeSystemError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size < 0 || stat.Size > maxTaggedPublicationPayloadBytes {
		return nil, fmt.Errorf("%w: invalid managed staging metadata", errRsyncManagedTreeUnsafe)
	}
	payload := make([]byte, int(stat.Size))
	for offset := 0; offset < len(payload); {
		count, readErr := unix.Read(fd, payload[offset:])
		if count > 0 {
			offset += count
		}
		if readErr != nil {
			return nil, rsyncManagedTreeSystemError(readErr)
		}
		if count == 0 && offset < len(payload) {
			return nil, fmt.Errorf("%w: short managed staging metadata read", errRsyncManagedTreeUnsafe)
		}
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return nil, err
	}
	return payload, nil
}

func (tree *rsyncManagedTree) FsyncStagingTree(component string) error {
	fd, err := tree.openStagingTree(component)
	if err != nil {
		return err
	}
	err = tree.fsyncTreeDirectory(fd)
	closeErr := unix.Close(fd)
	if err != nil {
		return err
	}
	if closeErr != nil {
		return rsyncManagedTreeSystemError(closeErr)
	}
	return tree.VerifyRootIdentity()
}

func (tree *rsyncManagedTree) fsyncTreeDirectory(directoryFD int) error {
	names, err := rsyncTreeDirectoryNames(directoryFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return rsyncManagedTreeSystemError(err)
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			fd, err := unix.Openat2(directoryFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: rsyncManagedTreeResolve})
			if err != nil {
				return rsyncManagedTreeSystemError(err)
			}
			var opened unix.Stat_t
			if err := unix.Fstat(fd, &opened); err != nil {
				_ = unix.Close(fd)
				return rsyncManagedTreeSystemError(err)
			}
			if opened.Dev != stat.Dev || opened.Ino != stat.Ino {
				_ = unix.Close(fd)
				return fmt.Errorf("%w: managed Rsync tree changed before fsync", errRsyncManagedTreeUnsafe)
			}
			if err := tree.sync(fd); err != nil {
				_ = unix.Close(fd)
				return err
			}
			if err := unix.Close(fd); err != nil {
				return rsyncManagedTreeSystemError(err)
			}
		case unix.S_IFDIR:
			fd, err := unix.Openat2(directoryFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: rsyncManagedTreeResolve})
			if err != nil {
				return rsyncManagedTreeSystemError(err)
			}
			err = tree.fsyncTreeDirectory(fd)
			closeErr := unix.Close(fd)
			if err != nil {
				return err
			}
			if closeErr != nil {
				return rsyncManagedTreeSystemError(closeErr)
			}
		case unix.S_IFLNK:
			// Symlinks have no file descriptor to fsync. Their containing
			// directory is synced below after every entry is validated.
		default:
			return fmt.Errorf("%w: unsupported managed Rsync tree node", errRsyncManagedTreeUnsafe)
		}
	}
	return tree.sync(directoryFD)
}

func (tree *rsyncManagedTree) VerifyStaging(component string) error {
	if !validRsyncManagedTreeComponent(component) {
		return fmt.Errorf("%w: invalid staging component", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, component)
	if err != nil {
		return err
	}
	return unix.Close(attemptFD)
}

// CommitStaging performs only the trusted no-replace directory move. Its
// caller must first authenticate the attempt marker and validate tree content;
// populated staging is the expected state at provider commit time.
func (tree *rsyncManagedTree) CommitStaging(stagingComponent, finalComponent string) error {
	if !validRsyncManagedTreeComponent(stagingComponent) || !validRsyncManagedTreeComponent(finalComponent) {
		return fmt.Errorf("%w: invalid commit component", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyStaging(stagingComponent); err != nil {
		return err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	pointsFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "points")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(pointsFD) }()
	if tree.renameat2 == nil {
		return errRsyncManagedTreeUnsupported
	}
	if err := tree.renameat2(stagingFD, stagingComponent, pointsFD, finalComponent, unix.RENAME_NOREPLACE); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	if err := tree.sync(stagingFD); err != nil {
		return err
	}
	if err := tree.sync(pointsFD); err != nil {
		return err
	}
	if err := tree.sync(tree.rootFD); err != nil {
		return err
	}
	return tree.VerifyRootIdentity()
}

func (tree *rsyncManagedTree) ProbeCommitPrimitives() (rsyncTreeCommitProbe, error) {
	if err := tree.VerifyRootIdentity(); err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	attempt := id + "." + id
	if err := tree.CreateFreshStaging(attempt); err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	stagingFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "staging")
	if err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	attemptFD, err := openRsyncManagedTreeChildDir(stagingFD, attempt)
	if err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	defer func() { _ = unix.Close(attemptFD) }()
	if err := rsyncManagedTreeWriteProbeFile(tree, attemptFD, "link-source"); err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	hardlinkVerified := false
	if tree.linkat == nil {
		return rsyncTreeCommitProbe{}, errRsyncManagedTreeUnsupported
	}
	if err := tree.linkat(attemptFD, "link-source", attemptFD, "link-target", 0); err == nil {
		var source, target unix.Stat_t
		if sourceErr := unix.Fstatat(attemptFD, "link-source", &source, unix.AT_SYMLINK_NOFOLLOW); sourceErr != nil {
			return rsyncTreeCommitProbe{}, rsyncManagedTreeSystemError(sourceErr)
		}
		if targetErr := unix.Fstatat(attemptFD, "link-target", &target, unix.AT_SYMLINK_NOFOLLOW); targetErr != nil {
			return rsyncTreeCommitProbe{}, rsyncManagedTreeSystemError(targetErr)
		}
		hardlinkVerified = uint64(source.Dev) == uint64(target.Dev) && source.Ino == target.Ino && source.Nlink >= 2 && target.Nlink >= 2
		if err := unix.Unlinkat(attemptFD, "link-target", 0); err != nil {
			return rsyncTreeCommitProbe{}, rsyncManagedTreeSystemError(err)
		}
	} else if !rsyncManagedTreeHardlinkUnavailable(err) {
		return rsyncTreeCommitProbe{}, rsyncManagedTreeSystemError(err)
	}
	if err := unix.Unlinkat(attemptFD, "link-source", 0); err != nil {
		return rsyncTreeCommitProbe{}, rsyncManagedTreeSystemError(err)
	}
	if err := tree.VerifyFreshStaging(attempt); err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	if err := tree.CommitStaging(attempt, id); err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	pointsFD, err := openRsyncManagedTreeChildDir(tree.rootFD, "points")
	if err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	defer func() { _ = unix.Close(pointsFD) }()
	if err := unix.Unlinkat(pointsFD, id, unix.AT_REMOVEDIR); err != nil {
		return rsyncTreeCommitProbe{}, rsyncManagedTreeSystemError(err)
	}
	if err := tree.sync(pointsFD); err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	if err := tree.sync(tree.rootFD); err != nil {
		return rsyncTreeCommitProbe{}, err
	}
	return rsyncTreeCommitProbe{HardlinkVerified: hardlinkVerified, RenameNoReplaceVerified: true, DirectoryFsyncVerified: true}, nil
}

func rsyncManagedTreeHardlinkUnavailable(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EMLINK) ||
		errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS)
}

func (tree *rsyncManagedTree) readRepositoryMarker() ([]byte, error) {
	if err := tree.VerifyRootIdentity(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat2(tree.rootFD, "repository.json", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: rsyncManagedTreeResolve,
	})
	if err != nil {
		return nil, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, rsyncManagedTreeSystemError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%w: managed repository marker is not a regular file", errRsyncManagedTreeUnsafe)
	}
	const maxMarkerBytes = 64 << 10
	marker := make([]byte, 0, 1024)
	buffer := make([]byte, 4096)
	for {
		count, err := unix.Read(fd, buffer)
		if err != nil {
			return nil, rsyncManagedTreeSystemError(err)
		}
		if count == 0 {
			break
		}
		if len(marker)+count > maxMarkerBytes {
			return nil, fmt.Errorf("%w: managed repository marker exceeds size limit", errRsyncManagedTreeUnsafe)
		}
		marker = append(marker, buffer[:count]...)
	}
	if len(bytes.TrimSpace(marker)) == 0 {
		return nil, fmt.Errorf("%w: managed repository marker is empty", errRsyncManagedTreeUnsafe)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return nil, err
	}
	return marker, nil
}

func (tree *rsyncManagedTree) validateLocalSourceRoot(path string) (string, error) {
	sourcePath, err := normalizeRsyncManagedRoot(path)
	if err != nil {
		return "", err
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return "", err
	}
	sourceFD, device, inode, mountID, err := openTrustedRsyncManagedRoot(sourcePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(sourceFD) }()
	if device == tree.rootDevice && inode == tree.rootInode {
		return "", fmt.Errorf("%w: source and managed root overlap", errRsyncManagedTreeUnsafe)
	}
	managedRootAncestor, err := rsyncManagedTreeDirectoryIsAncestor(tree.rootFD, sourceFD)
	if err != nil {
		return "", err
	}
	sourceAncestor, err := rsyncManagedTreeDirectoryIsAncestor(sourceFD, tree.rootFD)
	if err != nil {
		return "", err
	}
	if managedRootAncestor || sourceAncestor {
		return "", fmt.Errorf("%w: source and managed root overlap", errRsyncManagedTreeUnsafe)
	}
	return rsyncTreeDigest([]byte(strings.Join([]string{
		"rsync-local-source-v1",
		fmt.Sprintf("%d", device),
		fmt.Sprintf("%d", inode),
		fmt.Sprintf("%d", mountID),
	}, "\n"))), nil
}

func (tree *rsyncManagedTree) capacitySnapshot() (rsyncTreeCapacitySnapshot, error) {
	if err := tree.VerifyRootIdentity(); err != nil {
		return rsyncTreeCapacitySnapshot{}, err
	}
	if tree.statfs == nil {
		return rsyncTreeCapacitySnapshot{}, errRsyncManagedTreeUnsupported
	}
	var stat rsyncTreeFilesystemStats
	if err := tree.statfs(tree.rootFD, &stat); err != nil {
		return rsyncTreeCapacitySnapshot{}, rsyncManagedTreeSystemError(err)
	}
	if stat.BlockSize <= 0 {
		return rsyncTreeCapacitySnapshot{}, fmt.Errorf("%w: invalid managed tree capacity evidence", errRsyncManagedTreeUnsafe)
	}
	blockSize := uint64(stat.BlockSize)
	freeBlocks := stat.AvailableBlocks
	if freeBlocks > ^uint64(0)/blockSize {
		return rsyncTreeCapacitySnapshot{}, fmt.Errorf("%w: managed tree capacity overflow", errRsyncManagedTreeUnsafe)
	}
	return rsyncTreeCapacitySnapshot{
		FreeBytes:   freeBlocks * blockSize,
		FreeInodes:  stat.FreeInodes,
		QuotaSignal: RsyncTreeQuotaSignalUnknown,
	}, nil
}

func rsyncManagedTreeDirectoryIsAncestor(ancestorFD, descendantFD int) (bool, error) {
	var ancestor unix.Stat_t
	if err := unix.Fstat(ancestorFD, &ancestor); err != nil {
		return false, rsyncManagedTreeSystemError(err)
	}
	currentFD, err := unix.Dup(descendantFD)
	if err != nil {
		return false, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(currentFD) }()
	for depth := 0; depth < 1024; depth++ {
		var current unix.Stat_t
		if err := unix.Fstat(currentFD, &current); err != nil {
			return false, rsyncManagedTreeSystemError(err)
		}
		if current.Dev == ancestor.Dev && current.Ino == ancestor.Ino {
			return true, nil
		}
		parentFD, err := unix.Openat(currentFD, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return false, rsyncManagedTreeSystemError(err)
		}
		var parent unix.Stat_t
		if err := unix.Fstat(parentFD, &parent); err != nil {
			_ = unix.Close(parentFD)
			return false, rsyncManagedTreeSystemError(err)
		}
		if parent.Dev == current.Dev && parent.Ino == current.Ino {
			_ = unix.Close(parentFD)
			return false, nil
		}
		if err := unix.Close(currentFD); err != nil {
			_ = unix.Close(parentFD)
			return false, rsyncManagedTreeSystemError(err)
		}
		currentFD = parentFD
	}
	return false, fmt.Errorf("%w: source ancestry exceeds limit", errRsyncManagedTreeUnsafe)
}

func openTrustedRsyncManagedRoot(path string) (int, uint64, uint64, uint64, error) {
	slashFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, 0, 0, 0, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(slashFD) }()
	relative := strings.TrimPrefix(filepath.Clean(path), "/")
	fd, err := unix.Openat2(slashFD, relative, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return -1, 0, 0, 0, rsyncManagedTreeSystemError(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, 0, 0, 0, rsyncManagedTreeSystemError(err)
	}
	mountID, err := rsyncManagedTreeMountID(fd)
	if err != nil {
		_ = unix.Close(fd)
		return -1, 0, 0, 0, err
	}
	return fd, uint64(stat.Dev), stat.Ino, mountID, nil
}

func ensureRsyncManagedTreeControlDir(tree *rsyncManagedTree, name string) error {
	if err := unix.Mkdirat(tree.rootFD, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return rsyncManagedTreeSystemError(err)
	}
	childFD, err := openRsyncManagedTreeChildDir(tree.rootFD, name)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(childFD) }()
	if err := tree.sync(childFD); err != nil {
		return err
	}
	return tree.sync(tree.rootFD)
}

func openRsyncManagedTreeChildDir(parentFD int, name string) (int, error) {
	if !validRsyncManagedTreeComponent(name) {
		return -1, fmt.Errorf("%w: invalid managed tree component", errRsyncManagedTreeUnsafe)
	}
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: rsyncManagedTreeResolve,
	})
	if err != nil {
		return -1, rsyncManagedTreeSystemError(err)
	}
	return fd, nil
}

func rsyncManagedTreeDirectoryEmpty(fd int) (bool, error) {
	buffer := make([]byte, 4096)
	for {
		count, err := unix.ReadDirent(fd, buffer)
		if err != nil {
			return false, rsyncManagedTreeSystemError(err)
		}
		if count == 0 {
			return true, nil
		}
		_, _, names := unix.ParseDirent(buffer[:count], -1, nil)
		if len(names) != 0 {
			return false, nil
		}
	}
}

func rsyncManagedTreeWriteProbeFile(tree *rsyncManagedTree, dirFD int, name string) error {
	fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	if _, err := unix.Write(fd, []byte("x")); err != nil {
		_ = unix.Close(fd)
		return rsyncManagedTreeSystemError(err)
	}
	if err := tree.sync(fd); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := unix.Close(fd); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	return tree.sync(dirFD)
}

func rsyncManagedTreeMountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, rsyncManagedTreeSystemError(err)
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, errRsyncManagedTreeUnsupported
	}
	return stat.Mnt_id, nil
}
