// cernbox-cf.h - C ABI surface of the Cloud Files registration shim.
//
// This DLL wraps StorageProviderSyncRootManager.Register / .Unregister
// (Windows.Storage.Provider, WinRT). The Go side calls these entry points
// via syscall.SyscallN (LazyDLL) - same pattern as cfapi_syscall_windows.go,
// no cgo, no exception-handler interception.
//
// Returning HRESULT (S_OK == 0, anything else == failure). Strings are
// UTF-16, NUL-terminated. Pointers may be NULL only where explicitly noted.

#pragma once

#include <stdint.h>
#include <windows.h>     // GUID

#ifdef CERNBOX_CF_EXPORTS
#define CERNBOX_CF_API extern "C" __declspec(dllexport)
#else
#define CERNBOX_CF_API extern "C" __declspec(dllimport)
#endif

// Returns 1 if the calling process has MSIX package identity, 0 otherwise.
// StorageProviderSyncRootManager.Register requires identity and will throw
// E_NO_PACKAGE_IDENTITY if called from an unpackaged process. The daemon
// uses this as a precondition check before attempting registration.
CERNBOX_CF_API int32_t cernbox_cf_has_package_identity(void);

// Register a sync root with the modern Cloud Files / WinRT subsystem.
// Idempotent: re-registering the same id is treated as an update.
//
//   id              UTF-16, opaque sync-root identifier (must be unique within
//                   the provider). Format: "<provider>!<account>!<folder>".
//                   Used by the OS as the registry key name and as the
//                   Unregister handle.
//   localRoot       UTF-16, absolute path to the local sync directory.
//                   Must exist; the API resolves it via
//                   StorageFolder.GetFolderFromPathAsync.
//   displayName     UTF-16, user-facing folder name shown in Explorer.
//   providerVersion UTF-16, e.g. "1.0.0". Stored in the registration
//                   metadata; not user-visible.
//   providerId      Pointer to a 16-byte GUID identifying our provider.
//                   Same value as the legacy CfRegisterSyncRoot path used
//                   so existing tooling that filters by ProviderId still
//                   matches our entries.
//   iconResource    UTF-16, resource path for the namespace icon, e.g.
//                   "C:\path\to\cernbox-cf.dll,-101". May be NULL to use
//                   the OS default cloud-folder icon.
//
// Returns S_OK on success or an HRESULT (most commonly E_NO_PACKAGE_IDENTITY
// when called from an unpackaged process).
CERNBOX_CF_API int32_t cernbox_cf_register_sync_root(
    const wchar_t *id,
    const wchar_t *localRoot,
    const wchar_t *displayName,
    const wchar_t *providerVersion,
    const GUID    *providerId,
    const wchar_t *iconResource);

// Unregister a previously-registered sync root by id. Idempotent: returns
// S_OK if no registration exists for the id (the WinRT API throws
// HRESULT_FROM_WIN32(ERROR_NOT_FOUND), folded to S_OK by the wrapper so
// callers don't have to special-case "already gone").
CERNBOX_CF_API int32_t cernbox_cf_unregister_sync_root(const wchar_t *id);
