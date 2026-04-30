/*
 * cfapi_windows.c — Cloud Files API wrappers using the official SDK types.
 *
 * All CF_* types come from <cfapi.h> (Windows SDK), included via cfwrap.h.
 * Function symbols are resolved at runtime via LoadLibrary/GetProcAddress
 * against cldapi.dll, which ships with every Windows 10 1709+ install.
 *
 * CfConnectSyncRoot and CfExecute are NOT wrapped here: both use SEH
 * internally in cldapi.dll and crash when called via CGo (Go's VEH
 * intercepts the exception before the SEH handler runs). Those two calls
 * go through the LazyProc / syscall.SyscallN path in cfapi_syscall_windows.go.
 */

#define WIN32_LEAN_AND_MEAN

#include <windows.h>
#include <objbase.h>
#include <stdint.h>
#include <stdlib.h>

#include "cfwrap.h"   /* forward-declares CORRELATION_VECTOR, includes <cfapi.h> */
#include "_cgo_export.h"

/* ── Function pointer types ──────────────────────────────────────────────── */

typedef HRESULT (WINAPI *PFN_CfDisconnectSyncRoot)(CF_CONNECTION_KEY);

typedef HRESULT (WINAPI *PFN_CfUpdateSyncProviderStatus)(
    CF_CONNECTION_KEY, CF_SYNC_PROVIDER_STATUS);

typedef HRESULT (WINAPI *PFN_CfCreatePlaceholders)(
    PCWSTR, CF_PLACEHOLDER_CREATE_INFO *, DWORD, CF_CREATE_FLAGS, PDWORD);

typedef HRESULT (WINAPI *PFN_CfUpdatePlaceholder)(
    HANDLE, const CF_FS_METADATA *,
    LPCVOID, DWORD,
    const CF_FILE_RANGE *, DWORD,
    CF_UPDATE_FLAGS, USN *, LPOVERLAPPED);

typedef HRESULT (WINAPI *PFN_CfSetPinState)(
    HANDLE, CF_PIN_STATE, CF_SET_PIN_FLAGS, LPOVERLAPPED);

static PFN_CfDisconnectSyncRoot p_CfDisconnectSyncRoot = NULL;
static PFN_CfUpdateSyncProviderStatus p_CfUpdateSyncProviderStatus = NULL;
static PFN_CfCreatePlaceholders p_CfCreatePlaceholders = NULL;
static PFN_CfUpdatePlaceholder  p_CfUpdatePlaceholder  = NULL;
static PFN_CfSetPinState        p_CfSetPinState        = NULL;

/* Lazy-load cldapi.dll on first use. Returns 0 on success, an HRESULT on
 * failure. Subsequent calls are O(1).
 *
 * Sync-root REGISTRATION (CfRegisterSyncRoot / CfUnregisterSyncRoot) was
 * dropped along with its cldapi.dll function pointers - registration now
 * goes through the WinRT path (StorageProviderSyncRootManager.Register,
 * via cernbox-cf.dll). The remaining cldapi.dll surface here is just the
 * placeholder + connection-status operations that have no WinRT
 * equivalent. */
static int32_t load_cldapi(void) {
    static int loaded = 0; /* 0=not tried, 1=ok, -1=failed */
    if (loaded == 1) return 0;
    if (loaded == -1) return E_NOINTERFACE;

    HMODULE h = LoadLibraryW(L"cldapi.dll");
    if (!h) {
        loaded = -1;
        return HRESULT_FROM_WIN32(GetLastError());
    }
    p_CfDisconnectSyncRoot = (PFN_CfDisconnectSyncRoot)(void *)GetProcAddress(h, "CfDisconnectSyncRoot");
    p_CfUpdateSyncProviderStatus = (PFN_CfUpdateSyncProviderStatus)(void *)GetProcAddress(h, "CfUpdateSyncProviderStatus");
    p_CfCreatePlaceholders = (PFN_CfCreatePlaceholders)(void *)GetProcAddress(h, "CfCreatePlaceholders");
    p_CfUpdatePlaceholder  = (PFN_CfUpdatePlaceholder) (void *)GetProcAddress(h, "CfUpdatePlaceholder");
    p_CfSetPinState        = (PFN_CfSetPinState)       (void *)GetProcAddress(h, "CfSetPinState");
    if (!p_CfDisconnectSyncRoot || !p_CfUpdateSyncProviderStatus ||
        !p_CfCreatePlaceholders || !p_CfUpdatePlaceholder ||
        !p_CfSetPinState) {
        loaded = -1;
        return E_NOTIMPL;
    }
    loaded = 1;
    return 0;
}

