/*
 * winrt_register_windows.cpp — register sync roots via the WinRT
 * Windows.Storage.Provider.StorageProviderSyncRootManager API.
 *
 * The legacy CfRegisterSyncRoot still exists in cldapi.dll but
 * CfConnectSyncRoot rejects sync roots that weren't laid down through
 * this WinRT entry point with an access violation. Going through the
 * modern API also installs the shell metadata that makes the folder
 * show up correctly in Explorer.
 *
 * MinGW's WinRT headers (windows.foundation.h, windows.storage.h) are
 * incomplete and have known duplicate-definition bugs, so we declare the
 * interfaces we need by hand from the published IDL. The IIDs come from
 * runtime IInspectable.GetIids probing and the method order matches the
 * IDL's declaration order (verified against cldapi.dll's actual vtable).
 */

#define _WIN32_WINNT 0x0A00
#define WIN32_LEAN_AND_MEAN

#include <windows.h>
#include <objbase.h>
#include <roapi.h>
#include <winstring.h>
#include <inspectable.h>
#include <stdint.h>

extern "C" {
#include "cfapi.h"
}

/* ── Manually declared WinRT interfaces ───────────────────────────────── */

typedef int CernboxAsyncStatus;
const CernboxAsyncStatus CernboxAsyncStarted   = 0;
const CernboxAsyncStatus CernboxAsyncCompleted = 1;

/* IAsyncInfo. */
struct DECLSPEC_UUID("00000036-0000-0000-C000-000000000046")
ICernboxAsyncInfo : public IInspectable {
    virtual HRESULT STDMETHODCALLTYPE get_Id(unsigned int*) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Status(CernboxAsyncStatus*) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_ErrorCode(HRESULT*) = 0;
    virtual HRESULT STDMETHODCALLTYPE Cancel() = 0;
    virtual HRESULT STDMETHODCALLTYPE Close() = 0;
};

/* Forward decl of IStorageFolder. We never call its methods directly —
 * we only need a typed pointer to pass into put_Path, so an opaque
 * inheritance from IInspectable is enough. */
struct ICernboxStorageFolder : public IInspectable {};

/* IStorageFolderStatics — IID discovered at runtime. */
struct DECLSPEC_UUID("08F327FF-85D5-48B9-AEE9-28511E339F9F")
ICernboxStorageFolderStatics : public IInspectable {
    virtual HRESULT STDMETHODCALLTYPE GetFolderFromPathAsync(
        HSTRING path, IInspectable** operation) = 0;
};

/* IAsyncOperation<StorageFolder> — parameterised IID, runtime-discovered.
 * GetResults returns the StorageFolder. */
struct DECLSPEC_UUID("6BE9E7D7-E83A-5CBC-802C-1768960B52C3")
ICernboxAsyncOpStorageFolder : public IInspectable {
    virtual HRESULT STDMETHODCALLTYPE put_Completed(IInspectable*) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Completed(IInspectable**) = 0;
    virtual HRESULT STDMETHODCALLTYPE GetResults(ICernboxStorageFolder**) = 0;
};

/* IStorageProviderSyncRootInfo. The first two property pairs are
 * Get-then-Put; from DisplayNameResource onward the IDL flips to
 * Put-then-Get (verified empirically against the real vtable). */
