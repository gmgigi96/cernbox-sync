//go:build windows

package cloudfiles

/*
#cgo CFLAGS: -DUNICODE -D_UNICODE -D_WIN32_WINNT=0x0A00
#cgo LDFLAGS: -lole32

#include <stdlib.h>
#include "cfwrap.h"
*/
import "C"

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os/user"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/gmgigi96/cernbox-sync/webdav"
)

// SyncRootIDFor returns the StorageProviderSyncRootInfo.Id for the sync
// root at localRoot.
//
// Format: "<provider>!<sid>!<base64-sha1(localRoot)>" - this mirrors
// what the ownCloud desktop client uses (vfs_win.cpp ::registerFolder),
// which is the only known-working configuration on Win11 24H2+. The
// modern Cloud Files API validates ids against the documented
// `<storage_provider_id>!<windows_sid>!<account_id>` shape, but in
// practice rejects anything that isn't structured exactly like this.
// The third component is a hash of the local path rather than a
// human-readable name so the same provider can register multiple
// sync roots for the same user without collisions, and so the id
// stays valid for any path the user picks (no character-set
// surprises in the registry key).
//
// If the SID lookup fails - which would be a deeply unusual condition,
// since the SID lives in the process token - we fall back to a
// two-part id and let Register surface the error.
func SyncRootIDFor(localRoot string) string {
	sum := sha1.Sum([]byte(filepath.Clean(localRoot)))
	hash := base64.StdEncoding.EncodeToString(sum[:])
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return providerName + "!" + u.Uid + "!" + hash
	}
	return providerName + "!" + hash
}

// providerVersionString is stamped into the StorageProviderSyncRootInfo.
// User-invisible but the API requires a non-empty value.
const providerVersionString = "1.0.0"

// errNotImplemented marks Provider methods whose Windows-side bodies will
// be filled in by later phases (placeholder ops, callbacks, pinning).
var errNotImplemented = errors.New("cloudfiles: not implemented yet")

// winProvider is the Windows Cloud Files API-backed implementation of
// Provider. The connection key returned by CfConnectSyncRoot is stored
// under mu so concurrent Start/Stop calls can't lose it.
type winProvider struct {
	cfg Config

	mu      sync.Mutex
	started bool
	connKey int64 // CF_CONNECTION_KEY.Internal; valid only while started
}

// New constructs a Windows-backed Provider. Validation only — registration
// happens lazily in Start.
func New(cfg Config) (Provider, error) {
	if cfg.LocalRoot == "" {
		return nil, errors.New("cloudfiles: LocalRoot is required")
	}
	if cfg.Fetch == nil {
		return nil, errors.New("cloudfiles: Fetch is required")
	}
	return &winProvider{cfg: cfg}, nil
}

// Start registers the sync root with the OS via the WinRT
// StorageProviderSyncRootManager API and connects the FETCH_DATA callback
// table. The legacy CfRegisterSyncRoot is no longer used because
// CfConnectSyncRoot rejects sync roots that weren't established through
// the modern path.
func (p *winProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}
	if err := p.register(); err != nil {
		return err
	}
	key, err := p.connectWithCallback()
	if err != nil {
		_ = p.Unregister() // roll back on failure
		return err
	}
	p.connKey = key
	registerProvider(key, p)
	p.started = true
	// Tell the OS the provider is online and idle. Without this call Windows
	// keeps the sync root in a "provider unknown" state, which manifests as
	// sticky OFFLINE + RECALL_ON_DATA_ACCESS attributes on the sync-root
	// directory and a "cloud operation timed out" dialog on every access.
	// Best-effort: a failure here doesn't take down the provider — FETCH_DATA
	// still works on the open connection — so log via the default slog and
	// keep going.
	if hr := int32(C.cf_update_provider_status_idle(C.int64_t(key))); hr != 0 {
		slog.Warn("cloudfiles: CfUpdateSyncProviderStatus(IDLE) failed",
			"folder", p.cfg.FolderName, "hresult", fmt.Sprintf("0x%08x", uint32(hr)))
	}
	return nil
}

// Stop disconnects the sync root from callbacks. The sync root remains
// registered with the OS so the folder keeps its cloud-aware status across
// daemon restarts.
func (p *winProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	hr := int32(C.cf_disconnect_sync_root(C.int64_t(p.connKey)))
	unregisterProvider(p.connKey)
	p.started = false
	p.connKey = 0
	return hresultErr(hr, "CfDisconnectSyncRoot")
}

