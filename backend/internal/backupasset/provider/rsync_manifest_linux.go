//go:build linux

package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"

	"golang.org/x/sys/unix"
)

type rsyncTreeManifestWireEntry struct {
	Path          string                `json:"path"`
	Type          rsyncTreeManifestKind `json:"type"`
	Mode          uint32                `json:"mode"`
	UID           uint32                `json:"uid"`
	GID           uint32                `json:"gid"`
	ModTimeNS     int64                 `json:"mtime_ns"`
	Size          uint64                `json:"size"`
	ContentDigest string                `json:"content_sha256,omitempty"`
	LinkTarget    string                `json:"link_target,omitempty"`
}

func buildRsyncTreeManifest(ctx context.Context, rootFD int, limits ManifestLimits) (rsyncTreeManifest, error) {
	if ctx == nil || rootFD < 0 || !validRsyncTreeManifestLimits(limits) {
		return rsyncTreeManifest{}, fmt.Errorf("%w: invalid managed Rsync manifest request", backupasset.ErrInvalidState)
	}
	manifestContext, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	var root unix.Stat_t
	if err := unix.Fstat(rootFD, &root); err != nil {
		return rsyncTreeManifest{}, rsyncManagedTreeSystemError(err)
	}
	if root.Mode&unix.S_IFMT != unix.S_IFDIR {
		return rsyncTreeManifest{}, fmt.Errorf("%w: managed Rsync manifest root is not a directory", errRsyncManagedTreeUnsafe)
	}
	collector := rsyncTreeManifestCollector{ctx: manifestContext, limits: limits}
	rootCopy, err := unix.Openat2(rootFD, ".", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: rsyncManagedTreeResolve,
	})
	if err != nil {
		return rsyncTreeManifest{}, rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(rootCopy) }()
	if err := collector.walk(rootCopy, "", 0); err != nil {
		return rsyncTreeManifest{}, err
	}
	sort.Slice(collector.entries, func(left, right int) bool {
		return collector.entries[left].RelativePath < collector.entries[right].RelativePath
	})
	encoded, err := encodeRsyncTreeManifestEntries(collector.entries, limits)
	if err != nil {
		return rsyncTreeManifest{}, err
	}
	digest := sha256.Sum256(encoded)
	return rsyncTreeManifest{
		DigestAlgorithm: "sha256",
		Digest:          hex.EncodeToString(digest[:]),
		EntryCount:      uint64(len(collector.entries)),
		LogicalBytes:    collector.logicalBytes,
		Encoded:         encoded,
		Entries:         append([]rsyncTreeManifestEntry(nil), collector.entries...),
	}, nil
}

type rsyncTreeManifestCollector struct {
	ctx          context.Context
	limits       ManifestLimits
	entries      []rsyncTreeManifestEntry
	logicalBytes uint64
}

func (collector *rsyncTreeManifestCollector) walk(directoryFD int, prefix string, depth int) error {
	if err := collector.ctx.Err(); err != nil {
		return err
	}
	if depth > collector.limits.MaxDepth {
		return fmt.Errorf("%w: managed Rsync manifest exceeds maximum depth", errRsyncManagedTreeUnsafe)
	}
	names, err := rsyncTreeDirectoryNames(directoryFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := collector.ctx.Err(); err != nil {
			return err
		}
		relativePath := name
		if prefix != "" {
			relativePath = prefix + "/" + name
		}
		if len(relativePath) > collector.limits.MaxRecordBytes || !utf8.ValidString(relativePath) {
			return fmt.Errorf("%w: invalid managed Rsync manifest path", errRsyncManagedTreeUnsafe)
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return rsyncManagedTreeSystemError(err)
		}
		entry, childFD, err := collector.entry(directoryFD, name, relativePath, stat)
		if err != nil {
			return err
		}
		if err := collector.append(entry); err != nil {
			if childFD >= 0 {
				_ = unix.Close(childFD)
			}
			return err
		}
		if childFD >= 0 {
			err := collector.walk(childFD, relativePath, depth+1)
			closeErr := unix.Close(childFD)
			if err != nil {
				return err
			}
			if closeErr != nil {
				return rsyncManagedTreeSystemError(closeErr)
			}
		}
	}
	return nil
}

