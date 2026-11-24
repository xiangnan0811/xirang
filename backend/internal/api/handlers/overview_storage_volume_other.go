//go:build !linux

package handlers

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func newDefaultMountVolumeStatter() mountVolumeStatFunc {
	return func(path string) (string, unix.Statfs_t, error) {
		var stat unix.Statfs_t
		if err := unix.Statfs(path, &stat); err != nil {
			return "", stat, err
		}
		var fileStat unix.Stat_t
		if err := unix.Stat(path, &fileStat); err != nil {
			return "", stat, err
		}
		return fmt.Sprintf("dev:%d", uint64(fileStat.Dev)), stat, nil
	}
}
