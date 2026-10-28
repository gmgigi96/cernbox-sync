//go:build !windows

package engine

import "strings"

// isHidden reports whether the given file system entry should be considered
// hidden on the current platform.
//
// On Unix-like systems (Linux, macOS) a name is hidden when it starts with a
// dot ("."), which is the conventional POSIX hidden-file marker.
func isHidden(name string, _ string) bool {
	return strings.HasPrefix(name, ".")
}

// validLocalName reports whether name can be stored on the local file system.
// Any name the server sends is valid on Unix.
func validLocalName(string) bool { return true }
