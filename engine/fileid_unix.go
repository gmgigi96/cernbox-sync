//go:build unix

package engine

import (
	"io/fs"
	"syscall"
)

// fileIdentity returns the device and inode of a file, or zeros when the
// platform does not provide them.
func fileIdentity(_ string, info fs.FileInfo) (dev, ino uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino) //nolint:unconvert // types differ across platforms
	}
	return 0, 0
}
