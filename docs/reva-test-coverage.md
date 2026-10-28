# Reva sync-client test plan — CS coverage

Maps every row of the "Sync client tests against reva" plan that applies to
cernbox-sync (CS) to automated tests in this repository.

Legend: **covered** (test existed before), **new** (test added by this work),
**gap → fix** (test added and code changed to pass), **N/A** (cannot apply to
CS on Linux), **not built** (feature does not exist in CS; out of scope for now).

Test locations: `engine/spec_test.go` (fake WebDAV server), `webdav/spec_test.go`,
`daemon/spec_test.go`, `integration/spec_test.go` (real reva, `-tags integration`),
`gui/src/test/*.test.ts(x)`.

## Decisions (from the user)

- VFS (7.x), sharing UI (8.7–8.10), large-folder
  confirmation (6.2): reported, not built.
- 5.4 (same new name on both sides): keep current behaviour — local wins on
  first sync (`TestSync_FirstRun_LocalWins`). Contradicts the plan.
- 6.1 (de-selecting a folder): keep current behaviour — local copy is left
  untouched (`TestSync_SelectiveSync_LocalOutsideSelectionUntouched`).
  Contradicts the plan ("excluded folder is removed locally").
- 5.2 (server delete + local change): keep current behaviour — local file is
  kept as a `.conflict-*` copy, not uploaded again (`TestSync_RemoteDeletedLocalUpdated`).
- 8.4 (share revoked) / remote root gone: never delete local data; the cycle
  fails with a clear error. Contradicts "removes the share locally" (safety first).

## API contract shared by tests and implementation

webdav package:
- `StatusError{Method, Path string; Code int; Body string}` is returned by
  every method (PROPFIND, GET, PUT, MKCOL, DELETE, MOVE) on an unexpected
  status. Error text: `"<METHOD> <path>: status <code>: <body>"`.
- `var ErrUnauthorized = errors.New("webdav: unauthorized")`;
  `(*StatusError).Is(target)` returns true for `ErrUnauthorized` when Code == 401.
- Paths are percent-escaped per segment when building URLs (`# % ? & + ; ' "`,
  spaces, unicode). `Resource.Path` is always the decoded path.
- No overall `http.Client.Timeout` (transfers may take hours). Use dial /
  TLS-handshake / response-header timeouts on the transport instead.
- `func (c *Client) Move(src, dst string, overwrite bool) error` — sends MOVE
  with `Destination` (absolute escaped URL) and `Overwrite: T|F`. 201/204 ok.
- Retries: 429, 502, 503, 504 and transport errors are retried with backoff;
  `Retry-After` (seconds or HTTP date) is honoured, capped at 5 min.
  `func (c *Client) SetRetryPolicy(maxRetries int, baseDelay time.Duration)`;
  defaults 3 retries, 1 s base. PUT is retried only when its body can be
  re-opened (`Client.PutFile(remotePath, localPath string, ...)` or a
  `GetBody`-capable request) — never resend a half-consumed stream.
- `Resource.Permissions` holds `oc:permissions` (already parsed).

engine package:
- `type FileError struct{ Path, Op string; Err error }`,
  `type SyncError struct{ Errors []FileError }` (implements `error`, `Unwrap() []error`).
  `Run` keeps going after a per-file failure and returns `*SyncError` when at
  least one action failed. The DB is never updated for a failed action, so
  the next cycle retries it.
- A 401 anywhere aborts the cycle immediately; `errors.Is(err, webdav.ErrUnauthorized)`
  holds. Nothing is deleted locally or remotely in that cycle.
- `Run` returns an error and changes nothing when the local root does not
  exist (8.5) or the remote root returns 404/403 (8.4).
- Office/editor lock files are never synced: `~$*`, `.~lock.*#`, `~*.tmp`.
- Local rename/move (same inode as a DB entry whose path vanished) → one
  MOVE; a renamed directory → one MOVE for the directory. Remote rename
  (same `oc:fileid` at a new path) → local `os.Rename`, no GET.
- Read-only resources (`oc:permissions` without `W`/`NV` for files, without
  `CK` for folders, without `D` for deletes): local changes are not uploaded,
  the server version is restored locally, a `FileError` is reported.

## Dev environment limits

The dev reva has no share manager (OCS share creation returns 996), and its
trash-bin and versions endpoints list nothing. The integration tests for 8.1,
8.2, 8.4, 8.5, 10.1 and 10.2 therefore skip; the same rows are covered by the
engine tests against the fake server. `CS_IT_LARGE=1` enables the 2 GB / 10 GB
and 10,000-file integration tests (they need ~15 GB free disk for EOS).

