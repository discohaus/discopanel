package provisioner

import (
	"os"

	"golang.org/x/sys/unix"
)

// Reflinks src into dst when the filesystem supports it
func tryReflink(dst, src *os.File) bool {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())) == nil
}
