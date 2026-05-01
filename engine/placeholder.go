package engine

import "github.com/gmgigi96/cernbox-sync/webdav"

// PlaceholderFS abstracts OS-level placeholder file operations used in
// on-demand sync mode.
//
// In on-demand mode the engine creates placeholders instead of downloading
// file content; the OS (via the Cloud Files API on Windows) hydrates each
// placeholder lazily when a user accesses it. The hydration path is handled
// outside the engine via FetchFile.
//
// On non-Windows builds and in normal sync mode the engine does not use
// PlaceholderFS (Config.Placeholders is nil and the download path runs as
// before).
type PlaceholderFS interface {
	// Create creates a new placeholder at absPath. The placeholder appears
	// as an ordinary file with r.Size and r.LastModified, but its content
	// is not stored locally until hydrated.
	Create(absPath string, r webdav.Resource) error

	// Update updates an existing placeholder's metadata to match r. If the
	// placeholder had been hydrated, the OS marks the cached content stale
	// so the next access re-hydrates from the cloud.
	Update(absPath string, r webdav.Resource) error
}