// syncRootID is the stable identifier we hand to the OS for this folder.
// Delegates to the package-level helper so the format stays in one place.
func (p *winProvider) syncRootID() string {
	return SyncRootIDFor(p.cfg.LocalRoot)
}

// connectWithCallback wires our FETCH_DATA handler. Same syscall-instead-
// of-cgo rationale as connect(); the on_fetch_data trampoline stays in
// the C wrapper so the OS calls back into a stable C function, which
// then forwards into Go via the existing //export goFetchData bridge.
func (p *winProvider) connectWithCallback() (int64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := connectPrereqs(p.cfg.LocalRoot); err != nil {
		return 0, err
	}
	cb := []cfCallbackRegistration{
		{Type: cfCallbackTypeFetchData, Callback: uintptr(C.cf_get_fetch_data_callback())},
		{Type: cfCallbackTypeFetchPlaceholders, Callback: uintptr(C.cf_get_fetch_placeholders_callback())},
	}
	return connectSyncRootSyscall(p.cfg.LocalRoot, cb)
}

// register installs p.cfg.LocalRoot as a sync root via the modern
// StorageProviderSyncRootManager.Register WinRT API, called through the
// cernbox-cf.dll shim (cloudfiles/winrt/cernbox-cf.cpp). Replaces the
// legacy CfRegisterSyncRoot path. Works from packaged and unpackaged
// processes alike (mirrors ownCloud's working VFS plugin behaviour).
// ErrShimNotFound is returned when the shim DLL isn't reachable -
// typically a sign cloudfiles/winrt/build.ps1 hasn't been run, or the
// resulting DLL isn't on PATH alongside the daemon executable.
func (p *winProvider) register() error {
	return RegisterSyncRootWinRT(
		p.syncRootID(),
		p.cfg.LocalRoot,
		p.cfg.FolderName,
		providerVersionString,
		&kProviderGUID,
		"", // iconResource: empty -> OS default cloud-folder icon (Phase 4 polish)
	)
}

// connect calls CfConnectSyncRoot and returns the connection key. Routes
// through golang.org/x/sys/windows.LazyProc rather than cgo to avoid the
// runtime VEH interception that crashes cldapi.dll's internal SEH path
// (see cfapi_syscall_windows.go for the diagnostic background).
func (p *winProvider) connect() (int64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Trigger the C-side privilege + COM init prerequisites once.
	if err := connectPrereqs(p.cfg.LocalRoot); err != nil {
		return 0, err
	}
	return connectSyncRootSyscall(p.cfg.LocalRoot, nil)
}

// Unregister tears down the sync root entirely. Used when a folder is
// removed from the daemon's config; callers should Stop() first. Thin
// wrapper around the package-level UnregisterSyncRoot so the daemon can
// drop a sync root even when no Provider instance is cached for it (e.g.,
// after a daemon restart between registration and removal).
func (p *winProvider) Unregister() error {
	return UnregisterSyncRoot(p.syncRootID())
}

// UnregisterSyncRoot removes the OS-level sync-root registration with the
// given id (typically obtained from SyncRootIDFor). Calls
// StorageProviderSyncRootManager.Unregister via the cernbox-cf.dll shim;
// idempotent (the shim folds ERROR_NOT_FOUND to success so redundant
// cleanup calls are harmless).
//
// This is the package-level counterpart to SetPinState - usable without
// constructing a Provider, so the daemon can clean up after a folder is
// removed even if it never instantiated a provider for it in this run.
func UnregisterSyncRoot(syncRootID string) error {
	return UnregisterSyncRootWinRT(syncRootID)
}

// Create lays down a placeholder at absPath for the remote resource r.
// The OS exposes the file with r.Size and r.LastModified but holds no
// content — the FETCH_DATA callback (phase 6) hydrates it on first access.
func (p *winProvider) Create(absPath string, r webdav.Resource) error {
	id, idLen := fileIdentity(relPathFromAbs(p.cfg.LocalRoot, absPath))
	defer C.free(id)

	cAbs := C.CString(absPath)
	defer C.free(unsafe.Pointer(cAbs))

	hr := int32(C.cf_create_placeholder(
		cAbs,
		C.int64_t(r.Size),
		C.int64_t(timeToFiletime(r.LastModified)),
		id,
		C.int32_t(idLen),
	))
	return hresultErr(hr, "CfCreatePlaceholders")
}

