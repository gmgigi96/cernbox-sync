//go:build windows

package cloudfiles

// LazyDLL bridge to cernbox-cf.dll (cloudfiles/winrt/cernbox-cf.cpp).
//
// cernbox-cf.dll wraps StorageProviderSyncRootManager.Register / .Unregister
// (Windows.Storage.Provider, WinRT) via C++/WinRT. We can't call the WinRT
// API directly from Go - the projection lives in the C++/WinRT headers
// shipped with the Windows SDK and is impractical to mirror in Go's syscall
// machinery, and MinGW's own C++/WinRT support is too patchy to do it from
// cgo. So we ship a tiny MSVC-built shim DLL alongside the daemon and call
// it via the same syscall.SyscallN pattern cfapi_syscall_windows.go uses
// for cldapi.dll.
//
// Currently NOT yet wired into winProvider.register / .Unregister - that
// swap happens in the next step, after the DLL is verified to compile in
// the VM. Until then the legacy CfRegisterSyncRoot path stays active.

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cfShimDLLName is the DLL we LoadLibrary at runtime. Plain filename (not
// an absolute path) so Windows resolves it through the standard DLL search
// order: %PATH%, the directory of the calling .exe (cernbox-syncd.exe),
// and the package install directory. Inside the MSIX the daemon and the
// shim both live in the package install root, so the directory-of-the-exe
// rule finds it without any extra search-path setup.
const cfShimDLLName = "cernbox-cf.dll"

var (
	cfShimOnce       sync.Once
	cfShimErr        error
	cfShimDLL        *windows.LazyDLL
	pHasPackageId    *windows.LazyProc
	pRegisterRoot    *windows.LazyProc
	pUnregisterRoot  *windows.LazyProc
)

// loadCfShim resolves cernbox-cf.dll lazily on first call. Errors are
// cached so repeated callers don't re-attempt LoadLibrary on a missing
// DLL. Returns ErrShimNotFound specifically when the DLL isn't reachable
// (e.g., running unpackaged from a dev tree without the shim built),
// other errors for malformed DLLs / missing exports.
func loadCfShim() error {
	cfShimOnce.Do(func() {
		cfShimDLL = windows.NewLazyDLL(cfShimDLLName)
		if err := cfShimDLL.Load(); err != nil {
			cfShimErr = fmt.Errorf("%w: %v", ErrShimNotFound, err)
			return
		}
		pHasPackageId = cfShimDLL.NewProc("cernbox_cf_has_package_identity")
		pRegisterRoot = cfShimDLL.NewProc("cernbox_cf_register_sync_root")
		pUnregisterRoot = cfShimDLL.NewProc("cernbox_cf_unregister_sync_root")
		for name, proc := range map[string]*windows.LazyProc{
			"cernbox_cf_has_package_identity":  pHasPackageId,
			"cernbox_cf_register_sync_root":    pRegisterRoot,
			"cernbox_cf_unregister_sync_root":  pUnregisterRoot,
		} {
			if err := proc.Find(); err != nil {
				cfShimErr = fmt.Errorf("GetProcAddress(%s): %w", name, err)
				return
			}
		}
	})
	return cfShimErr
}

// ErrShimNotFound is returned when cernbox-cf.dll can't be located at
// runtime - typically because the daemon is running from a dev build that
// hasn't compiled the shim yet, or because the file was excluded from the
// MSIX staging step. The daemon treats this as a hard error: on-demand
// sync is MSIX-only from this version onwards, so a missing shim means
// on-demand can't proceed.
var ErrShimNotFound = errors.New("cloudfiles: cernbox-cf.dll not found")

// HasPackageIdentity reports whether the calling process has MSIX package
// identity. StorageProviderSyncRootManager.Register requires identity;
// without it the WinRT call throws E_NO_PACKAGE_IDENTITY. The daemon
// checks this once at startup and refuses to set up on-demand if false
// (the engine falls back to plain download/upload sync).
//
// Implemented in cernbox-cf.dll because GetCurrentPackageFamilyName lives
// in kernel32 and is callable directly via syscall, but co-locating it
// with the registration helpers keeps the "is this build packaged?" check
// tied to the same DLL-loaded state - if the shim isn't reachable, on-
// demand can't run anyway, so reporting "no identity" is the right
// fallback.
func HasPackageIdentity() (bool, error) {
	if err := loadCfShim(); err != nil {
		return false, err
	}
	r1, _, _ := syscall.SyscallN(pHasPackageId.Addr())
	return r1 != 0, nil
}

// RegisterSyncRootWinRT registers a sync root via the modern Cloud Files /
// WinRT API. Replaces the legacy CfRegisterSyncRoot path; only callable
// from a process with MSIX package identity (HasPackageIdentity must be
// true).
//
//   id              Stable, opaque sync-root identifier. Used by the OS as
//                   the registry key name and as the Unregister handle.
//                   Format we use: "<provider>!<account>!<folder>".
//   localRoot       Absolute path to the local sync directory.
//   displayName     User-facing folder name.
//   providerVersion e.g. "1.0.0".
//   providerId      Provider GUID; matches kProviderGuid in cfapi_windows.c
//                   so registry tooling that filters by ProviderId still
//                   matches our entries.
//   iconResource    Optional Windows resource path for the namespace icon
//                   (e.g. "<install-dir>\cernbox-cf.dll,-101"). Empty means
//                   use the OS default cloud-folder icon.
func RegisterSyncRootWinRT(
	id, localRoot, displayName, providerVersion string,
	providerId *windows.GUID,
	iconResource string,
) error {
	if err := loadCfShim(); err != nil {
		return err
	}

	// Normalise localRoot: WinRT's StorageFolder.GetFolderFromPathAsync is
	// strict about path shape - relative paths or forward slashes are
	// rejected with E_INVALIDARG.
	abs, err := filepath.Abs(localRoot)
	if err != nil {
		return fmt.Errorf("localRoot abs: %w", err)
	}
	abs = filepath.Clean(abs)

	wId, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return fmt.Errorf("widen id: %w", err)
	}
	wRoot, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return fmt.Errorf("widen localRoot: %w", err)
	}
	wName, err := windows.UTF16PtrFromString(displayName)
	if err != nil {
		return fmt.Errorf("widen displayName: %w", err)
	}
	wVer, err := windows.UTF16PtrFromString(providerVersion)
	if err != nil {
		return fmt.Errorf("widen providerVersion: %w", err)
	}
	var wIcon *uint16
	if iconResource != "" {
		wIcon, err = windows.UTF16PtrFromString(iconResource)
		if err != nil {
			return fmt.Errorf("widen iconResource: %w", err)
		}
	}

	r1, _, _ := syscall.SyscallN(pRegisterRoot.Addr(),
		uintptr(unsafe.Pointer(wId)),
		uintptr(unsafe.Pointer(wRoot)),
		uintptr(unsafe.Pointer(wName)),
		uintptr(unsafe.Pointer(wVer)),
		uintptr(unsafe.Pointer(providerId)),
		uintptr(unsafe.Pointer(wIcon)),
	)
	hr := int32(r1)
	if hr != 0 {
		return fmt.Errorf("StorageProviderSyncRootManager.Register failed: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// UnregisterSyncRootWinRT removes a sync root by id. Idempotent: missing
// registrations are folded to nil inside the shim.
func UnregisterSyncRootWinRT(id string) error {
	if err := loadCfShim(); err != nil {
		return err
	}
	wId, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return fmt.Errorf("widen id: %w", err)
	}
	r1, _, _ := syscall.SyscallN(pUnregisterRoot.Addr(),
		uintptr(unsafe.Pointer(wId)),
	)
	hr := int32(r1)
	if hr != 0 {
		return fmt.Errorf("StorageProviderSyncRootManager.Unregister failed: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// kProviderGUID mirrors the GUID baked into cfapi_windows.c. Kept in sync
// manually - both legacy and WinRT registrations must use the same
// provider id so registry tooling that filters by it still works.
var kProviderGUID = windows.GUID{
	Data1: 0x50319b5b,
	Data2: 0x95bf,
	Data3: 0x43ec,
	Data4: [8]byte{0xbc, 0x95, 0x98, 0x48, 0x1c, 0x9f, 0xa1, 0x9d},
}
