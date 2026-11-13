//go:build linux || darwin

package disk

import (
	"strconv"
	"syscall"
)

// usage is the free (for an unprivileged user) and total bytes of the
// filesystem path is on.
func usage(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize) // int64 on Linux, uint32 on macOS
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}

// deviceID names the filesystem path is on.
func deviceID(path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(st.Dev), 10), nil // its type differs by system
}