struct DECLSPEC_UUID("7C1305C4-99F9-41AC-8904-AB055D654926")
ICernboxStorageProviderSyncRootInfo : public IInspectable {
    virtual HRESULT STDMETHODCALLTYPE get_Id(HSTRING*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_Id(HSTRING) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Path(ICernboxStorageFolder**) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_Path(ICernboxStorageFolder*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_DisplayNameResource(HSTRING) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_DisplayNameResource(HSTRING*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_IconResource(HSTRING) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_IconResource(HSTRING*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_HydrationPolicy(int) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_HydrationPolicy(int*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_HydrationPolicyModifier(int) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_HydrationPolicyModifier(int*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_PopulationPolicy(int) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_PopulationPolicy(int*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_InSyncPolicy(int) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_InSyncPolicy(int*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_HardlinkPolicy(int) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_HardlinkPolicy(int*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_Version(HSTRING) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Version(HSTRING*) = 0;
    virtual HRESULT STDMETHODCALLTYPE put_ShowSiblingsAsGroup(unsigned char) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_ShowSiblingsAsGroup(unsigned char*) = 0;
};

/* IStorageProviderSyncRootManagerStatics — the second statics IID exposed
 * by the SyncRootManager activation factory. The first one (3E99FBBF...)
 * fails Register with REGDB_E_CLASSNOTREG; .NET's WinRT projection picks
 * this newer variant which actually works. */
struct DECLSPEC_UUID("EFB6CFEE-1374-544E-9DF1-5598D2E9CFDD")
ICernboxStorageProviderSyncRootManagerStatics : public IInspectable {
    virtual HRESULT STDMETHODCALLTYPE Register(
        ICernboxStorageProviderSyncRootInfo*) = 0;
    virtual HRESULT STDMETHODCALLTYPE Unregister(HSTRING) = 0;
};

/* MinGW's DECLSPEC_UUID(...) doesn't generate the __uuidof() template
 * specialisation; __CRT_UUID_DECL does. Both are needed so g++ can
 * resolve __uuidof(T) for our hand-rolled interfaces. */
__CRT_UUID_DECL(ICernboxAsyncInfo,                            0x00000036, 0x0000, 0x0000, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46)
__CRT_UUID_DECL(ICernboxStorageFolderStatics,                 0x08F327FF, 0x85D5, 0x48B9, 0xAE, 0xE9, 0x28, 0x51, 0x1E, 0x33, 0x9F, 0x9F)
__CRT_UUID_DECL(ICernboxAsyncOpStorageFolder,                 0x6BE9E7D7, 0xE83A, 0x5CBC, 0x80, 0x2C, 0x17, 0x68, 0x96, 0x0B, 0x52, 0xC3)
__CRT_UUID_DECL(ICernboxStorageProviderSyncRootInfo,          0x7C1305C4, 0x99F9, 0x41AC, 0x89, 0x04, 0xAB, 0x05, 0x5D, 0x65, 0x49, 0x26)
__CRT_UUID_DECL(ICernboxStorageProviderSyncRootManagerStatics, 0xEFB6CFEE, 0x1374, 0x544E, 0x9D, 0xF1, 0x55, 0x98, 0xD2, 0xE9, 0xCF, 0xDD)

/* ── Helpers ───────────────────────────────────────────────────────────── */

namespace {

struct HString {
    HSTRING h = nullptr;
    HString() = default;
    HString(const wchar_t* s, UINT32 len) {
        WindowsCreateString(s, len, &h);
    }
    ~HString() { if (h) WindowsDeleteString(h); }
    HString(const HString&) = delete;
    HString& operator=(const HString&) = delete;
};

/* Build an HSTRING from a UTF-8 input. Caller must WindowsDeleteString. */
HRESULT MakeHStringFromUtf8(const char* utf8, HSTRING* out) {
    if (!utf8) {
        return WindowsCreateString(nullptr, 0, out);
    }
    int wlen = MultiByteToWideChar(CP_UTF8, 0, utf8, -1, nullptr, 0);
    if (wlen <= 0) return E_INVALIDARG;
    wchar_t* wbuf = (wchar_t*)_alloca(wlen * sizeof(wchar_t));
    MultiByteToWideChar(CP_UTF8, 0, utf8, -1, wbuf, wlen);
    /* WindowsCreateString takes the length in code units WITHOUT the null. */
    return WindowsCreateString(wbuf, (UINT32)(wlen - 1), out);
}

/* Spin-wait for an IAsyncOperation completion. */
HRESULT WaitAsync(IInspectable* op, DWORD timeout_ms = 30000) {
    ICernboxAsyncInfo* info = nullptr;
    HRESULT hr = op->QueryInterface(__uuidof(ICernboxAsyncInfo),
                                    reinterpret_cast<void**>(&info));
    if (FAILED(hr)) return hr;
    DWORD start = GetTickCount();
    for (;;) {
        CernboxAsyncStatus status;
        hr = info->get_Status(&status);
        if (FAILED(hr)) { info->Release(); return hr; }
        if (status == CernboxAsyncCompleted) {
            info->Release();
            return S_OK;
        }
        if (status != CernboxAsyncStarted) {
            HRESULT err = E_FAIL;
            info->get_ErrorCode(&err);
            info->Release();
            return FAILED(err) ? err : E_FAIL;
        }
        if (GetTickCount() - start > timeout_ms) {
            info->Release();
            return HRESULT_FROM_WIN32(ERROR_TIMEOUT);
        }
        Sleep(20);
    }
}

/* Get an IStorageFolder for a UTF-8 path. */
HRESULT GetStorageFolder(const char* utf8_path, ICernboxStorageFolder** out) {
    HString className(L"Windows.Storage.StorageFolder", 28);
    if (!className.h) return E_OUTOFMEMORY;

    /* Activation factory — IInspectable + QI to avoid IID drift across builds. */
    IInspectable* insp = nullptr;
    HRESULT hr = RoGetActivationFactory(className.h, __uuidof(IInspectable),
                                        reinterpret_cast<void**>(&insp));
    if (FAILED(hr)) return hr;
    ICernboxStorageFolderStatics* statics = nullptr;
    hr = insp->QueryInterface(__uuidof(ICernboxStorageFolderStatics),
                              reinterpret_cast<void**>(&statics));
    insp->Release();
    if (FAILED(hr)) return hr;

    HSTRING hsPath = nullptr;
    hr = MakeHStringFromUtf8(utf8_path, &hsPath);
    if (FAILED(hr)) { statics->Release(); return hr; }

    IInspectable* op = nullptr;
    hr = statics->GetFolderFromPathAsync(hsPath, &op);
    WindowsDeleteString(hsPath);
    statics->Release();
    if (FAILED(hr)) return hr;

    hr = WaitAsync(op);
    if (FAILED(hr)) { op->Release(); return hr; }

    /* The GetResults call lives on IAsyncOperation<StorageFolder>. */
    ICernboxAsyncOpStorageFolder* asyncOp = nullptr;
    hr = op->QueryInterface(__uuidof(ICernboxAsyncOpStorageFolder),
                            reinterpret_cast<void**>(&asyncOp));
    op->Release();
    if (FAILED(hr)) return hr;
    hr = asyncOp->GetResults(out);
    asyncOp->Release();
    return hr;
}

/* Get the SyncRootManager statics. */
HRESULT GetManagerStatics(ICernboxStorageProviderSyncRootManagerStatics** out) {
    HString name(L"Windows.Storage.Provider.StorageProviderSyncRootManager", 55);
    IInspectable* insp = nullptr;
    HRESULT hr = RoGetActivationFactory(name.h, __uuidof(IInspectable),
                                        reinterpret_cast<void**>(&insp));
    if (FAILED(hr)) return hr;
    hr = insp->QueryInterface(
        __uuidof(ICernboxStorageProviderSyncRootManagerStatics),
        reinterpret_cast<void**>(out));
    insp->Release();
    return hr;
}

} // namespace

/* ── exported wrappers ────────────────────────────────────────────────── */

extern "C" int32_t cf_winrt_register_sync_root(
    const char* utf8_path,
    const char* utf8_provider_id,
    const char* utf8_display_name) {

    HRESULT hr = RoInitialize(RO_INIT_SINGLETHREADED);
    if (FAILED(hr) && hr != RPC_E_CHANGED_MODE && hr != S_FALSE) {
        return (int32_t)hr;
    }

    ICernboxStorageFolder* folder = nullptr;
    hr = GetStorageFolder(utf8_path, &folder);
    if (FAILED(hr)) return (int32_t)hr;

    /* Activate StorageProviderSyncRootInfo. */
    HString infoClass(L"Windows.Storage.Provider.StorageProviderSyncRootInfo", 52);
    IInspectable* infoInsp = nullptr;
    hr = RoActivateInstance(infoClass.h, &infoInsp);
    if (FAILED(hr)) { folder->Release(); return (int32_t)hr; }

    ICernboxStorageProviderSyncRootInfo* info = nullptr;
    hr = infoInsp->QueryInterface(
        __uuidof(ICernboxStorageProviderSyncRootInfo),
        reinterpret_cast<void**>(&info));
    infoInsp->Release();
    if (FAILED(hr)) { folder->Release(); return (int32_t)hr; }

    HSTRING hsID = nullptr, hsName = nullptr, hsIcon = nullptr, hsVer = nullptr;
    do {
        if (FAILED(hr = MakeHStringFromUtf8(utf8_provider_id, &hsID))) break;
        if (FAILED(hr = info->put_Id(hsID))) break;
        if (FAILED(hr = info->put_Path(folder))) break;

        if (FAILED(hr = MakeHStringFromUtf8(utf8_display_name, &hsName))) break;
        if (FAILED(hr = info->put_DisplayNameResource(hsName))) break;

        WindowsCreateString(L"%SystemRoot%\\system32\\imageres.dll,-1043", 39, &hsIcon);
        if (FAILED(hr = info->put_IconResource(hsIcon))) break;

        /* Hydration: Partial(1), Population: AlwaysFull(2),
         * InSync: TrackAllChanges(0xFFFFFF), Hardlink: None(0). */
        if (FAILED(hr = info->put_HydrationPolicy(1))) break;
        if (FAILED(hr = info->put_PopulationPolicy(2))) break;
        if (FAILED(hr = info->put_InSyncPolicy(0xFFFFFF))) break;
        if (FAILED(hr = info->put_HardlinkPolicy(0))) break;

        WindowsCreateString(L"1.0", 3, &hsVer);
        if (FAILED(hr = info->put_Version(hsVer))) break;

        ICernboxStorageProviderSyncRootManagerStatics* mgr = nullptr;
        if (FAILED(hr = GetManagerStatics(&mgr))) break;
        hr = mgr->Register(info);
        mgr->Release();
        if (hr == HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS)) hr = S_OK;
    } while (false);

    if (hsID)   WindowsDeleteString(hsID);
    if (hsName) WindowsDeleteString(hsName);
    if (hsIcon) WindowsDeleteString(hsIcon);
    if (hsVer)  WindowsDeleteString(hsVer);
    info->Release();
    folder->Release();
    return (int32_t)hr;
}

extern "C" int32_t cf_winrt_unregister_sync_root(const char* utf8_provider_id) {
    HRESULT hr = RoInitialize(RO_INIT_SINGLETHREADED);
    if (FAILED(hr) && hr != RPC_E_CHANGED_MODE && hr != S_FALSE) {
        return (int32_t)hr;
    }

    ICernboxStorageProviderSyncRootManagerStatics* mgr = nullptr;
    hr = GetManagerStatics(&mgr);
    if (FAILED(hr)) return (int32_t)hr;

    HSTRING hsID = nullptr;
    hr = MakeHStringFromUtf8(utf8_provider_id, &hsID);
    if (SUCCEEDED(hr)) {
        hr = mgr->Unregister(hsID);
        WindowsDeleteString(hsID);
    }
    mgr->Release();
    return (int32_t)hr;
}
