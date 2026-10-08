# cernbox-sync

A bidirectional WebDAV sync client written in Go, targeting CERNBox. Authentication is basic auth. The sync is always client-initiated.

## Architecture

The system is split into two binaries plus a desktop GUI:

- **`cernbox-syncd`** — background daemon that owns the config DB, runs sync cycles on a schedule, serves IPC requests, and watches the local filesystem for changes.
- **`cernbox-sync`** — thin CLI client that forwards every command to the daemon over a Unix domain socket.
- **`gui/`** — Tauri desktop application (React/TypeScript frontend + Rust backend) that communicates with the daemon over the same IPC socket.

Communication uses a Unix domain socket with newline-delimited JSON. The `subscribe` IPC command opens a long-lived connection over which the daemon pushes real-time events (`sync-started`, `sync-progress`, `sync-completed`, `sync-failed`, `folder-added`, `folder-removed`, `folder-updated`); the GUI uses this to update the UI without polling.

## Building

```sh
make          # build both binaries (cernbox-sync and cernbox-syncd)
make cli      # build CLI only
make daemon   # build daemon only
make gui      # build Tauri desktop GUI
make gui-dev  # run GUI in development mode with hot reload
make test     # run unit tests (go test ./...)
make test-integration  # run integration tests (requires Docker, see dev-up)
make dev-up   # start Docker Compose with Revad WebDAV server for integration tests
make dev-down # stop Docker Compose
make fmt      # format code (go fmt)
make clean    # remove built binaries
```

Run a single test:

```sh
go test ./engine/ -run TestSync_RemoteNewFile
```

## Usage

### Start the daemon

```sh
cernbox-syncd
```

The daemon writes logs to stderr and listens on `$XDG_RUNTIME_DIR/cernbox-sync.sock`.

### Register a sync folder pair

```sh
cernbox-sync add \
  -name    documents \
  -local   /path/to/local/dir \
  -remote  "https://cernbox.cern.ch/remote.php/dav/spaces/<space-id>/Documents"
```

### List registered pairs

```sh
cernbox-sync list
```

### Trigger an immediate sync

```sh
# All registered pairs
cernbox-sync sync

# One specific pair
cernbox-sync sync -name documents
```

The command returns immediately; the sync runs asynchronously inside the daemon.

### Show daemon status

```sh
cernbox-sync status
```

Prints which folders are currently syncing and when each folder was last synced.

### Remove a pair

```sh
cernbox-sync remove -name documents
```

### Stop the daemon

```sh
cernbox-sync stop
```

## Config and state file locations

| Artifact | Linux | macOS | Windows |
|---|---|---|---|
| Config DB | `$XDG_CONFIG_HOME/cernbox-sync/config.db` | `~/Library/Application Support/cernbox-sync/config.db` | `%AppData%\cernbox-sync\config.db` |
| IPC socket | `$XDG_RUNTIME_DIR/cernbox-sync.sock` | `~/Library/Caches/cernbox-sync/sync.sock` | `%LocalAppData%\cernbox-sync\sync.sock` |
| Sync state DB | `<local_root>/.sync.db` | same | same |

## Sync algorithm

Each sync cycle runs five phases:

1. **Remote scan** — BFS over the remote tree via `PROPFIND Depth:1`. Unchanged subtrees are skipped when the directory etag matches the last-recorded value (incremental scan).
2. **Local scan** — `filepath.WalkDir` over the local root.
3. **Load state** — reads the last-synced snapshot from the per-folder SQLite DB.
4. **Classify** — compares remote, local, and DB state to determine the action for each path.
5. **Execute** — applies actions (download, upload, mkdir, delete) with configurable concurrency; parent directories before children, deletions deepest-first. Errors are logged and skipped so a partial sync never aborts the run.

### Conflict resolution

When both sides have changed since the last sync, the local file is renamed to `<name>.conflict-YYYYMMDD-HHMMSS<ext>` and the server version is downloaded in its place (server wins).

## Settings

### Daemon-wide settings

Stored in the config DB and changeable at runtime without a daemon restart via `cernbox-sync set-settings` or the GUI Settings page.

| Setting | Default | Description |
|---|---|---|
| `sync_interval` | `5m` | How often the daemon auto-syncs all folders |
| `upload_bandwidth` | `0` (unlimited) | Max upload throughput in bytes/sec |
| `download_bandwidth` | `0` (unlimited) | Max download throughput in bytes/sec |
| `transfer_streams` | `4` | Parallel upload/download workers |
| `metadata_streams` | `4` | Parallel mkdir/rmdir workers per depth tier |
| `log_rotate_max_age` | `720h` | Per-folder log retention window |

