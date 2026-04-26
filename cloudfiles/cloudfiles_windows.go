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
	"sync"
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

// Start registers the sync root with the OS. The connect step (which
// requires a FETCH_DATA callback) is deferred to a later phase — calling
// CfConnectSyncRoot with an empty callback table is rejected by cldapi.dll
// with an access violation, so it has to wait until phase 6 wires real
// callbacks in.
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

// Stop is the counterpart to Start. Currently a no-op (besides flipping
// the started flag) since Start does not call CfConnectSyncRoot yet. Once
// connect is wired in phase 6, Stop will disconnect.
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

// Create / Update / Pin / Unpin land in later phases.
func (p *winProvider) Create(absPath string, r webdav.Resource) error { return errNotImplemented }
func (p *winProvider) Update(absPath string, r webdav.Resource) error { return errNotImplemented }
func (p *winProvider) Pin(relPath string) error                       { return errNotImplemented }
func (p *winProvider) Unpin(relPath string) error                     { return errNotImplemented }

// hresultErr converts a non-zero HRESULT into a Go error.
func hresultErr(hr int32, op string) error {
	if hr == 0 {
		return nil
	}
	return fmt.Errorf("cloudfiles: %s failed: HRESULT 0x%08x", op, uint32(hr))
}

var _ Provider = (*winProvider)(nil)
