//go:build linux

package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type strictRootHandle struct{ fd int }

func openStrictRoot(path string) (*strictRootHandle, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("%w: strict root must be absolute", ErrOutsideRoot)
	}
	fd, err := unix.Open(clean, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &strictRootHandle{fd: fd}, nil
}

func (root *strictRootHandle) Close() error { return unix.Close(root.fd) }

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