// Update refreshes an existing placeholder's metadata. The OS marks any
// hydrated content stale via the DEHYDRATE flag so the next access pulls
// the new bytes through FETCH_DATA.
func (p *winProvider) Update(absPath string, r webdav.Resource) error {
	id, idLen := fileIdentity(relPathFromAbs(p.cfg.LocalRoot, absPath))
	defer C.free(id)

	cAbs := C.CString(absPath)
	defer C.free(unsafe.Pointer(cAbs))

	hr := int32(C.cf_update_placeholder(
		cAbs,
		C.int64_t(r.Size),
		C.int64_t(timeToFiletime(r.LastModified)),
		id,
		C.int32_t(idLen),
	))
	return hresultErr(hr, "CfUpdatePlaceholder")
}

// Pin marks relPath (relative to LocalRoot) as always-local. The OS keeps
// the file's content cached even under disk pressure. Pin can be called
// before or after the placeholder has been hydrated; the API does not
// trigger a download by itself.
func (p *winProvider) Pin(relPath string) error   { return p.setPinState(relPath, true) }
func (p *winProvider) Unpin(relPath string) error { return p.setPinState(relPath, false) }

func (p *winProvider) setPinState(relPath string, pinned bool) error {
	return SetPinState(filepath.Join(p.cfg.LocalRoot, relPath), pinned)
}

// SetPinState pins or unpins a placeholder file. See the package-level
// documentation in cloudfiles.go for details.
func SetPinState(absPath string, pinned bool) error {
	cAbs := C.CString(absPath)
	defer C.free(unsafe.Pointer(cAbs))

	state := C.int32_t(0)
	if pinned {
		state = 1
	}
	hr := int32(C.cf_set_pin_state(cAbs, state))
	return hresultErr(hr, "CfSetPinState")
}

// timeToFiletime converts a Go time to a Windows FILETIME (100 ns ticks
// since 1 January 1601 UTC). Returns 0 for zero times.
func timeToFiletime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	const filetimeEpoch = int64(116444736000000000) // 1601 → 1970 in 100 ns ticks
	return filetimeEpoch + t.UnixNano()/100
}

// relPathFromAbs returns absPath relative to localRoot, normalized to use
// forward slashes regardless of platform. The result is stored in the CF
// FileIdentity blob and round-tripped back to us through the FETCH_DATA
// callback; from there it goes straight into a WebDAV URL, where any
// backslash would be percent-encoded as %5C and the GET would 404. Keeping
// the stored form portable means a subdirectory placeholder like
//
//	C:\sync\pictures\vacation\italy\captions.txt
//
// hydrates against
//
//	<remote-base>/pictures/vacation/italy/captions.txt
//
// instead of the encoded backslash variant.
//
// If absPath isn't under localRoot the input is returned unchanged.
func relPathFromAbs(localRoot, absPath string) string {
	rel, err := filepath.Rel(localRoot, absPath)
	if err != nil {
		return absPath
	}
	return filepath.ToSlash(rel)
}

// fileIdentity allocates a UTF-16 buffer holding relPath. The returned
// pointer must be freed by the caller (C.free). The length is in bytes,
// not UTF-16 code units, since the CF API takes a byte-count.
func fileIdentity(relPath string) (unsafe.Pointer, int) {
	utf16 := utf16FromString(relPath)
	bytes := len(utf16) * 2
	buf := C.malloc(C.size_t(bytes))
	if buf == nil {
		return nil, 0
	}
	dst := unsafe.Slice((*uint16)(buf), len(utf16))
	copy(dst, utf16)
	return buf, bytes
}

// utf16FromString converts a Go string to a UTF-16 slice without a null
// terminator. The CF API uses an explicit length so the terminator isn't
// needed and would only inflate the identity blob.
func utf16FromString(s string) []uint16 {
	return utf16.Encode([]rune(s))
}

// hresultErr converts a non-zero HRESULT into a Go error.
func hresultErr(hr int32, op string) error {
	if hr == 0 {
		return nil
	}
	return fmt.Errorf("cloudfiles: %s failed: HRESULT 0x%08x", op, uint32(hr))
}

var _ Provider = (*winProvider)(nil)
