//go:build windows

package files

// Windows has no posix owner ids, ok is always false
func Owner(path string) (int, int, bool) {
	return 0, 0, false
}
