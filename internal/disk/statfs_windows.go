//go:build windows

package disk

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
	getVolumePathNameW = kernel32.NewProc("GetVolumePathNameW")
)

// usage is the free (for the calling user) and total bytes of the volume
// path is on.
func usage(path string) (free, total uint64, err error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var avail, size, all uint64
	r, _, e := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&all)))
	if r == 0 {
		return 0, 0, e
	}
	return avail, size, nil
}

// deviceID names the volume path is on (its mount point, e.g. C:\).
func deviceID(path string) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, syscall.MAX_PATH+1)
	r, _, e := getVolumePathNameW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return "", e
	}
	return strings.ToLower(syscall.UTF16ToString(buf)), nil
}
