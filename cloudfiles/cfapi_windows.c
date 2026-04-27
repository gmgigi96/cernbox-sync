/*
 * cfapi_windows.c — Cloud Files API wrappers.
 *
 * MinGW's <cfapi.h> ranges from incomplete to absent depending on the
 * distribution. Rather than rely on it, we declare the subset of the
 * Cloud Files ABI we need (types, enums, functions) ourselves with the
 * cernbox_ prefix. The function symbols are resolved at link time
 * against cldapi.dll, which ships with every Windows 10 1709+ install.
 *
 * Phase 4: register / unregister / connect / disconnect only. The
 * connect path uses an empty callback table (just the terminator),
 * which is enough to validate that cldapi.dll is reachable and the
 * registration handshake works end to end.
 */

#define WIN32_LEAN_AND_MEAN

#include <windows.h>
#include <objbase.h>   /* CoInitializeEx, COINIT_MULTITHREADED */
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#include "cfapi.h"

/* ── Cloud Files ABI ─────────────────────────────────────────────────────── */

/* CF_HYDRATION_POLICY_PRIMARY values; we only need PARTIAL. */
#define CERNBOX_CF_HYDRATION_POLICY_PARTIAL 0
#define CERNBOX_CF_POPULATION_POLICY_FULL   2
#define CERNBOX_CF_INSYNC_POLICY_TRACK_ALL  0x000FFF00
#define CERNBOX_CF_HARDLINK_POLICY_NONE     0
#define CERNBOX_CF_CALLBACK_TYPE_NONE       65535

typedef struct {
    USHORT Primary;     /* CF_HYDRATION_POLICY_PRIMARY    */
    USHORT Modifier;    /* CF_HYDRATION_POLICY_MODIFIER   */
} CernboxHydrationPolicy;

typedef struct {
    USHORT Primary;     /* CF_POPULATION_POLICY_PRIMARY    */
    USHORT Modifier;    /* CF_POPULATION_POLICY_MODIFIER   */
} CernboxPopulationPolicy;

typedef struct {
    DWORD                   StructSize;
    CernboxHydrationPolicy  Hydration;
    CernboxPopulationPolicy Population;
    ULONG                   InSync;     /* CF_INSYNC_POLICY        */
    ULONG                   HardLink;   /* CF_HARDLINK_POLICY      */
} CernboxCfSyncPolicies;

typedef struct {
    DWORD  StructSize;
    PCWSTR ProviderName;
    PCWSTR ProviderVersion;
    LPVOID SyncRootIdentity;
    DWORD  SyncRootIdentityLength;
    LPVOID FileIdentity;
    DWORD  FileIdentityLength;
    GUID   ProviderId;
} CernboxCfSyncRegistration;

typedef struct {
    int   Type;       /* CF_CALLBACK_TYPE; integer for ABI parity */
    void *Callback;   /* CF_CALLBACK function pointer             */
} CernboxCfCallbackRegistration;

typedef struct {
    LONG_PTR Internal;
} CernboxCfConnectionKey;

/* Placeholder metadata. Mirrors CF_FS_METADATA + FILE_BASIC_INFO from
 * the SDK; integer-typed for ABI parity (FILETIME is 8 bytes, LARGE_INTEGER
 * is 8 bytes). */
typedef struct {
    int64_t CreationTime;
    int64_t LastAccessTime;
    int64_t LastWriteTime;
    int64_t ChangeTime;
    DWORD   FileAttributes;
} CernboxFileBasicInfo;

typedef struct {
    CernboxFileBasicInfo BasicInfo;
    int64_t              FileSize;
} CernboxCfFsMetadata;

/* CF_PLACEHOLDER_CREATE_INFO mirror. Result is filled in by the OS after
 * CfCreatePlaceholders so we can read per-file errors. */
