package migrate

import (
	"os"
	"path/filepath"
	"strings"
)

// runningProcesses lists the names and executables of the running processes
// (best effort). Both are needed: AppImage builds run under a wrapper name
// such as "AppRun.wrapped".
func runningProcesses() []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.Trim(e.Name(), "0123456789") != "" {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		if comm, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
			out = append(out, strings.TrimSpace(string(comm)))
		}
		if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
			out = append(out, exe)
		}
	}
	return out
}
