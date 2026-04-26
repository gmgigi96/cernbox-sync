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

/* Connect to the sync root identified by utf8_path. The connection key is
 * returned via out_connection_key and must be passed back to disconnect. */
int32_t cf_connect_sync_root(const char *utf8_path, int64_t *out_connection_key);

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

#ifdef __cplusplus
}
#endif

#endif /* CERNBOX_SYNC_CFAPI_H */