typedef struct {
    LPCWSTR             RelativeFileName;
    CernboxCfFsMetadata FsMetadata;
    LPCVOID             FileIdentity;
    DWORD               FileIdentityLength;
    DWORD               Flags;       /* CF_PLACEHOLDER_CREATE_FLAGS */
    HRESULT             Result;
    int64_t             CreateUsn;
} CernboxCfPlaceholderCreateInfo;

/* Flags we use. Values come straight from the CF_PLACEHOLDER_CREATE_FLAGS
 * and CF_UPDATE_FLAGS enums in the SDK so the bit positions match the ABI
 * cldapi.dll expects. */
#define CERNBOX_CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC 0x2
#define CERNBOX_CF_UPDATE_FLAG_MARK_IN_SYNC             0x2
#define CERNBOX_CF_UPDATE_FLAG_DEHYDRATE                0x4

/* CF_CALLBACK_TYPE values. */
#define CERNBOX_CF_CALLBACK_TYPE_FETCH_DATA 0

/* CF_OPERATION_TYPE values. */
#define CERNBOX_CF_OPERATION_TYPE_TRANSFER_DATA 0

/* CF_CALLBACK_INFO mirror; only the fields we use are typed precisely. The
 * trailing fields we don't need are sized as opaque LARGE_INTEGER / DWORDs
 * so the struct keeps the right layout for the OS. */
typedef struct {
    DWORD              StructSize;
    LONG_PTR           ConnectionKey;     /* CF_CONNECTION_KEY.Internal */
    LPVOID             CallbackContext;
    LPCWSTR            VolumeGuidName;
    LPCWSTR            VolumeDosName;
    DWORD              VolumeSerialNumber;
    int64_t            SyncRootFileId;
    LPCWSTR            SyncRootIdentity;
    DWORD              SyncRootIdentityLength;
    int64_t            FileId;
    int64_t            FileSize;
    LPCVOID            FileIdentity;
    DWORD              FileIdentityLength;
    LPCWSTR            NormalizedPath;
    int64_t            TransferKey;
    UCHAR              PriorityHint;
    LPVOID             CorrelationVector;
    int64_t            ProcessInfo;
    int64_t            RequestKey;
} CernboxCfCallbackInfo;

/* Subset of CF_CALLBACK_PARAMETERS we read: only the FetchData branch.
 * The struct is union-shaped on the SDK side; we keep enough room. */
typedef struct {
    DWORD    Flags;
    int64_t  RequiredFileOffset;
    int64_t  RequiredLength;
    int64_t  OptionalFileOffset;
    int64_t  OptionalLength;
    int64_t  LastDehydrationTime;
    DWORD    LastDehydrationReason;
    /* pad so the surrounding union is large enough on the SDK side */
    UCHAR    _pad[64];
} CernboxCfCallbackFetchDataParams;

/* CF_OPERATION_INFO mirror for CfExecute. */
typedef struct {
    DWORD     StructSize;
    DWORD     Type;             /* CF_OPERATION_TYPE                 */
    LPCVOID   CorrelationVector;
    int64_t   SyncStatus;
    LONG_PTR  ConnectionKey;
    int64_t   TransferKey;
    DWORD     RequestKey;       /* spec uses bytes; DWORD is enough  */
} CernboxCfOperationInfo;

/* CF_OPERATION_PARAMETERS / TransferData branch. */
typedef struct {
    DWORD    ParamsSize;        /* must be set to sizeof(this struct) */
    DWORD    Flags;
    int32_t  CompletionStatus;  /* NTSTATUS */
    int64_t  FileOffset;
    int64_t  Length;
    LPVOID   Buffer;
} CernboxCfOperationParametersTransferData;

/* CF_CALLBACK function pointer. Matches what cldapi.dll passes to the
 * registration table. */
typedef VOID (CALLBACK *CernboxCfCallback)(
    const CernboxCfCallbackInfo *,
    const void *);  /* opaque CF_CALLBACK_PARAMETERS pointer */

