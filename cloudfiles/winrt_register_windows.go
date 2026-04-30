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
	cfShimOnce      sync.Once
	cfShimErr       error
	cfShimDLL       *windows.LazyDLL
	pHasPackageId   *windows.LazyProc
	pRegisterRoot   *windows.LazyProc
	pUnregisterRoot *windows.LazyProc
)

// loadCfShim resolves cernbox-cf.dll lazily on first call. Errors are
// cached so repeated callers don't re-attempt LoadLibrary on a missing
// DLL. Returns ErrShimNotFound specifically when the DLL isn't reachable
// (e.g., running from a dev tree without the shim built or with the
// build output not on PATH), other errors for malformed DLLs / missing
// exports.
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
			"cernbox_cf_has_package_identity": pHasPackageId,
			"cernbox_cf_register_sync_root":   pRegisterRoot,
			"cernbox_cf_unregister_sync_root": pUnregisterRoot,
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
// runtime - typically because the daemon is running from a dev build
// that hasn't compiled the shim yet (run cloudfiles/winrt/build.ps1)
// or because the build output isn't on PATH / co-located with the
// daemon executable.
var ErrShimNotFound = errors.New("cloudfiles: cernbox-cf.dll not found")

// HasPackageIdentity reports whether the calling process has MSIX package
// identity. Microsoft's docs claim StorageProviderSyncRootManager.Register
// requires identity, but in practice (per the working ownCloud client and
// our own testing) it works fine from an unpackaged Win32 process linked
// against WindowsApp.lib. We still expose this helper so callers that
// want to differentiate packaged vs unpackaged installs can do so, and
// because it doubles as a "shim DLL is loadable" health check.
func HasPackageIdentity() (bool, error) {
	if err := loadCfShim(); err != nil {
		return false, err
	}
	r1, _, _ := syscall.SyscallN(pHasPackageId.Addr())
	return r1 != 0, nil
}

// RegisterSyncRootWinRT registers a sync root via the modern Cloud Files /
// WinRT API. Replaces the legacy CfRegisterSyncRoot path. Works from an
// unpackaged Win32 process - MSIX package identity is not required (see
// the ownCloud desktop client's vfs_win.cpp for prior art).
//
//	id              Stable, opaque sync-root identifier. Used by the OS as
//	                the registry key name and as the Unregister handle.
//	                Format we use: "<provider>!<sid>!<base64-sha1(path)>".
//	localRoot       Absolute path to the local sync directory.
//	displayName     User-facing folder name.
//	providerVersion e.g. "1.0.0".
//	providerId      Provider GUID. Currently IGNORED inside the shim
//	                because the StorageProviderSyncRootInfo.ProviderId
//	                setter has a known crash on Win11 24H2+ (documented
//	                by ownCloud's vfs_win.cpp). Still threaded through
//	                the ABI for forward compatibility.
//	iconResource    Optional Windows resource path for the namespace icon
//	                (e.g. "<install-dir>\cernbox-cf.dll,-101"). Empty means
//	                use the OS default cloud-folder icon.
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
