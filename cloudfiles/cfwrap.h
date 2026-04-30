/*
 * cfwrap.h — thin C wrappers around the Windows Cloud Files API.
 *
 * Each wrapper accepts UTF-8 strings from the Go side and converts them to
 * UTF-16 internally; in return Go gets a single int32_t HRESULT it can
 * inspect (0 == success). This keeps the CGo surface small and avoids
 * leaking Win32 types up to Go.
 *
 * Types come from the official Windows SDK <cfapi.h>. The CORRELATION_VECTOR
 * forward declaration is needed because MinGW does not ship that header.
 *
 * CfConnectSyncRoot and CfExecute are called from Go via syscall.SyscallN
 * (see cfapi_syscall_windows.go) rather than cgo wrappers, so cldapi.dll's
 * internal SEH doesn't collide with Go's vectored exception handler.
 */
#ifndef CERNBOX_SYNC_CFWRAP_H
#define CERNBOX_SYNC_CFWRAP_H

#include <windows.h>

/* MinGW's windows.h does not define NTSTATUS or CORRELATION_VECTOR, both
 * of which cfapi.h uses. Provide minimal forward declarations here. */
#ifndef NTSTATUS
typedef LONG NTSTATUS;
#endif

typedef struct CORRELATION_VECTOR {
    CHAR Version;
    CHAR Vector[129];
} CORRELATION_VECTOR, *PCORRELATION_VECTOR;

#include <cfapi.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Sync-root registration moved to the WinRT path
 * (StorageProviderSyncRootManager.Register, wrapped in cernbox-cf.dll -
 * see cloudfiles/winrt/cernbox-cf.cpp). The legacy CfRegisterSyncRoot /
 * CfUnregisterSyncRoot wrappers were dropped along with their cldapi.dll
 * function-pointer entries; on-demand sync now requires the daemon to
 * run under MSIX package identity. */

/* Disconnect a previously connected sync root. */
int32_t cf_disconnect_sync_root(int64_t connection_key);

/* Tell the OS that the provider is online and idle on the given connection.
 * Without this call, Windows leaves the sync root in a "provider unknown"
 * state — Explorer then treats every access as a recall that times out
 * because no callback target is acknowledged.
 *
 * Should be called once after CfConnectSyncRoot returns successfully. */
int32_t cf_update_provider_status_idle(int64_t connection_key);

/* Create a placeholder for utf8_abs_path. mtime_filetime is the file's
 * modification time as a Windows FILETIME (100 ns ticks since 1601 UTC).
 *
 * file_identity is an opaque caller-supplied blob that the OS hands back
 * unchanged in the FETCH_DATA callback so we know which remote file to
 * fetch — for cernbox-sync we use the UTF-16 relative path.
 *
 * The placeholder is created with MARK_IN_SYNC so the OS treats it as
 * already up to date until the engine says otherwise via Update. */
int32_t cf_create_placeholder(
    const char *utf8_abs_path,
    int64_t     size,
    int64_t     mtime_filetime,
    const void *file_identity,
    int32_t     file_identity_len);

/* Update an existing placeholder's metadata. */
int32_t cf_update_placeholder(
    const char *utf8_abs_path,
    int64_t     size,
    int64_t     mtime_filetime,
    const void *file_identity,
    int32_t     file_identity_len);

/* Set the pin state of a placeholder file: pinned != 0 keeps the content
 * always-local even under disk pressure; pinned == 0 reverts to the
 * default lazy state. utf8_abs_path must point to an existing placeholder
 * under a registered sync root. */
int32_t cf_set_pin_state(const char *utf8_abs_path, int32_t pinned);

/* Returns the address of the static FETCH_DATA callback function so the Go
 * side can hand it to CfConnectSyncRoot via syscall, bypassing cgo's
 * exception-handler interception. */
void *cf_get_fetch_data_callback(void);

/* Returns the address of the static FETCH_PLACEHOLDERS callback. Used to
 * satisfy Windows when it asks us to enumerate the namespace, even under
 * CF_POPULATION_POLICY_FULL. */
void *cf_get_fetch_placeholders_callback(void);

#ifdef __cplusplus
}
#endif

#endif /* CERNBOX_SYNC_CFWRAP_H */