/* Function-pointer typedefs. We resolve these at runtime via LoadLibrary
 * because MinGW does not ship libcldapi.a. */
typedef HRESULT (WINAPI *PFN_CfRegisterSyncRoot)(
    PCWSTR, const CernboxCfSyncRegistration *,
    const CernboxCfSyncPolicies *, DWORD);

typedef HRESULT (WINAPI *PFN_CfUnregisterSyncRoot)(PCWSTR);

typedef HRESULT (WINAPI *PFN_CfConnectSyncRoot)(
    PCWSTR, const CernboxCfCallbackRegistration *,
    LPCVOID, DWORD, CernboxCfConnectionKey *);

typedef HRESULT (WINAPI *PFN_CfDisconnectSyncRoot)(CernboxCfConnectionKey);

typedef HRESULT (WINAPI *PFN_CfCreatePlaceholders)(
    PCWSTR, CernboxCfPlaceholderCreateInfo *, DWORD, DWORD, PDWORD);

typedef HRESULT (WINAPI *PFN_CfUpdatePlaceholder)(
    HANDLE, const CernboxCfFsMetadata *,
    LPCVOID, DWORD,
    void *, DWORD,    /* DehydrateRangeArray, count — unused */
    DWORD,            /* CF_UPDATE_FLAGS */
    int64_t *,        /* USN out */
    LPOVERLAPPED);

typedef HRESULT (WINAPI *PFN_CfExecute)(
    const CernboxCfOperationInfo *,
    void *);          /* CF_OPERATION_PARAMETERS */

typedef HRESULT (WINAPI *PFN_CfSetPinState)(
    HANDLE,           /* FileHandle */
    int,              /* CF_PIN_STATE                       */
    DWORD,            /* CF_SET_PIN_FLAGS                   */
    LPOVERLAPPED);

static PFN_CfRegisterSyncRoot   p_CfRegisterSyncRoot   = NULL;
static PFN_CfUnregisterSyncRoot p_CfUnregisterSyncRoot = NULL;
static PFN_CfConnectSyncRoot    p_CfConnectSyncRoot    = NULL;
static PFN_CfDisconnectSyncRoot p_CfDisconnectSyncRoot = NULL;
static PFN_CfCreatePlaceholders p_CfCreatePlaceholders = NULL;
static PFN_CfUpdatePlaceholder  p_CfUpdatePlaceholder  = NULL;
static PFN_CfExecute            p_CfExecute            = NULL;
static PFN_CfSetPinState        p_CfSetPinState        = NULL;

/* enable_manage_volume_privilege turns on SeManageVolumePrivilege for the
 * current process token. CfConnectSyncRoot needs it; without the privilege
 * cldapi.dll faults internally rather than returning a clean HRESULT. The
 * privilege is in the Administrators group's token by default but isn't
 * enabled — AdjustTokenPrivileges flips the bit. Idempotent. */
static void enable_manage_volume_privilege(void) {
    HANDLE token;
    if (!OpenProcessToken(GetCurrentProcess(),
                          TOKEN_ADJUST_PRIVILEGES | TOKEN_QUERY, &token)) {
        return;
    }
    TOKEN_PRIVILEGES tp = {0};
    tp.PrivilegeCount = 1;
    tp.Privileges[0].Attributes = SE_PRIVILEGE_ENABLED;
    if (LookupPrivilegeValueW(NULL, L"SeManageVolumePrivilege",
                              &tp.Privileges[0].Luid)) {
        AdjustTokenPrivileges(token, FALSE, &tp, sizeof(tp), NULL, NULL);
    }
    CloseHandle(token);
}

/* Lazy-load cldapi.dll on first use. Returns 0 on success, an HRESULT on
 * failure. Subsequent calls are O(1). */
