//go:build !unix && !windows

package engine

import "io/fs"

// fileIdentity is not available on this platform: local renames are then
// handled as a delete plus an upload.
func fileIdentity(string, fs.FileInfo) (dev, ino uint64) { return 0, 0 }
