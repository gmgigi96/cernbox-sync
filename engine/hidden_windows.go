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

// validLocalName reports whether name can be stored on a Windows file system:
// no reserved characters, no trailing space or dot (Windows strips them, so
// the item would come back under another name) and no reserved device name.
func validLocalName(name string) bool {
	if name == "" || strings.ContainsAny(name, `<>:"/\|?*`) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	base, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(strings.TrimRight(base, " ")) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}
