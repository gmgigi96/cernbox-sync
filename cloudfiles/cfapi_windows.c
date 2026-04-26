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
#include <stdint.h>
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

static PFN_CfRegisterSyncRoot   p_CfRegisterSyncRoot   = NULL;
static PFN_CfUnregisterSyncRoot p_CfUnregisterSyncRoot = NULL;
static PFN_CfConnectSyncRoot    p_CfConnectSyncRoot    = NULL;
static PFN_CfDisconnectSyncRoot p_CfDisconnectSyncRoot = NULL;

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
    if (!p_CfRegisterSyncRoot || !p_CfUnregisterSyncRoot ||
        !p_CfConnectSyncRoot || !p_CfDisconnectSyncRoot) {
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

int32_t cf_connect_sync_root(const char *utf8_path, int64_t *out_connection_key) {
    int32_t loaderr = load_cldapi();
    if (loaderr) return loaderr;

    wchar_t *path = utf8_to_wide(utf8_path);
    if (!path) {
        return E_OUTOFMEMORY;
    }

    /* Empty callback table: only the terminator. The connection succeeds
     * but no events fire — Phase 6 will populate this table with the
     * FETCH_DATA handler that drives hydration. */
    CernboxCfCallbackRegistration callbacks[] = {
        { CERNBOX_CF_CALLBACK_TYPE_NONE, NULL },
    };

    CernboxCfConnectionKey key;
    HRESULT hr = p_CfConnectSyncRoot(path, callbacks, NULL, 0 /* CF_CONNECT_FLAG_NONE */, &key);
    free(path);
    if (SUCCEEDED(hr)) {
        *out_connection_key = (int64_t)key.Internal;
    }
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
