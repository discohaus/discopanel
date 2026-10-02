//go:build !windows

package files

import (
	"fmt"
	"syscall"
)

// Returns total and used bytes for the volume at path
func GetDiskSpace(path string) (int64, int64, error) {
	var stat syscall.Statfs_t

	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, fmt.Errorf("failed to get disk stats for %s: %w", path, err)
	}

	total := int64(stat.Blocks) * int64(stat.Bsize)
	used := total - int64(stat.Bfree)*int64(stat.Bsize)

	return total, used, nil
}
