// Package cloudfiles provides on-demand sync support backed by the Windows
// Cloud Files API. It implements engine.PlaceholderFS so the engine can
// create placeholders instead of downloading remote file content; the OS
// hydrates each placeholder lazily when a user accesses it.
//
// The package is split into a portable public surface (this file) and a
// platform-specific implementation:
//
//   - cloudfiles_windows.go — real CF API integration via CGo
//   - cloudfiles_other.go   — no-op stub so the project builds on Linux/macOS
//
// Callers always go through New(); on non-Windows platforms it returns
// ErrUnsupported so the daemon can fall back to normal sync.
package cloudfiles

import (
	"context"
	"errors"
	"io"

	"github.com/gmgigi96/cernbox-sync/engine"
)

// ErrUnsupported is returned by New on platforms without on-demand sync
// support (everything except Windows for now).
var ErrUnsupported = errors.New("cloudfiles: on-demand sync is only supported on Windows")

// FetchFunc is invoked by the OS callback layer to hydrate a placeholder.
// It must return a ReadCloser streaming the remote bytes for relPath
// (relative to the sync root). engine.FetchFile is the typical
// implementation.
//
// The callback runs on a CF-API thread; the hydration itself is performed
// on a separate worker goroutine, so FetchFunc may block on I/O.
type FetchFunc func(ctx context.Context, relPath string) (io.ReadCloser, error)

// Config configures a per-folder sync provider.
type Config struct {
	// LocalRoot is the absolute path of the local directory that becomes
	// the sync root.
	LocalRoot string

	// FolderName is the user-facing name shown in Explorer.
	FolderName string

	// Fetch is called when the OS asks to hydrate a placeholder.
	Fetch FetchFunc
}

// SetPinState sets the always-local pin flag on the placeholder at absPath.
// pinned == true keeps the OS-cached content for the file even under disk
// pressure; pinned == false reverts to the default lazy state.
//
// The function operates on an existing placeholder via a file handle and
// does not require a connected sync root, so the daemon can call it
// directly in response to a pin/unpin command without spinning up a
// Provider. On non-Windows platforms it returns ErrUnsupported.
//
// Implemented in cloudfiles_windows.go and cloudfiles_other.go.
var _ = (func(string, bool) error)(SetPinState)

// providerName is the brand stamped on every sync root we register. Kept
// portable (here, not in cloudfiles_windows.go) so SyncRootIDFor's
// platform-specific implementations have a single source of truth.
const providerName = "cernbox-sync"

// SyncRootIDFor returns the canonical StorageProviderSyncRootInfo.Id for
// the sync root rooted at localRoot. Stable across daemon restarts so
// re-registration is idempotent and Unregister can find the entry from
// the path alone.
//
// Implemented per-platform: see cloudfiles_windows.go (the real one,
// which builds `<provider>!<sid>!<base64-sha1(localRoot)>` mirroring
// the working ownCloud client implementation) and cloudfiles_other.go
// (a trivial stub since non-Windows builds have no Cloud Files concept).

// Provider owns one CF-API sync root and exposes the placeholder operations
// used by the engine (engine.PlaceholderFS) plus lifecycle and pinning.
//
// On non-Windows builds the concrete type is a stub whose methods all
// return ErrUnsupported; New() refuses to construct one.
type Provider interface {
	engine.PlaceholderFS

	// Start registers the sync root with the OS (if not already registered)
	// and connects the callback table. Must be called before any placeholder
	// operations.
	Start(ctx context.Context) error

	// Stop disconnects the sync root from callbacks. Pending hydrations are
	// allowed to drain. Does not unregister the sync root from the OS — the
	// folder remains visible across daemon restarts.
	Stop() error

	// Pin marks relPath (relative to LocalRoot) as always-local: the file's
	// content is hydrated and the OS keeps it cached even under disk pressure.
	Pin(relPath string) error

	// Unpin reverts relPath to the default unpinned state. The OS may evict
	// content to free disk space.
	Unpin(relPath string) error
}
