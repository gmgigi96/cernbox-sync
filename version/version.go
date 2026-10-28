// Package version holds the client version and the User-Agent derived from it.
package version

import (
	"fmt"
	"runtime"
)

// Version is the client version. Release builds override it with
// -ldflags "-X github.com/gmgigi96/cernbox-sync/version.Version=<v>".
var Version = "0.1.0"

// UserAgent returns the User-Agent sent on every HTTP request (WebDAV and
// login flow). It mimics the Nextcloud desktop client ("mirall/<version>")
// so that servers route its Basic-Auth credentials to app-password
// authentication, and so the login flow grant page can describe the client.
// The GUI builds the same string (gui/src-tauri/src/lib.rs); keep both in sync.
func UserAgent() string {
	return fmt.Sprintf("Mozilla/5.0 (%s) mirall/%s (cernbox-sync)", osName(runtime.GOOS), Version)
}

// osName maps GOOS to the platform names used by the Nextcloud desktop client.
func osName(goos string) string {
	switch goos {
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "darwin":
		return "Macintosh"
	default:
		return goos
	}
}
