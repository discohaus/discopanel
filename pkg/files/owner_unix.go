//go:build !windows

package files

import (
	"os"
	"syscall"
)

// Returns owner uid and gid, ok false when unknown
func Owner(path string) (int, int, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}
