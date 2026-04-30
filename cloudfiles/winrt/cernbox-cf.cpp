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
//
// Field-set rationale:
//   The set of StorageProviderSyncRootInfo fields below mirrors the working
//   ownCloud desktop client (src/plugins/vfs/win/vfs_win.cpp ::registerFolder
//   in github.com/owncloud/client). Microsoft's own docs are misleading on
//   several points - in particular ProviderId() and RecycleBinUri() trigger
//   AccessViolations on Win11 24H2+ when called, despite both being
//   documented as legitimate setters. ownCloud's working code documents
//   this explicitly with a "// Disabled because using the ProviderId
//   getter/setter causes crashes" comment. We mirror that omission.
//   DisplayNameResource and IconResource are likewise plain strings here
//   (a literal display name and a path to the running .exe) - the
//   `@module,-resourceid` MS-resource-string format the docs insist on
//   pushed our previous attempts into a separate failure mode.

#define CERNBOX_CF_EXPORTS
#include "cernbox-cf.h"

#include <appmodel.h>          // GetCurrentPackageFamilyName
#include <stdio.h>
#include <winrt/base.h>
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Storage.h>
#include <winrt/Windows.Storage.Provider.h>
#include <winrt/Windows.Storage.Streams.h>  // DataWriter / IBuffer for Context

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
    const GUID    * /*providerId*/,
    const wchar_t *iconResource) {

    trace(L"register_sync_root: enter");
    // providerId is intentionally ignored. ownCloud's working code documents
    // that the ProviderId setter crashes on recent Windows builds; we match
    // that by never calling info.ProviderId(...). The signature still takes
    // it so future Windows builds (or Microsoft fixing the API) can re-enable
    // it without churning the Go-side ABI.
    if (!id || !localRoot || !displayName || !providerVersion) {
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

        // Plain string for DisplayNameResource - NOT the documented
        // `@module,-resourceid` MS-resource format. ownCloud confirmed
        // by experiment that the literal-string form works on Win10/11
        // and the resource-ref form is brittle. The OS uses this for
        // the SyncRootManager registry entry; the directory's own name
        // is what users see in Explorer, so a plain product-name string
        // is fine here.
        info.DisplayNameResource(hstring(displayName));

        // IconResource: plain path to an icon-bearing file (e.g. our shim
        // DLL or the daemon .exe). Empty -> use a sensible default. Same
        // rationale as DisplayNameResource above.
        if (iconResource && iconResource[0]) {
            info.IconResource(hstring(iconResource));
        } else {
            info.IconResource(hstring(L"%SystemRoot%\\System32\\imageres.dll"));
        }

        // Hydration / Population / InSync policies. Mirrors the working
        // ownCloud config: Full + ValidationRequired|AutoDehydrationAllowed,
        // PopulationPolicy::AlwaysFull, InSyncPolicy::FileLastWriteTime.
        // Earlier attempts with PopulationPolicy::Full + multiple InSync
        // bits got rejected with E_FAIL on Win11 24H2+; this combination
        // is what's known to register cleanly there.
        info.HydrationPolicy(StorageProviderHydrationPolicy::Full);
        info.HydrationPolicyModifier(
            StorageProviderHydrationPolicyModifier::ValidationRequired |
            StorageProviderHydrationPolicyModifier::AutoDehydrationAllowed);
        info.PopulationPolicy(StorageProviderPopulationPolicy::AlwaysFull);
        info.InSyncPolicy(StorageProviderInSyncPolicy::FileLastWriteTime);

        info.HardlinkPolicy(StorageProviderHardlinkPolicy::None);
        info.Version(hstring(providerVersion));

        // ShowSiblingsAsGroup(true) lets Explorer collapse multiple folders
        // registered by the same provider into one group entry. ownCloud
        // sets this to true; we match.
        info.ShowSiblingsAsGroup(true);

        // ProviderId: NOT set. See note at function head and the matching
        // comment in ownCloud's vfs_win.cpp.

        // ProtectionMode + AllowPinning are documented as optional and
        // ProtectionMode::Unknown is the documented default; setting
        // AllowPinning(true) makes Pin/Unpin available to the user. Both
        // are safe to call (unlike ProviderId / RecycleBinUri).
        info.ProtectionMode(StorageProviderProtectionMode::Unknown);
        info.AllowPinning(true);

        // RecycleBinUri: NOT set. Same crash class as ProviderId per
        // ownCloud's testing.

        // Context: opaque IBuffer the OS hands back to us on certain
        // callbacks. ownCloud writes the provider name (UTF-16) here. The
        // OS treats it as opaque, so any non-empty content satisfies the
        // validation; the textual content matters only if our callback
        // code ever cares to read it back.
        Streams::DataWriter ctxWriter;
        ctxWriter.WriteString(hstring(L"cernbox-sync"));
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
