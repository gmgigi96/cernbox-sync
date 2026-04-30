// cernbox-cf.cpp - C++/WinRT implementation of the Cloud Files registration
// shim. Built with MSVC (cl.exe) + Windows 10 SDK 10.0.22621.0+ which ships
// the cppwinrt projection headers. See build.ps1 for compile flags.
//
// Why a separate DLL:
//   StorageProviderSyncRootManager (the modern replacement for the legacy
//   CfRegisterSyncRoot) is a WinRT API. C++/WinRT wraps it cleanly, but
//   MinGW (which we use for the cgo daemon) doesn't have working C++/WinRT
//   support. A small shim DLL compiled with MSVC sidesteps that, and the
//   Go side loads it via syscall.SyscallN - no cgo dependency, same pattern
//   as cfapi_syscall_windows.go uses for cldapi.dll.

#define CERNBOX_CF_EXPORTS
#include "cernbox-cf.h"

#include <appmodel.h>          // GetCurrentPackageFamilyName
#include <winrt/base.h>
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Storage.h>
#include <winrt/Windows.Storage.Provider.h>

using namespace winrt;
using namespace Windows::Storage;
using namespace Windows::Storage::Provider;

namespace {

// init_apartment_once initialises the COM apartment for the calling
// thread on first use. WinRT calls require this; doing it lazily keeps
// the DLL safe to load before any registration is attempted.
//
// We use the multi-threaded apartment (MTA) to match the daemon's
// threading model - the registration calls happen from goroutines that
// don't have a fixed apartment, so MTA is the only sane choice.
struct ApartmentGuard {
    ApartmentGuard() {
        try {
            init_apartment(apartment_type::multi_threaded);
        } catch (hresult_error const& e) {
            // RPC_E_CHANGED_MODE: apartment already initialised in a different
            // mode by the host process. That's fine, we'll use whatever's
            // there - most COM calls work in either apartment.
            if (e.code() != HRESULT(RPC_E_CHANGED_MODE)) {
                throw;
            }
        }
    }
};

void ensure_apartment() {
    static ApartmentGuard guard;
    (void)guard;
}

// hr_from_exception extracts an HRESULT from any C++/WinRT exception.
// Catches both winrt::hresult_error (the projection's exception type) and
// the std::exception base in case something else slips through.
int32_t hr_from_exception() noexcept {
    try {
        throw;
    } catch (hresult_error const& e) {
        return static_cast<int32_t>(e.code().value);
    } catch (...) {
        return static_cast<int32_t>(E_UNEXPECTED);
    }
}

} // anonymous namespace

extern "C" int32_t cernbox_cf_has_package_identity(void) {
    UINT32 length = 0;
    LONG rc = ::GetCurrentPackageFamilyName(&length, nullptr);
    // APPMODEL_ERROR_NO_PACKAGE (15700) means no package identity.
    // ERROR_INSUFFICIENT_BUFFER (122) means a name is available - we don't
    // actually need the name itself, just the success signal.
    return (rc == ERROR_INSUFFICIENT_BUFFER) ? 1 : 0;
}

extern "C" int32_t cernbox_cf_register_sync_root(
    const wchar_t *id,
    const wchar_t *localRoot,
    const wchar_t *displayName,
    const wchar_t *providerVersion,
    const GUID    *providerId,
    const wchar_t *iconResource) {

    if (!id || !localRoot || !displayName || !providerVersion || !providerId) {
        return E_INVALIDARG;
    }

    try {
        ensure_apartment();

        // GetFolderFromPathAsync returns a StorageFolder; .get() blocks the
        // calling thread until completion. Acceptable here because the
        // daemon's registration path runs on a worker goroutine and isn't
        // performance-critical (one call per sync root, on first start).
        auto folder = StorageFolder::GetFolderFromPathAsync(hstring(localRoot)).get();

        StorageProviderSyncRootInfo info;
        info.Id(hstring(id));
        info.Path(folder);
        info.DisplayNameResource(hstring(displayName));
        if (iconResource) {
            info.IconResource(hstring(iconResource));
        }

        // Hydration policy: PARTIAL = on-demand fetch on first read; the
        // OS calls our FETCH_DATA callback. Matches the legacy registration
        // path so existing fetch-callback code keeps working unchanged.
        info.HydrationPolicy(StorageProviderHydrationPolicy::Partial);
        info.HydrationPolicyModifier(StorageProviderHydrationPolicyModifier::None);

        // Population: AlwaysFull means the engine populates every
        // placeholder eagerly; the OS won't ask us via FETCH_PLACEHOLDERS.
        // This is what we already did under the legacy API
        // (CF_POPULATION_POLICY_PARTIAL plus the stub FETCH_PLACEHOLDERS
        // handler); AlwaysFull is the cleaner equivalent here.
        info.PopulationPolicy(StorageProviderPopulationPolicy::AlwaysFull);

        // Track in-sync state from mtime so the OS can avoid spurious
        // re-hydration when the engine touches metadata.
        info.InSyncPolicy(
            StorageProviderInSyncPolicy::FileLastWriteTime |
            StorageProviderInSyncPolicy::DirectoryLastWriteTime);

        info.HardlinkPolicy(StorageProviderHardlinkPolicy::None);
        info.Version(hstring(providerVersion));
        info.ShowSiblingsAsGroup(false);
        info.ProviderId(*providerId);

        StorageProviderSyncRootManager::Register(info);
        return S_OK;
    } catch (...) {
        return hr_from_exception();
    }
}

extern "C" int32_t cernbox_cf_unregister_sync_root(const wchar_t *id) {
    if (!id) {
        return E_INVALIDARG;
    }

    try {
        ensure_apartment();
        StorageProviderSyncRootManager::Unregister(hstring(id));
        return S_OK;
    } catch (hresult_error const& e) {
        // ERROR_NOT_FOUND (1168) wrapped as an HRESULT means there's no
        // registration with that id. Fold to S_OK so retry-after-restart
        // cleanup paths are idempotent.
        if (e.code().value == HRESULT_FROM_WIN32(ERROR_NOT_FOUND)) {
            return S_OK;
        }
        return static_cast<int32_t>(e.code().value);
    } catch (...) {
        return E_UNEXPECTED;
    }
}
