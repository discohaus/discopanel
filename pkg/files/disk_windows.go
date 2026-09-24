//go:build windows

package files

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Returns total and used bytes for the volume at path
func GetDiskSpace(path string) (int64, int64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes int64

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to convert path to UTF16: %w", err)
	}

	ret, _, err := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)

	if ret == 0 {
		return 0, 0, fmt.Errorf("failed to get disk stats for %s: %w", path, err)
	}

	return totalNumberOfBytes, totalNumberOfBytes - totalNumberOfFreeBytes, nil
}