func (collector *rsyncTreeManifestCollector) entry(parentFD int, name, relativePath string, stat unix.Stat_t) (rsyncTreeManifestEntry, int, error) {
	entry, err := rsyncTreeManifestEntryFromStat(relativePath, stat)
	if err != nil {
		return rsyncTreeManifestEntry{}, -1, err
	}
	switch entry.Kind {
	case rsyncTreeManifestDirectory:
		childFD, err := unix.Openat2(parentFD, name, &unix.OpenHow{
			Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: rsyncManagedTreeResolve,
		})
		if err != nil {
			return rsyncTreeManifestEntry{}, -1, rsyncManagedTreeSystemError(err)
		}
		var opened unix.Stat_t
		if err := unix.Fstat(childFD, &opened); err != nil {
			_ = unix.Close(childFD)
			return rsyncTreeManifestEntry{}, -1, rsyncManagedTreeSystemError(err)
		}
		if opened.Dev != stat.Dev || opened.Ino != stat.Ino {
			_ = unix.Close(childFD)
			return rsyncTreeManifestEntry{}, -1, fmt.Errorf("%w: managed Rsync directory changed during manifest", errRsyncManagedTreeUnsafe)
		}
		return entry, childFD, nil
	case rsyncTreeManifestRegular:
		contentDigest, err := rsyncTreeRegularDigest(collector.ctx, parentFD, name, stat)
		if err != nil {
			return rsyncTreeManifestEntry{}, -1, err
		}
		entry.ContentDigest = contentDigest
		return entry, -1, nil
	case rsyncTreeManifestSymlink:
		linkTarget, err := rsyncTreeLinkTarget(parentFD, name, collector.limits.MaxRecordBytes)
		if err != nil {
			return rsyncTreeManifestEntry{}, -1, err
		}
		entry.LinkTarget = linkTarget
		return entry, -1, nil
	default:
		return rsyncTreeManifestEntry{}, -1, fmt.Errorf("%w: unsupported managed Rsync manifest node", errRsyncManagedTreeUnsafe)
	}
}

func (collector *rsyncTreeManifestCollector) append(entry rsyncTreeManifestEntry) error {
	if int64(len(collector.entries)) >= collector.limits.MaxEntries {
		return fmt.Errorf("%w: managed Rsync manifest exceeds entry limit", errRsyncManagedTreeUnsafe)
	}
	if entry.Kind == rsyncTreeManifestRegular {
		if entry.Size > math.MaxUint64-collector.logicalBytes {
			return fmt.Errorf("%w: managed Rsync logical byte overflow", errRsyncManagedTreeUnsafe)
		}
		collector.logicalBytes += entry.Size
	}
	collector.entries = append(collector.entries, entry)
	return nil
}