## Matrix

| ID | Status | Notes / tests |
|---|---|---|
| 1.1 | covered | Login Flow v2 (from master, PR #2): `loginflow/loginflow_test.go`, `gui/e2e/account.spec.ts`. On auth failure the re-login banner (1.3) runs the same flow. |
| 1.2 | N/A | CS does no server version check, so it never shows an "unsupported server" warning. |
| 1.3 | gap → fix | 401 aborts the cycle, nothing deleted; daemon publishes `sync-failed` with auth error; GUI asks to log in again. |
| 1.4 | covered/new | `gui/src/test/graph.test.ts` — personal, project spaces, received shares. |
| 1.5 | covered | `migrate/plan_test.go` (`TestPlan_BlockedFolders` covers VFS folders), `TestSync_ImportedBaseline_OnlyTransfersChanges`, `daemon/legacy_test.go`. |
| 2.1–2.5 | covered/new | engine + integration; zero-byte file and empty dir added. |
| 2.6 | gap → fix | 60 s `http.Client.Timeout` broke transfers > 60 s. 100 MB in integration; 2 GB / 10 GB opt-in via `CS_IT_LARGE=1`. |
| 2.7 | new | 10,000 files / 50-level tree (engine fake server; integration with `CS_IT_LARGE=1`, 1,000 files by default). |
| 2.8 | covered | `TestSync_Idempotent*`, `TestIntegration_SecondSyncIsNoOp`, `TestIntegration_PausePersistsAcrossRestart`. |
| 3.1–3.5 | gap → fix | No MOVE support existed (rename = delete + upload; fileid, shares, versions lost). |
| 4.1, 4.2, 4.4 | gap → fix | URLs were not escaped (`#`, `%`, `?` broke). 260-char Windows path: N/A. |
| 4.3, 4.5 | N/A | Windows/macOS file systems; on Linux these names are valid and sync (tested). |
| 4.6 | covered | `TestSync_HiddenFiles_*`. |
| 5.1 | covered | `TestSync_Conflict*` — naming is `.conflict-YYYYMMDD-HHMMSS`, not "conflicted copy". |
| 5.2 | covered (decision) | see Decisions. |
| 5.3 | covered | `TestSync_LocalDeletedRemoteUpdated`. |
| 5.4 | covered (decision) | see Decisions. |
| 5.5 | gap → fix | Local `RemoveAll` of the remotely-deleted folder deleted the new local file. Now the folder is recreated remotely and the file uploaded. |
| 5.6 | covered | `TestSync_Conflict_SweepOnResolve`, integration counterpart. |
| 6.1 | covered (decision) | top-level folder selection only; see Decisions. |
| 6.2 | not built | |
| 7.x | not built | no VFS in CS. |
| 8.1 | new | two engine clients on one fake server / two integration users. |
| 8.2, 8.3, 8.11 | gap → fix | `oc:permissions` ignored; 403 handling. |
| 8.4, 8.5 | gap → fix | missing local root used to look like "everything deleted locally". |
| 8.6 | new | GUI space list dedupes a share received via user and via group. |
| 8.7–8.10 | not built | no sharing UI in CS. |
| 9.1–9.4 | new | deep etag propagation, change during sync, mtime-only change. |
| 10.1, 10.2 | new | restore from trash / restore version (fake server; integration where reva supports it). |
| 11.1 | gap → fix | 423 is a per-file error, retried next cycle, no data loss. |
| 11.2 | gap → fix | Office lock files ignored. |
| 11.3 | N/A | NC only. |
| 12.1 | gap → fix | 507 → per-file error, file kept, others continue. |
| 12.2, 12.3 | gap → fix | temp-file download (no partial file locally); retries. |
| 12.4 | gap → fix | one failing file no longer hides failures (`SyncError`), others sync. |
| 12.5 | gap → fix | 429 + `Retry-After`. |
| 12.6 | new | disk-full: temp file removed, DB untouched, clear error. |
| 13.1 | new | daemon ticker reset on `set-settings`. |
| 13.2 | new | engine bandwidth limit. |
| 13.3 | new | `TransferStreams` 1 and 16. |
| 13.4 | covered | `daemon/watcher_test.go`. |
| 13.5 | new | restart during sync; `remove` keeps local files. |
