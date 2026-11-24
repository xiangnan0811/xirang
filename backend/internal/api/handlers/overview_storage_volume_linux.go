//go:build linux

package handlers

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type mountInfoEntry struct {
	mountID    uint64
	superDev   string
	mountPoint string
}

type mountInfoTable struct {
	byMountID map[uint64]string
	entries   []mountInfoEntry
}

func parseMountInfo(r io.Reader) *mountInfoTable {
	table := &mountInfoTable{
		byMountID: make(map[uint64]string),
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		mountID, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		superDev := fields[2]
		if superDev == "" {
			continue
		}
		mountPoint := unescapeMountInfoField(fields[4])
		table.byMountID[mountID] = superDev
		table.entries = append(table.entries, mountInfoEntry{
			mountID:    mountID,
			superDev:   superDev,
			mountPoint: filepath.Clean(mountPoint),
		})
	}
	return table
}

func unescapeMountInfoField(raw string) string {
	if !strings.Contains(raw, `\`) {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+3 < len(raw) &&
			raw[i+1] >= '0' && raw[i+1] <= '7' &&
			raw[i+2] >= '0' && raw[i+2] <= '7' &&
			raw[i+3] >= '0' && raw[i+3] <= '7' {
			if v, err := strconv.ParseUint(raw[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(raw[i])
	}
	return b.String()
}

func (t *mountInfoTable) superDevForMountID(mountID uint64) (string, bool) {
	if t == nil {
		return "", false
	}
	dev, ok := t.byMountID[mountID]
	return dev, ok && dev != ""
}

func (t *mountInfoTable) superDevForPath(resolvedPath string) (string, bool) {
	if t == nil || len(t.entries) == 0 {
		return "", false
	}
	cleaned := filepath.Clean(resolvedPath)
	bestLen := -1
	bestDev := ""
	for _, entry := range t.entries {
		mp := entry.mountPoint
		if mp == "" {
			continue
		}
		matched := false
		if mp == "/" {
			matched = strings.HasPrefix(cleaned, "/")
		} else if cleaned == mp || strings.HasPrefix(cleaned, mp+"/") {
			matched = true
		}
		// Use >= so a later shadowed mount at the exact same mountPoint wins.
		if matched && len(mp) >= bestLen {
			bestLen = len(mp)
			bestDev = entry.superDev
		}
	}
	return bestDev, bestDev != ""
}

func loadProcMountInfo() *mountInfoTable {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return parseMountInfo(f)
}

func newDefaultMountVolumeStatter() mountVolumeStatFunc {
	table := loadProcMountInfo()
	return func(path string) (string, unix.Statfs_t, error) {
		var stat unix.Statfs_t
		if err := unix.Statfs(path, &stat); err != nil {
			return "", stat, err
		}
		key, err := resolveLinuxMountVolumeKey(path, table)
		return key, stat, err
	}
}

// resolveLinuxMountVolumeKey identifies the shared backing filesystem for path.
// On Linux, /proc/self/mountinfo field 3 (superblock major:minor) is identical
// across all Btrfs subvolumes and bind mounts of the same filesystem, even when
// stat(2) assigns distinct per-subvolume st_dev IDs and statfs(2) XORs subvolid
// into f_fsid. Preferring STATX_MNT_ID -> mountinfo superblock avoids both.
func resolveLinuxMountVolumeKey(path string, table *mountInfoTable) (string, error) {
	if table != nil {
		var stx unix.Statx_t
		if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_MNT_ID, &stx); err == nil && stx.Mask&unix.STATX_MNT_ID != 0 {
			if superDev, ok := table.superDevForMountID(stx.Mnt_id); ok {
				return "sb:" + superDev, nil
			}
		}
		resolved := path
		if eval, err := filepath.EvalSymlinks(path); err == nil {
			resolved = eval
		}
		if superDev, ok := table.superDevForPath(resolved); ok {
			return "sb:" + superDev, nil
		}
	}
	var fileStat unix.Stat_t
	if err := unix.Stat(path, &fileStat); err != nil {
		return "", err
	}
	return fmt.Sprintf("dev:%d", uint64(fileStat.Dev)), nil
}