/* -- helpers --------------------------------------------------------------- */

/* Convert a UTF-8 string to a freshly-allocated wide string. The caller
 * must free the returned pointer. Returns NULL on conversion failure. */
static wchar_t *utf8_to_wide(const char *s) {
    if (!s) return NULL;
    int len = MultiByteToWideChar(CP_UTF8, 0, s, -1, NULL, 0);
    if (len <= 0) return NULL;
    wchar_t *w = (wchar_t *)calloc((size_t)len, sizeof(wchar_t));
    if (!w) return NULL;
    if (MultiByteToWideChar(CP_UTF8, 0, s, -1, w, len) <= 0) {
        free(w);
        return NULL;
    }
    return w;
}

/* -- exported wrappers ----------------------------------------------------- */

int32_t cf_disconnect_sync_root(int64_t connection_key) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    CF_CONNECTION_KEY key;
    key.Internal = (LONGLONG)connection_key;
    HRESULT hr = p_CfDisconnectSyncRoot(key);
    return (int32_t)hr;
}

int32_t cf_update_provider_status_idle(int64_t connection_key) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    CF_CONNECTION_KEY key;
    key.Internal = (LONGLONG)connection_key;
    HRESULT hr = p_CfUpdateSyncProviderStatus(key, CF_PROVIDER_STATUS_IDLE);
    return (int32_t)hr;
}

/* fill_metadata sets the timestamp and size fields. All four file timestamps
 * share mtime_ft for simplicity. */
static void fill_metadata(CF_FS_METADATA *md, int64_t size, int64_t mtime_ft) {
    md->FileSize.QuadPart                   = size;
    md->BasicInfo.CreationTime.QuadPart     = mtime_ft;
    md->BasicInfo.LastAccessTime.QuadPart   = mtime_ft;
    md->BasicInfo.LastWriteTime.QuadPart    = mtime_ft;
    md->BasicInfo.ChangeTime.QuadPart       = mtime_ft;
    md->BasicInfo.FileAttributes            = FILE_ATTRIBUTE_NORMAL;
}

int32_t cf_create_placeholder(
    const char *utf8_abs_path,
    int64_t     size,
    int64_t     mtime_filetime,
    const void *file_identity,
    int32_t     file_identity_len) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    wchar_t *path = utf8_to_wide(utf8_abs_path);
    if (!path) return E_OUTOFMEMORY;

    /* Split path → base directory + relative file name. */
    wchar_t *last_sep = wcsrchr(path, L'\\');
    wchar_t *fwd_sep  = wcsrchr(path, L'/');
    if (!last_sep || (fwd_sep && fwd_sep > last_sep)) last_sep = fwd_sep;
    if (!last_sep || last_sep == path) {
        free(path);
        return E_INVALIDARG;
    }
    *last_sep = L'\0';
    wchar_t *rel_name = last_sep + 1;

    CF_PLACEHOLDER_CREATE_INFO info = {0};
    info.RelativeFileName   = rel_name;
    info.FileIdentity       = file_identity;
    info.FileIdentityLength = (DWORD)file_identity_len;
    info.Flags              = CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC;
    fill_metadata(&info.FsMetadata, size, mtime_filetime);

    DWORD processed = 0;
    HRESULT hr = p_CfCreatePlaceholders(path, &info, 1,
                                        CF_CREATE_FLAG_NONE, &processed);
    free(path);
    if (FAILED(hr)) return (int32_t)hr;
    return (int32_t)info.Result;
}