static int32_t load_cldapi(void) {
    static int loaded = 0; /* 0=not tried, 1=ok, -1=failed */
    if (loaded == 1) return 0;
    if (loaded == -1) return E_NOINTERFACE;

    HMODULE h = LoadLibraryW(L"cldapi.dll");
    if (!h) {
        loaded = -1;
        return HRESULT_FROM_WIN32(GetLastError());
    }
    p_CfRegisterSyncRoot   = (PFN_CfRegisterSyncRoot)  (void *)GetProcAddress(h, "CfRegisterSyncRoot");
    p_CfUnregisterSyncRoot = (PFN_CfUnregisterSyncRoot)(void *)GetProcAddress(h, "CfUnregisterSyncRoot");
    p_CfConnectSyncRoot    = (PFN_CfConnectSyncRoot)   (void *)GetProcAddress(h, "CfConnectSyncRoot");
    p_CfDisconnectSyncRoot = (PFN_CfDisconnectSyncRoot)(void *)GetProcAddress(h, "CfDisconnectSyncRoot");
    p_CfCreatePlaceholders = (PFN_CfCreatePlaceholders)(void *)GetProcAddress(h, "CfCreatePlaceholders");
    p_CfUpdatePlaceholder  = (PFN_CfUpdatePlaceholder) (void *)GetProcAddress(h, "CfUpdatePlaceholder");
    p_CfExecute            = (PFN_CfExecute)           (void *)GetProcAddress(h, "CfExecute");
    p_CfSetPinState        = (PFN_CfSetPinState)       (void *)GetProcAddress(h, "CfSetPinState");
    if (!p_CfRegisterSyncRoot || !p_CfUnregisterSyncRoot ||
        !p_CfConnectSyncRoot || !p_CfDisconnectSyncRoot ||
        !p_CfCreatePlaceholders || !p_CfUpdatePlaceholder ||
        !p_CfExecute || !p_CfSetPinState) {
        loaded = -1;
        return E_NOTIMPL;
    }
    loaded = 1;
    return 0;
}

/* ── helpers ─────────────────────────────────────────────────────────────── */

/* Provider GUID for cernbox-sync. Generated once and embedded so all sync
 * roots created by this binary share a single provider identity. */
static const GUID kProviderGuid = {
    0x2b3a4c5d, 0x6e7f, 0x8a9b,
    {0x0c, 0x1d, 0x2e, 0x3f, 0x4a, 0x5b, 0x6c, 0x7d}
};

/* Convert a UTF-8 string to a freshly-allocated wide string. The caller
 * must free the returned pointer. Returns NULL on conversion failure. */
static wchar_t *utf8_to_wide(const char *s) {
    if (!s) {
        return NULL;
    }
    int len = MultiByteToWideChar(CP_UTF8, 0, s, -1, NULL, 0);
    if (len <= 0) {
        return NULL;
    }
    wchar_t *w = (wchar_t *)calloc((size_t)len, sizeof(wchar_t));
    if (!w) {
        return NULL;
    }
    if (MultiByteToWideChar(CP_UTF8, 0, s, -1, w, len) <= 0) {
        free(w);
        return NULL;
    }
    return w;
}

/* ── exported wrappers ───────────────────────────────────────────────────── */

int32_t cf_register_sync_root(const char *utf8_path, const char *utf8_provider_name) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    wchar_t *path = utf8_to_wide(utf8_path);
    wchar_t *provider = utf8_to_wide(utf8_provider_name);
    if (!path || !provider) {
        free(path);
        free(provider);
        return E_OUTOFMEMORY;
    }

    CernboxCfSyncRegistration reg = {0};
    reg.StructSize = sizeof(reg);
    reg.ProviderName = provider;
    reg.ProviderVersion = L"1.0";
    reg.ProviderId = kProviderGuid;

    CernboxCfSyncPolicies policies = {0};
    policies.StructSize = sizeof(policies);
    policies.Hydration.Primary = CERNBOX_CF_HYDRATION_POLICY_PARTIAL;
    policies.Population.Primary = CERNBOX_CF_POPULATION_POLICY_FULL;
    policies.InSync = CERNBOX_CF_INSYNC_POLICY_TRACK_ALL;
    policies.HardLink = CERNBOX_CF_HARDLINK_POLICY_NONE;

    HRESULT hr = p_CfRegisterSyncRoot(path, &reg, &policies, 0 /* CF_REGISTER_FLAG_NONE */);
    free(path);
    free(provider);
    return (int32_t)hr;
}

