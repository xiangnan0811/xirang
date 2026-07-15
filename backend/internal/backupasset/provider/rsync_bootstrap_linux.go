//go:build linux

package provider

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"xirang/backend/internal/backupasset"

	"golang.org/x/sys/unix"
)

func bootstrapRsyncManagedRoot(ctx context.Context, request RsyncManagedRootBootstrapRequest) (RsyncManagedRootBootstrapEvidence, error) {
	if err := ctx.Err(); err != nil {
		return RsyncManagedRootBootstrapEvidence{}, err
	}
	created, err := createTrustedRsyncManagedRoot(request.ManagedRoot)
	if err != nil {
		return RsyncManagedRootBootstrapEvidence{}, err
	}
	tree, err := openRsyncManagedTreeBase(request.ManagedRoot)
	if err != nil {
		return RsyncManagedRootBootstrapEvidence{}, err
	}
	defer func() { _ = tree.Close() }()

	var marker []byte
	if created {
		marker, err = encodeRsyncManagedRootMarkerV1(request)
		if err == nil {
			err = tree.writeRepositoryMarker(marker)
		}
	} else {
		marker, err = tree.readRepositoryMarker()
		if err == nil {
			err = decodeRsyncManagedRootMarkerV1(marker, request)
		}
	}
	if err != nil {
		return RsyncManagedRootBootstrapEvidence{}, err
	}
	for _, component := range []string{"staging", "points"} {
		if err := ensureRsyncManagedTreeControlDir(tree, component); err != nil {
			return RsyncManagedRootBootstrapEvidence{}, err
		}
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return RsyncManagedRootBootstrapEvidence{}, err
	}
	markerDigest := rsyncTreeDigest(marker)
	return RsyncManagedRootBootstrapEvidence{
		Created: created, RepositoryMarkerDigest: markerDigest, ManagedRootIdentityDigest: tree.identityDigest(markerDigest),
	}, nil
}

func validateRsyncManagedRootSeparation(ctx context.Context, managedRoot, otherRoot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tree, err := openRsyncManagedTreeReadOnly(managedRoot)
	if err != nil {
		return err
	}
	defer func() { _ = tree.Close() }()
	if _, err := tree.validateLocalSourceRoot(otherRoot); err != nil {
		return err
	}
	return ctx.Err()
}

func createTrustedRsyncManagedRoot(path string) (bool, error) {
	root, err := normalizeRsyncManagedRoot(path)
	if err != nil {
		return false, err
	}
	parentPath := filepath.Dir(root)
	component := filepath.Base(root)
	if component == "." || component == string(filepath.Separator) || component == ".." || strings.ContainsRune(component, '\x00') {
		return false, fmt.Errorf("%w: invalid managed Rsync root component", backupasset.ErrInvalidState)
	}
	slashFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(slashFD) }()
	parentRelative := strings.TrimPrefix(filepath.Clean(parentPath), "/")
	parentFD, err := unix.Openat2(slashFD, parentRelative, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return false, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(parentFD) }()
	if err := unix.Mkdirat(parentFD, component, 0o700); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return false, nil
		}
		return false, rsyncManagedTreeSystemError(err)
	}
	if err := unix.Fsync(parentFD); err != nil {
		return false, rsyncManagedTreeSystemError(err)
	}
	return true, nil
}

func (tree *rsyncManagedTree) writeRepositoryMarker(marker []byte) error {
	if tree == nil || len(marker) == 0 || len(marker) > 64<<10 {
		return fmt.Errorf("%w: invalid managed Rsync root marker", backupasset.ErrInvalidState)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return err
	}
	fd, err := unix.Openat2(tree.rootFD, "repository.json", &unix.OpenHow{
		Flags:   unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC,
		Mode:    0o600,
		Resolve: rsyncManagedTreeResolve,
	})
	if err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	for offset := 0; offset < len(marker); {
		count, writeErr := unix.Write(fd, marker[offset:])
		if count > 0 {
			offset += count
		}
		if writeErr != nil {
			_ = unix.Close(fd)
			return rsyncManagedTreeSystemError(writeErr)
		}
		if count == 0 {
			_ = unix.Close(fd)
			return fmt.Errorf("%w: short managed Rsync root marker write", backupasset.ErrInvalidState)
		}
	}
	if err := tree.sync(fd); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := unix.Close(fd); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	return tree.sync(tree.rootFD)
}