int32_t cf_update_placeholder(
    const char *utf8_abs_path,
    int64_t     size,
    int64_t     mtime_filetime,
    const void *file_identity,
    int32_t     file_identity_len) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    wchar_t *path = utf8_to_wide(utf8_abs_path);
    if (!path) return E_OUTOFMEMORY;

    /* Open the placeholder for metadata-only update. CfUpdatePlaceholder
     * requires at least FILE_WRITE_ATTRIBUTES; using FILE_GENERIC_READ would
     * include FILE_READ_DATA which triggers FETCH_DATA on a dehydrated
     * placeholder — unnecessary and potentially disruptive here. */
    HANDLE hFile = CreateFileW(
        path,
        FILE_READ_ATTRIBUTES | FILE_WRITE_ATTRIBUTES,
        FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
        NULL,
        OPEN_EXISTING,
        FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS,
        NULL);
    free(path);
    if (hFile == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());

    CF_FS_METADATA md = {0};
    fill_metadata(&md, size, mtime_filetime);

    USN usn = 0;
    /* MARK_IN_SYNC only: DEHYDRATE fails with 0x8007018B on a placeholder
     * that was never hydrated. The OS still invalidates any cached content
     * on the next access since the metadata has changed. */
    HRESULT hr = p_CfUpdatePlaceholder(
        hFile, &md,
        file_identity, (DWORD)file_identity_len,
        NULL, 0,
        CF_UPDATE_FLAG_MARK_IN_SYNC,
        &usn, NULL);
    CloseHandle(hFile);
    return (int32_t)hr;
}

int32_t cf_set_pin_state(const char *utf8_abs_path, int32_t pinned) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    wchar_t *path = utf8_to_wide(utf8_abs_path);
    if (!path) return E_OUTOFMEMORY;

    HANDLE hFile = CreateFileW(
        path,
        FILE_READ_ATTRIBUTES | FILE_WRITE_ATTRIBUTES,
        FILE_SHARE_READ | FILE_SHARE_WRITE,
        NULL,
        OPEN_EXISTING,
        FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS,
        NULL);
    free(path);
    if (hFile == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());

    CF_PIN_STATE state = pinned ? CF_PIN_STATE_PINNED : CF_PIN_STATE_UNPINNED;
    HRESULT hr = p_CfSetPinState(hFile, state, CF_SET_PIN_FLAG_NONE, NULL);
    CloseHandle(hFile);
    return (int32_t)hr;
}

/* ── FETCH_DATA callback bridge ─────────────────────────────────────────── */

/* on_fetch_data is wired into the CF_CALLBACK_REGISTRATION table for
 * CF_CALLBACK_TYPE_FETCH_DATA. The OS invokes it on a thread-pool thread
 * each time a placeholder is read for the first time. We pull the few
 * fields Go needs and call goFetchData; the heavy lifting (download +
 * CfExecute via syscall) happens in a Go worker so the callback returns fast. */
static VOID CALLBACK on_fetch_data(
    const CF_CALLBACK_INFO       *info,
    const CF_CALLBACK_PARAMETERS *params) {
    goFetchData(
        info->ConnectionKey.Internal,
        info->TransferKey.QuadPart,
        (void *)info->FileIdentity,
        (int32_t)info->FileIdentityLength,
        params->FetchData.RequiredFileOffset.QuadPart,
        params->FetchData.RequiredLength.QuadPart);
}

/* Exported accessor so the Go side can hand the callback pointer directly
 * to CfConnectSyncRoot via syscall, without going through the cgo bridge
 * (whose VEH integration crashes inside cldapi.dll's internal SEH path). */
void *cf_get_fetch_data_callback(void) {
    return (void *)on_fetch_data;
}

/* ── FETCH_PLACEHOLDERS callback bridge ─────────────────────────────────────
 *
 * Even though we register sync roots with CF_POPULATION_POLICY_FULL — meaning
 * we tell Windows we've populated every placeholder ahead of time — Windows
 * has been observed calling FETCH_PLACEHOLDERS on the sync root the first
 * time Explorer opens it. If no handler is registered, the OS waits the full
 * timeout and surfaces "cloud operation not completed" to the user.
 *
 * This stub responds with TRANSFER_PLACEHOLDERS / no entries / SUCCESS /
 * STOP_ON_DEMAND_POPULATION so Windows is satisfied and won't ask again. */
static VOID CALLBACK on_fetch_placeholders(
    const CF_CALLBACK_INFO       *info,
    const CF_CALLBACK_PARAMETERS *params) {
    (void)params;
    goFetchPlaceholders(
        info->ConnectionKey.Internal,
        info->TransferKey.QuadPart);
}

void *cf_get_fetch_placeholders_callback(void) {
    return (void *)on_fetch_placeholders;
}