int32_t cf_unregister_sync_root(const char *utf8_path) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    wchar_t *path = utf8_to_wide(utf8_path);
    if (!path) {
        return E_OUTOFMEMORY;
    }
    HRESULT hr = p_CfUnregisterSyncRoot(path);
    free(path);
    return (int32_t)hr;
}

int32_t cf_disconnect_sync_root(int64_t connection_key) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    CernboxCfConnectionKey key;
    key.Internal = (LONG_PTR)connection_key;
    HRESULT hr = p_CfDisconnectSyncRoot(key);
    return (int32_t)hr;
}

/* fill_metadata sets the timestamp and size fields on a CF_FS_METADATA-style
 * struct. All four file timestamps share mtime_ft for simplicity (we don't
 * track creation/access time separately for remote files). */
static void fill_metadata(CernboxCfFsMetadata *md, int64_t size, int64_t mtime_ft) {
    md->FileSize = size;
    md->BasicInfo.CreationTime   = mtime_ft;
    md->BasicInfo.LastAccessTime = mtime_ft;
    md->BasicInfo.LastWriteTime  = mtime_ft;
    md->BasicInfo.ChangeTime     = mtime_ft;
    md->BasicInfo.FileAttributes = FILE_ATTRIBUTE_NORMAL;
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

    /* Split path → base directory + relative file name. CfCreatePlaceholders
     * only operates on direct children of BaseDirectoryPath. */
    wchar_t *last_sep = wcsrchr(path, L'\\');
    wchar_t *fwd_sep  = wcsrchr(path, L'/');
    if (!last_sep || (fwd_sep && fwd_sep > last_sep)) {
        last_sep = fwd_sep;
    }
    if (!last_sep || last_sep == path) {
        free(path);
        return E_INVALIDARG;
    }
    *last_sep = L'\0';
    wchar_t *rel_name = last_sep + 1;

    CernboxCfPlaceholderCreateInfo info = {0};
    info.RelativeFileName   = rel_name;
    info.FileIdentity       = file_identity;
    info.FileIdentityLength = (DWORD)file_identity_len;
    info.Flags              = CERNBOX_CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC;
    fill_metadata(&info.FsMetadata, size, mtime_filetime);

    DWORD processed = 0;
    HRESULT hr = p_CfCreatePlaceholders(path, &info, 1,
                                        0 /* CF_CREATE_FLAG_NONE */, &processed);
    free(path);
    if (FAILED(hr)) {
        return (int32_t)hr;
    }
    /* The bulk call may succeed at the API level while individual
     * placeholders fail — surface the per-file Result. */
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

    /* Open the placeholder for writing metadata. FILE_FLAG_OPEN_REPARSE_POINT
     * keeps us from triggering hydration just by opening the file. */
    HANDLE hFile = CreateFileW(
        path,
        FILE_GENERIC_WRITE | FILE_GENERIC_READ,
        FILE_SHARE_READ | FILE_SHARE_WRITE,
        NULL,
        OPEN_EXISTING,
        FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS,
        NULL);
    free(path);
    if (hFile == INVALID_HANDLE_VALUE) {
        return HRESULT_FROM_WIN32(GetLastError());
    }

    CernboxCfFsMetadata md = {0};
    fill_metadata(&md, size, mtime_filetime);

    int64_t usn = 0;
    /* MARK_IN_SYNC only: DEHYDRATE fails with 0x8007018B on a placeholder
     * that was never hydrated. The OS still invalidates any cached content
     * on the next access since the metadata (size + mtime) has changed. */
    HRESULT hr = p_CfUpdatePlaceholder(
        hFile, &md,
        file_identity, (DWORD)file_identity_len,
        NULL, 0,
        CERNBOX_CF_UPDATE_FLAG_MARK_IN_SYNC,
        &usn, NULL);
    CloseHandle(hFile);
    return (int32_t)hr;
}

