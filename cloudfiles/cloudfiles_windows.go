//go:build windows

package cloudfiles

/*
#cgo CFLAGS: -DUNICODE -D_UNICODE -D_WIN32_WINNT=0x0A00
#cgo LDFLAGS: -lole32

#include <stdlib.h>
#include "cfapi.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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

// Start registers the sync root with the OS.
//
// CfConnectSyncRoot — the call that wires our FETCH_DATA callback to the OS
// — currently crashes inside cldapi.dll for sync roots registered via the
// (deprecated) CfRegisterSyncRoot. The supported path on Win 10/11 is the
// WinRT StorageProviderSyncRootManager.Register API, which sets up extra
// shell metadata that CfConnectSyncRoot depends on. Wiring that requires
// pulling in the C++/WinRT runtime and is tracked as a follow-up; until
// then Start is register-only and the hydration tests are skipped. The
// FETCH_DATA bridge itself (callback_windows.go + cf_execute_transfer)
// is fully implemented and will activate as soon as connect succeeds.
func (p *winProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}
	if err := p.register(); err != nil {
		return err
	}
	p.started = true
	return nil
}

// Stop is the symmetric counterpart to Start. Once connect is wired through
// WinRT registration this will also call cf_disconnect_sync_root and
// unregisterProvider; for now it just flips the started flag.
func (p *winProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	p.started = false
	p.connKey = 0
	return nil
}

// connectWithCallback wires our FETCH_DATA handler. The empty-callback
// variant (cf_connect_sync_root) is kept around for tests that only want
// to validate the registration handshake.
func (p *winProvider) connectWithCallback() (int64, error) {
	cPath := C.CString(p.cfg.LocalRoot)
	defer C.free(unsafe.Pointer(cPath))

	var key C.int64_t
	hr := int32(C.cf_connect_sync_root_with_callback(cPath, &key))
	if hr != 0 {
		return 0, hresultErr(hr, "CfConnectSyncRoot")
	}
	return int64(key), nil
}

// register calls CfRegisterSyncRoot for p.cfg.LocalRoot. Returns nil if the
// sync root is already registered.
func (p *winProvider) register() error {
	cPath := C.CString(p.cfg.LocalRoot)
	defer C.free(unsafe.Pointer(cPath))
	cName := C.CString(providerName)
	defer C.free(unsafe.Pointer(cName))

	hr := int32(C.cf_register_sync_root(cPath, cName))
	if hr == 0 || hr == hresultAlreadyExists {
		return nil
	}
	return hresultErr(hr, "CfRegisterSyncRoot")
}

// connect calls CfConnectSyncRoot and returns the connection key.
func (p *winProvider) connect() (int64, error) {
	cPath := C.CString(p.cfg.LocalRoot)
	defer C.free(unsafe.Pointer(cPath))

	var key C.int64_t
	hr := int32(C.cf_connect_sync_root(cPath, &key))
	if hr != 0 {
		return 0, hresultErr(hr, "CfConnectSyncRoot")
	}
	return int64(key), nil
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

// relPathFromAbs returns absPath relative to localRoot, with forward slashes
// converted to backslashes (Windows convention). If absPath isn't under
// localRoot the input is returned unchanged.
func relPathFromAbs(localRoot, absPath string) string {
	rel, err := filepath.Rel(localRoot, absPath)
	if err != nil {
		return absPath
	}
	return rel
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