### Per-folder settings

| Setting | Default | Description |
|---|---|---|
| `sync_hidden_files` | `false` | Include dot-files in sync |
| `auto_sync_on_change` | `false` | Trigger an immediate sync when local filesystem events are detected (debounced via `fsnotify`) |

## First-start setup wizard

On first start (no account, or nothing synced yet) the GUI opens a setup wizard:

1. **Welcome** — checks whether the ownCloud / CERNBox desktop client syncs folders on this computer.
2. **Account** — sign in, pre-filled with the desktop client's username when there is one. An account already configured is kept, with the option to use a different one.
3. **Import** — only when the desktop client was found: take over its folders (see below).
4. **Interface** — simple or advanced view; a GUI-only preference stored in the webview's local storage.
5. **Preferences** — sync interval and bandwidth limits, saved as daemon settings.
6. **Finish** — summary, then straight to adding a folder or to the app.

Once finished it is not shown again (the flag lives in the webview's local storage), except when no account is configured. Installs that already sync folders skip it.

## Migrating from the ownCloud / CERNBox desktop client

During the setup wizard the GUI offers to take over the sync folders of a configured ownCloud desktop client, including its CERNBox branding. The daemon does the work (IPC commands `legacy-detect` and `legacy-import`, package `migrate`):

- The configuration is read from `cernbox/cernbox.cfg` or `ownCloud/owncloud.cfg` in `$XDG_CONFIG_HOME` (Linux), `~/Library/Preferences` (macOS) or `%APPDATA%` (Windows), and in the locations used by releases up to 2.4. Stored credentials are never read; the username is pre-filled on the sign-in page.
- Each folder's remote path is matched to a space and checked on the server. CERNBox addresses spaces by their storage path (e.g. `/eos/project/...`) and the personal space as `/home`.
- Selective-sync exclusions become the equivalent folder selection. Files next to excluded folders cannot be part of a selection and are no longer synced.
- The last-synced state recorded in the client's journal (`.sync_journal.db`) seeds the folder's `.sync.db`, so the first sync only transfers what changed since the old client last synced instead of uploading every file again.
- The paused state, the hidden-files setting and fixed bandwidth limits are carried over, and folders sync on local changes as they did in the desktop client.
- Folders using virtual files, and folders whose journal is missing while the local folder holds files, are not imported. The import is blocked while the old client runs (it keeps its journal locked); quit it first, and afterwards remove the folders from it so that it does not sync them again.

## Repository layout

```
.
├── main.go                      — CLI entry-point (add, list, remove, sync, status, stop, …)
├── cmd/
│   └── cernbox-syncd/
│       └── main.go              — Daemon entry-point
├── ipc/
│   └── ipc.go                   — Shared IPC protocol: socket path, Request/Response/Event types, Send()
├── migrate/                     — Import of the ownCloud/CERNBox desktop client's sync folders
├── daemon/
│   ├── daemon.go                — Sync loop, IPC server, goroutine-per-folder dispatch, event bus
│   └── watcher.go               — fsnotify-based filesystem watcher with per-folder debounce
├── config/
│   └── config.go                — Global config DB: sync pairs, credentials, daemon-wide settings
├── webdav/
│   ├── types.go                 — XML/response types
│   └── client.go                — WebDAV HTTP client: PROPFIND, GET, PUT, MKCOL, DELETE; bandwidth limiting
├── db/
│   └── db.go                    — Per-folder SQLite state store and conflict tracking
├── engine/
│   └── engine.go                — Bidirectional sync algorithm
├── logger/
│   └── logger.go                — slog configuration; custom levels (off, error, info, debug, trace)
├── synclog/
│   └── synclog.go               — Per-folder activity log files with time-based rotation
├── integration/
│   └── integration_test.go      — End-to-end tests against a real daemon and Revad WebDAV server
└── gui/                         — Tauri desktop application
    ├── src/                     — React/TypeScript pages and components
    └── src-tauri/               — Rust command handlers and Tauri configuration
```

## Known limitations

- Basic auth credentials are stored in plaintext in the config DB (SSO/OAuth2 not yet implemented).
- No TLS certificate pinning or mutual-TLS support.
- On-demand sync (virtual filesystem / download-on-access) is not yet implemented.
- No delta transfer (rsync-style diffs); changed files are always re-transferred in full.