/* ── FETCH_DATA callback bridge ─────────────────────────────────────────── */

/* The Go entry point (//export goFetchData in callback_windows.go) is
 * declared in cgo's auto-generated header. Including it pulls in the exact
 * function signature so we don't drift from the Go side. */
#include "_cgo_export.h"

/* on_fetch_data is wired into the CF_CALLBACK_REGISTRATION table for
 * CF_CALLBACK_TYPE_FETCH_DATA. The OS invokes it on a thread-pool thread
 * each time a placeholder is read for the first time. We pull the few
 * fields Go needs and call goFetchData; the heavy lifting (download +
 * CfExecute) happens in a Go worker so the callback returns fast. */
static VOID CALLBACK on_fetch_data(
    const CernboxCfCallbackInfo *info,
    const void                  *params_in) {
    const CernboxCfCallbackFetchDataParams *params =
        (const CernboxCfCallbackFetchDataParams *)params_in;
    /* cgo's auto-generated signature uses `void *` so we cast away the
     * const here. The Go side does not mutate the buffer. */
    goFetchData(
        (int64_t)info->ConnectionKey,
        info->TransferKey,
        (void *)info->FileIdentity,
        (int32_t)info->FileIdentityLength,
        params->RequiredFileOffset,
        params->RequiredLength);
}

/* Exported accessor so the Go side can hand the callback pointer directly
 * to CfConnectSyncRoot via syscall, without going through the cgo bridge
 * (whose VEH integration crashes inside cldapi.dll's internal SEH path). */
void *cf_get_fetch_data_callback(void) {
    return (void *)on_fetch_data;
}

int32_t cf_execute_transfer(
    int64_t     connection_key,
    int64_t     transfer_key,
    int64_t     offset,
    int64_t     length,
    const void *buffer,
    int32_t     status) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    CernboxCfOperationInfo opi = {0};
    opi.StructSize    = sizeof(opi);
    opi.Type          = CERNBOX_CF_OPERATION_TYPE_TRANSFER_DATA;
    opi.ConnectionKey = (LONG_PTR)connection_key;
    opi.TransferKey   = transfer_key;

    CernboxCfOperationParametersTransferData params = {0};
    params.ParamsSize       = sizeof(params);
    params.CompletionStatus = status;
    params.FileOffset       = offset;
    params.Length           = length;
    params.Buffer           = (LPVOID)buffer;

    return (int32_t)p_CfExecute(&opi, &params);
}

int32_t cf_set_pin_state(const char *utf8_abs_path, int32_t pinned) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    wchar_t *path = utf8_to_wide(utf8_abs_path);
    if (!path) return E_OUTOFMEMORY;

    /* Open the placeholder for writing attributes. FILE_FLAG_OPEN_REPARSE_POINT
     * keeps us from triggering hydration just by opening it. */
    HANDLE hFile = CreateFileW(
        path,
        FILE_READ_ATTRIBUTES | FILE_WRITE_ATTRIBUTES,
        FILE_SHARE_READ | FILE_SHARE_WRITE,
        NULL,
        OPEN_EXISTING,
        FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS,
        NULL);
    free(path);
    if (hFile == INVALID_HANDLE_VALUE) {
        return HRESULT_FROM_WIN32(GetLastError());
    }

    /* CF_PIN_STATE: 1 = PINNED, 2 = UNPINNED. CF_SET_PIN_FLAG_NONE = 0. */
    int state = pinned ? 1 : 2;
    HRESULT hr = p_CfSetPinState(hFile, state, 0, NULL);
    CloseHandle(hFile);
    return (int32_t)hr;
}
