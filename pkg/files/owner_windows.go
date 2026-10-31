//go:build windows

package files

import "os"

// Windows has no posix owner ids, ok is always false
func Owner(path string) (int, int, bool) {
	return 0, 0, false
}

// Windows has no posix owner ids, ok is always false
func InfoOwner(info os.FileInfo) (int, int, bool) {
	return 0, 0, false
}
