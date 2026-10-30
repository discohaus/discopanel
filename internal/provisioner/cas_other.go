//go:build !linux

package provisioner

import "os"

// Reflink ioctl is linux only, callers copy instead
func tryReflink(dst, src *os.File) bool {
	return false
}
