package provider

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	errRsyncManagedTreeUnsupported = errors.New("managed Rsync tree primitive unsupported")
	errRsyncManagedTreeUnsafe      = errors.New("managed Rsync tree invariant failed")
)

// MaxRsyncTreeMetadataBytes is the hard ceiling for provider-side managed-tree
// metadata. Callers must clamp dynamic publication settings to this bound
// before constructing a RsyncTreePublicationInput.
const MaxRsyncTreeMetadataBytes = 16 << 20

const maxRsyncManagedTreeMetadataBytes = MaxRsyncTreeMetadataBytes

type rsyncTreeCommitProbe struct {
	HardlinkVerified        bool
	RenameNoReplaceVerified bool
	DirectoryFsyncVerified  bool
}

type rsyncTreeFilesystemStats struct {
	BlockSize       int64
	AvailableBlocks uint64
	FreeInodes      uint64
}

// rsyncManagedTree holds a trusted directory descriptor for the managed root.
// Its root path is private provider state and must not cross a DTO, audit, or
// log boundary.
type rsyncManagedTree struct {
	rootPath    string
	rootFD      int
	rootDevice  uint64
	rootInode   uint64
	rootMountID uint64
	fsync       func(int) error
	linkat      func(int, string, int, string, int) error
	renameat2   func(int, string, int, string, uint) error
	statfs      func(int, *rsyncTreeFilesystemStats) error
}

func (tree *rsyncManagedTree) RootPath() string {
	if tree == nil {
		return ""
	}
	return tree.rootPath
}

func normalizeRsyncManagedRoot(path string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || clean == string(filepath.Separator) || !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%w: invalid managed root", errRsyncManagedTreeUnsafe)
	}
	return clean, nil
}

func validRsyncManagedTreeComponent(value string) bool {
	return validManagedTreeComponent(value)
}

func (tree *rsyncManagedTree) sync(fd int) error {
	if tree == nil || tree.fsync == nil {
		return errRsyncManagedTreeUnsupported
	}
	if err := tree.fsync(fd); err != nil {
		return fmt.Errorf("%w: fsync", errRsyncManagedTreeUnsafe)
	}
	return nil
}

func rsyncManagedTreeSystemError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) {
		return fmt.Errorf("%w: %w", errRsyncManagedTreeUnsupported, err)
	}
	return fmt.Errorf("%w: %w", errRsyncManagedTreeUnsafe, err)
}
