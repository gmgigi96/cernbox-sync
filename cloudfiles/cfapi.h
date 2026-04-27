/*
 * cfapi.h — thin C wrappers around the Windows Cloud Files API.
 *
 * Each wrapper accepts UTF-8 strings from the Go side and converts them to
 * UTF-16 internally; in return Go gets a single int32_t HRESULT it can
 * inspect (0 == success). This keeps the CGo surface small and avoids
 * leaking Win32 types up to Go.
 *
 * Phase 4 only: register / unregister / connect / disconnect. No callbacks
 * are wired yet — connect uses an empty callback table so the OS knows the
 * sync root is "live" without actually firing fetch/populate events.
 */
#ifndef CERNBOX_SYNC_CFAPI_H
#define CERNBOX_SYNC_CFAPI_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Register the local folder utf8_path as a Cloud Files sync root.
 * Returns S_OK on success, an HRESULT on failure. The function is idempotent
 * for the typical "already registered" case (HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS))
 * which the Go side maps to nil. */
int32_t cf_register_sync_root(const char *utf8_path, const char *utf8_provider_name);

/* Reverse of cf_register_sync_root. */
int32_t cf_unregister_sync_root(const char *utf8_path);

/* CfConnectSyncRoot is invoked from Go via syscall (see
 * cfapi_syscall_windows.go) rather than a cgo wrapper, so cldapi.dll's
 * internal SEH doesn't collide with Go's vectored exception handler.
 * The static FETCH_DATA callback function is exported via
 * cf_get_fetch_data_callback below. */

/* Disconnect a previously connected sync root. */
int32_t cf_disconnect_sync_root(int64_t connection_key);

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

/* Update an existing placeholder's metadata. If the file was previously
 * hydrated, the OS marks it stale so the next access refetches via
 * FETCH_DATA. */
int32_t cf_update_placeholder(
    const char *utf8_abs_path,
    int64_t     size,
    int64_t     mtime_filetime,
    const void *file_identity,
    int32_t     file_identity_len);

/* Deliver a chunk of file content to the OS in response to a FETCH_DATA
 * callback. transfer_key and connection_key come from the callback's
 * CF_CALLBACK_INFO; offset and length describe where the bytes belong in
 * the file; buffer holds the bytes (size == length). status is an
 * NTSTATUS — 0 means success; non-zero signals an error.
 *
 * For multi-chunk transfers, call repeatedly with rising offsets until
 * the full RequiredLength is delivered. */
int32_t cf_execute_transfer(
    int64_t     connection_key,
    int64_t     transfer_key,
    int64_t     offset,
    int64_t     length,
    const void *buffer,
    int32_t     status);

/* Set the pin state of a placeholder file: pinned != 0 keeps the content
 * always-local even under disk pressure; pinned == 0 reverts to the
 * default lazy state. utf8_abs_path must point to an existing placeholder
 * under a registered sync root. */
int32_t cf_set_pin_state(const char *utf8_abs_path, int32_t pinned);

/* Returns the address of the static FETCH_DATA callback function so the Go
 * side can hand it to CfConnectSyncRoot via syscall, bypassing cgo's
 * exception-handler interception. */
void *cf_get_fetch_data_callback(void);

#ifdef __cplusplus
}
#endif

#endif /* CERNBOX_SYNC_CFAPI_H */