func rsyncTreeDirectoryNames(directoryFD int) ([]string, error) {
	buffer := make([]byte, 4096)
	names := make([]string, 0)
	for {
		count, err := unix.ReadDirent(directoryFD, buffer)
		if err != nil {
			return nil, rsyncManagedTreeSystemError(err)
		}
		if count == 0 {
			break
		}
		_, _, parsed := unix.ParseDirent(buffer[:count], -1, nil)
		for _, name := range parsed {
			if name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
				return nil, fmt.Errorf("%w: invalid managed Rsync directory entry", errRsyncManagedTreeUnsafe)
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func rsyncTreeManifestEntryFromStat(relativePath string, stat unix.Stat_t) (rsyncTreeManifestEntry, error) {
	if stat.Size < 0 {
		return rsyncTreeManifestEntry{}, fmt.Errorf("%w: invalid managed Rsync file size", errRsyncManagedTreeUnsafe)
	}
	entry := rsyncTreeManifestEntry{
		RelativePath: relativePath,
		Mode:         uint32(stat.Mode & 0o7777),
		UID:          stat.Uid,
		GID:          stat.Gid,
		ModTimeNS:    stat.Mtim.Sec*int64(timeSecondNanoseconds) + stat.Mtim.Nsec,
		Size:         uint64(stat.Size),
		Device:       uint64(stat.Dev),
		Inode:        stat.Ino,
		Nlink:        uint64(stat.Nlink),
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		entry.Kind = rsyncTreeManifestDirectory
	case unix.S_IFREG:
		entry.Kind = rsyncTreeManifestRegular
	case unix.S_IFLNK:
		entry.Kind = rsyncTreeManifestSymlink
	default:
		return rsyncTreeManifestEntry{}, fmt.Errorf("%w: unsupported managed Rsync manifest node", errRsyncManagedTreeUnsafe)
	}
	return entry, nil
}

const timeSecondNanoseconds = 1_000_000_000

func rsyncTreeRegularDigest(ctx context.Context, parentFD int, name string, expected unix.Stat_t) (string, error) {
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: rsyncManagedTreeResolve,
	})
	if err != nil {
		return "", rsyncManagedTreeSystemError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return "", rsyncManagedTreeSystemError(err)
	}
	if opened.Dev != expected.Dev || opened.Ino != expected.Ino || opened.Size != expected.Size || opened.Mode != expected.Mode || opened.Mtim != expected.Mtim {
		return "", fmt.Errorf("%w: managed Rsync regular file changed during manifest", errRsyncManagedTreeUnsafe)
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var readBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, err := unix.Read(fd, buffer)
		if count > 0 {
			readBytes += int64(count)
			if _, writeErr := hash.Write(buffer[:count]); writeErr != nil {
				return "", writeErr
			}
		}
		if err != nil {
			return "", rsyncManagedTreeSystemError(err)
		}
		if count == 0 {
			break
		}
	}
	if readBytes != expected.Size {
		return "", fmt.Errorf("%w: managed Rsync regular file size drift", errRsyncManagedTreeUnsafe)
	}
	var final unix.Stat_t
	if err := unix.Fstat(fd, &final); err != nil {
		return "", rsyncManagedTreeSystemError(err)
	}
	if final.Dev != expected.Dev || final.Ino != expected.Ino || final.Size != expected.Size || final.Mode != expected.Mode || final.Mtim != expected.Mtim {
		return "", fmt.Errorf("%w: managed Rsync regular file changed during manifest", errRsyncManagedTreeUnsafe)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func rsyncTreeLinkTarget(parentFD int, name string, maximum int) (string, error) {
	for size := 256; size <= maximum; size *= 2 {
		buffer := make([]byte, size)
		count, err := unix.Readlinkat(parentFD, name, buffer)
		if err != nil {
			return "", rsyncManagedTreeSystemError(err)
		}
		if count < len(buffer) {
			value := string(buffer[:count])
			if !utf8.ValidString(value) {
				return "", fmt.Errorf("%w: invalid managed Rsync symlink target", errRsyncManagedTreeUnsafe)
			}
			return value, nil
		}
	}
	return "", fmt.Errorf("%w: managed Rsync symlink target exceeds limit", errRsyncManagedTreeUnsafe)
}

func encodeRsyncTreeManifestEntries(entries []rsyncTreeManifestEntry, limits ManifestLimits) ([]byte, error) {
	var encoded bytes.Buffer
	for _, entry := range entries {
		wire := rsyncTreeManifestWireEntry{
			Path: entry.RelativePath, Type: entry.Kind, Mode: entry.Mode, UID: entry.UID, GID: entry.GID, ModTimeNS: entry.ModTimeNS,
			Size: entry.Size, ContentDigest: entry.ContentDigest, LinkTarget: entry.LinkTarget,
		}
		line, err := json.Marshal(wire)
		if err != nil {
			return nil, err
		}
		if len(line)+1 > limits.MaxRecordBytes || int64(encoded.Len()+len(line)+1) > limits.MaxBytes {
			return nil, fmt.Errorf("%w: managed Rsync manifest exceeds byte limit", errRsyncManagedTreeUnsafe)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	return encoded.Bytes(), nil
}
