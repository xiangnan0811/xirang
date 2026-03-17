//go:build linux

package handlers

import "syscall"

func fillDiskInfo(path string, result *verifyMountResult) {
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(path, &statfs); err != nil {
		return
	}
	result.TotalGB = (statfs.Blocks * uint64(statfs.Bsize)) / (1024 * 1024 * 1024)
	result.FreeGB = (statfs.Bavail * uint64(statfs.Bsize)) / (1024 * 1024 * 1024)
	result.Filesystem = detectFilesystemLinux(statfs.Type)
}

// detectFilesystemLinux 根据 Linux 文件系统 magic number 返回可读名称
func detectFilesystemLinux(fsType int64) string {
	switch fsType {
	case 0x6969:
		return "nfs"
	case 0xFF534D42, 0xFE534D42:
		return "cifs"
	case 0xEF53:
		return "ext4"
	case 0x58465342:
		return "xfs"
	case 0x9123683E:
		return "btrfs"
	case 0x01021994:
		return "tmpfs"
	default:
		return "unknown"
	}
}
