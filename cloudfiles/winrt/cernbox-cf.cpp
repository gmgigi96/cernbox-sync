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
#include <stdio.h>
#include <winrt/base.h>
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Storage.h>
#include <winrt/Windows.Storage.Provider.h>
#include <winrt/Windows.Storage.Streams.h>  // InMemoryRandomAccessStream / IBuffer for Context

using namespace winrt;
using namespace Windows::Storage;
using namespace Windows::Storage::Provider;

namespace {

// Diagnostic trace to a fixed-path log file so we can pinpoint where a
// crash inside the WinRT projection happens. We can't rely on stderr
// reaching the Go test harness through the syscall.SyscallN path, and
// SEH from inside the WinRT call would otherwise just crash the calling
// process before any Go-side logging fires. The log is opened in
// append mode each call so even a hard crash mid-call leaves a partial
// trail. NUL-safe: every write is flushed.
void trace(const wchar_t* msg) {
    FILE* f = nullptr;
    if (_wfopen_s(&f, L"C:\\cernbox-cf-trace.log", L"a, ccs=UTF-8") == 0 && f) {
        SYSTEMTIME st;
        GetSystemTime(&st);
        fwprintf(f, L"%04u-%02u-%02uT%02u:%02u:%02u.%03uZ %ls\n",
                 st.wYear, st.wMonth, st.wDay,
                 st.wHour, st.wMinute, st.wSecond, st.wMilliseconds,
                 msg);
        fflush(f);
        fclose(f);
    }
}

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

    trace(L"register_sync_root: enter");
    if (!id || !localRoot || !displayName || !providerVersion || !providerId) {
        trace(L"register_sync_root: E_INVALIDARG");
        return E_INVALIDARG;
    }

    try {
        trace(L"register_sync_root: ensure_apartment");
        ensure_apartment();

        wchar_t buf[600];
        _snwprintf_s(buf, _TRUNCATE,
                     L"register_sync_root: id=%ls localRoot=%ls", id, localRoot);
        trace(buf);

        // GetFolderFromPathAsync returns a StorageFolder; .get() blocks the
        // calling thread until completion. Acceptable here because the
        // daemon's registration path runs on a worker goroutine and isn't
        // performance-critical (one call per sync root, on first start).
        trace(L"register_sync_root: GetFolderFromPathAsync");
        auto folder = StorageFolder::GetFolderFromPathAsync(hstring(localRoot)).get();
        trace(L"register_sync_root: GetFolderFromPathAsync done");

        trace(L"register_sync_root: build StorageProviderSyncRootInfo");
        StorageProviderSyncRootInfo info;
        info.Id(hstring(id));
        info.Path(folder);

        // DisplayNameResource and IconResource MUST be in the
        // `@dll-path,-resourceid` format (per Microsoft docs and the
        // CloudMirror sample). A plain string fails Register with
        // E_INVALIDARG. If the caller passed plain names ("test-folder"
        // etc.) we substitute a generic shell32 cloud-folder resource
        // reference - the directory's own name is what users actually
        // see in Explorer; DisplayNameResource is just for the
        // SyncRootManager registry entries we don't expose.
        const wchar_t* defaultRes = L"@%SystemRoot%\\System32\\shell32.dll,-2783";
        const wchar_t* dn = (displayName && displayName[0] == L'@') ? displayName : defaultRes;
        info.DisplayNameResource(hstring(dn));
        const wchar_t* ir = (iconResource && iconResource[0] == L'@') ? iconResource : defaultRes;
        info.IconResource(hstring(ir));

        // Hydration / Population / InSync policies. Match the policy set
        // Microsoft's CloudMirror sample uses verbatim - the previous
        // attempt used Partial+AlwaysFull (which is what the legacy
        // CfRegisterSyncRoot path used) but that combo gets rejected by
        // StorageProviderSyncRootManager.Register with E_FAIL on Win11
        // 24H2+. Full+AutoDehydrationAllowed gives effectively the same
        // user-visible behaviour: files are placeholders, hydrated on
        // read, and dehydrated when stale - the OS just doesn't draw
        // the "cloud-only" badge until first access.
        info.HydrationPolicy(StorageProviderHydrationPolicy::Full);
        info.HydrationPolicyModifier(
            StorageProviderHydrationPolicyModifier::AutoDehydrationAllowed);
        info.PopulationPolicy(StorageProviderPopulationPolicy::Full);
        info.InSyncPolicy(
            StorageProviderInSyncPolicy::FileCreationTime |
            StorageProviderInSyncPolicy::DirectoryCreationTime);

        info.HardlinkPolicy(StorageProviderHardlinkPolicy::None);
        info.Version(hstring(providerVersion));
        info.ShowSiblingsAsGroup(false);
        info.ProviderId(*providerId);

        // ProtectionMode + AllowPinning are documented as optional, but
        // in practice on Win11 24H2+ leaving them unset makes Register
        // access-violate inside the deployment service. Set explicit
        // defaults: Unknown (no DRM) and Allowed (so Pin/Unpin works).
        info.ProtectionMode(StorageProviderProtectionMode::Unknown);
        info.AllowPinning(true);

        // RecycleBinUri is documented optional but Microsoft's CloudMirror
        // sample sets it (with a non-functional placeholder URL) and
        // omitting it makes Register fail with E_FAIL on this Win11 build.
        // We don't currently surface a recycle bin to the user; this is
        // a placeholder that satisfies the validation. Phase 4 polish
        // can wire up a real one.
        info.RecycleBinUri(
            Windows::Foundation::Uri(L"https://cernbox.cern.ch/.recyclebin"));

        // Context: opaque IBuffer the OS hands back to us on certain
        // callbacks. Microsoft's CloudMirror sample sets this with a
        // single ASCII char, which we mimic - leaving it unset (or
        // empty) provokes E_FAIL on Win11 24H2+.
        Streams::DataWriter ctxWriter;
        ctxWriter.WriteByte(0x01);
        info.Context(ctxWriter.DetachBuffer());

        trace(L"register_sync_root: StorageProviderSyncRootManager::Register");
        StorageProviderSyncRootManager::Register(info);
        trace(L"register_sync_root: SUCCESS");
        return S_OK;
    } catch (hresult_error const& e) {
        wchar_t buf[300];
        _snwprintf_s(buf, _TRUNCATE,
                     L"register_sync_root: hresult_error 0x%08x msg=%ls",
                     static_cast<uint32_t>(e.code().value),
                     e.message().c_str());
        trace(buf);
        return static_cast<int32_t>(e.code().value);
    } catch (...) {
        trace(L"register_sync_root: caught SEH/unknown -> E_UNEXPECTED");
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
