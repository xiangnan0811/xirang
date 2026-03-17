//go:build darwin

package handlers

import "syscall"

func fillDiskInfo(path string, result *verifyMountResult) {
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(path, &statfs); err != nil {
		return
	}
	result.TotalGB = (statfs.Blocks * uint64(statfs.Bsize)) / (1024 * 1024 * 1024)
	result.FreeGB = (statfs.Bavail * uint64(statfs.Bsize)) / (1024 * 1024 * 1024)
	// macOS Statfs_t 包含 Fstypename 字段，可直接读取文件系统名称
	result.Filesystem = int8SliceToString(statfs.Fstypename[:])
}

func int8SliceToString(s []int8) string {
	buf := make([]byte, 0, len(s))
	for _, b := range s {
		if b == 0 {
			break
		}
		buf = append(buf, byte(b))
	}
	return string(buf)
}
