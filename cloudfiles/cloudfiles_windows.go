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
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/gmgigi96/cernbox-sync/webdav"
)

// providerName is the brand stamped on every sync root we register. It also
// serves as the user-visible identity in shell APIs that expose the provider.
const providerName = "cernbox-sync"

// hresultAlreadyExists is HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS) and is
// returned when a sync root with the same path is already registered.
// We treat it as success so re-registering on daemon restart is idempotent.
const hresultAlreadyExists = int32(-2147024713) // 0x800700B7

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
// Format: providerName + "::" + folderName so the same sync root re-binds
// across daemon restarts.
func (p *winProvider) syncRootID() string {
	return providerName + "::" + p.cfg.FolderName
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

// register installs p.cfg.LocalRoot as a Cloud Files sync root via the
// legacy CfRegisterSyncRoot API in cldapi.dll. The WinRT path
// (StorageProviderSyncRootManager.Register) does the same work plus
// some metadata the registry-direct route can't reproduce, but its C++
// vtable surface is fragile to mirror by hand. CfRegisterSyncRoot is a
// stable C ABI and writes BOTH the SyncRootManager registry entries and
// the IO_REPARSE_TAG_CLOUD reparse point on the directory — without the
// reparse point CfConnectSyncRoot returns ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT.
// Idempotent: the C wrapper folds ERROR_ALREADY_EXISTS into S_OK.
func (p *winProvider) register() error {
	cPath := C.CString(p.cfg.LocalRoot)
	defer C.free(unsafe.Pointer(cPath))
	cName := C.CString(p.cfg.FolderName)
	defer C.free(unsafe.Pointer(cName))

	hr := int32(C.cf_register_sync_root(cPath, cName))
	if hr == hresultAlreadyExists {
		return nil
	}
	return hresultErr(hr, "CfRegisterSyncRoot")
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
// removed from the daemon's config; callers should Stop() first.
func (p *winProvider) Unregister() error {
	cPath := C.CString(p.cfg.LocalRoot)
	defer C.free(unsafe.Pointer(cPath))
	return hresultErr(int32(C.cf_unregister_sync_root(cPath)), "CfUnregisterSyncRoot")
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
