//go:build windows

package engine

import (
	"strings"
	"syscall"
)

// isHidden reports whether the given file system entry should be considered
// hidden on Windows.
//
// A file is hidden when either:
//   - Its name starts with a dot (cross-platform convention honoured by many
//     Windows tools such as Git for Windows), or
//   - The FILE_ATTRIBUTE_HIDDEN flag is set on the file.
func isHidden(name string, absPath string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	ptr, err := syscall.UTF16PtrFromString(absPath)
	if err != nil {
		return false
	}
	attrs, err := syscall.GetFileAttributes(ptr)
	if err != nil {
		return false
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
