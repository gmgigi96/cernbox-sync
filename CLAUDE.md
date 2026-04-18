# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

```bash
make          # Build both binaries: cernbox-sync (CLI) and cernbox-syncd (daemon)
make cli      # Build CLI only
make daemon   # Build daemon only
make test     # Run all tests (go test ./...)
make test-integration  # Run integration tests (requires Docker, see dev-up)
make fmt      # Format code (go fmt)
make clean    # Remove built binaries
make dev-up   # Start Docker Compose with Revad WebDAV server for integration tests
make dev-down # Stop Docker Compose
make gui      # Build Tauri desktop GUI application
make gui-dev  # Run GUI in development mode with hot reload
```

Run a single test:
```bash
go test ./engine/ -run TestSyncRemoteNewFile
```

## Architecture

**Two-binary design + GUI:**
- `cernbox-syncd` — background daemon that owns all sync state, runs periodic sync cycles (configurable, default 5 min), and listens on a Unix domain socket for commands
- `cernbox-sync` — thin CLI that forwards commands to the daemon via IPC; does no sync work itself
- `gui/` — Tauri desktop application (React/TypeScript frontend + Rust backend) that communicates with the daemon via IPC

**IPC:** Newline-delimited JSON over a Unix domain socket (`$XDG_RUNTIME_DIR/cernbox-sync.sock` on Linux). The `ipc` package defines `Request`/`Response` structs and `Send()`.

**IPC commands:** `list`, `add`, `update`, `remove`, `sync`, `status`, `stop`, `get-settings`, `set-settings`, `get-account`, `set-account`, `subscribe`

The `subscribe` command opens a long-lived connection; the daemon pushes events (`sync-started`, `sync-progress`, `sync-completed`, `sync-failed`, `folder-added`, `folder-removed`, `folder-updated`) as newline-delimited JSON. The GUI uses this to update the UI in real time without polling.

**Sync algorithm** (`engine/engine.go`) runs in 5 phases per cycle:
1. Remote scan — BFS via `PROPFIND Depth:1`
2. Local scan — `filepath.WalkDir`
3. Load state — reads per-folder SQLite DB (`.sync.db`)
4. Classify — compares remote, local, and DB state to produce an action list
5. Execute — applies actions with configurable concurrency (dirs before files; deletions deepest-first)

Conflict resolution is hardcoded to server wins; the local file is renamed `.conflict-YYYYMMDD-HHMMSS<ext>`.

**Execute phase concurrency:**
- `TransferStreams` — number of parallel upload/download workers (0 or 1 = sequential)
- `MetadataStreams` — number of parallel mkdir/rmdir workers per depth tier (0 or 1 = sequential)
- Directory creation/deletion is grouped by depth tier to preserve parent-before-child (creation) and child-before-parent (deletion) invariants

**Bandwidth limiting:**
- `UploadBandwidth` / `DownloadBandwidth` — bytes/sec; 0 = unlimited

**Two databases:**
- Config DB (`<user-config-dir>/cernbox-sync/config.db`) — global; stores registered sync pairs (name, local root, remote URL), credentials, and daemon-wide settings
- Sync state DB (`<local_root>/.sync.db`) — per folder; tracks last-synced snapshot (path, etag, size, etc.) for change detection

**Daemon-wide settings** (stored in config DB `settings` table):
- `sync_interval` — duration string (e.g. `"5m"`, `"1h"`); daemon resets its ticker immediately on change
- `upload_bandwidth` / `download_bandwidth` — int64 bytes/sec
- `transfer_streams` / `metadata_streams` — int concurrency values
- `log_rotate_max_age` — duration string for per-folder log retention

**Per-folder settings** (`FolderSettings` in config DB):
- `sync_hidden_files` — bool; include dot-files in sync
- `auto_sync_on_change` — bool; trigger an immediate sync when local filesystem events are detected (debounced via `fsnotify`)

**Key packages:**
- `engine` — core sync logic and tests (fake WebDAV server used in tests)
- `daemon` — sync loop, IPC server, goroutine-per-folder dispatch, event bus, filesystem watcher
- `webdav` — HTTP WebDAV client (PROPFIND, GET, PUT, MKCOL, DELETE) with basic auth
- `config` — CRUD for the global config DB and settings
- `db` — CRUD for the per-folder sync state DB
- `ipc` — shared protocol types, socket path resolution, event types
- `logger` — stdlib `slog` configuration; custom levels: `off`, `error`, `info`, `debug`, `trace`
- `synclog` — per-folder activity log files with rotation
- `gui/` — Tauri app: `src-tauri/src/lib.rs` (Rust command handlers), `src/` (React/TypeScript pages and components)

**GUI pages:** Dashboard, Settings, Folders, FolderDetail, AccountSetup, SpacePicker (remote WebDAV browser), FolderPicker, LocalFolderPicker

## Project Rules

### Development Guidance

- The sync daemon should only contain the logic for the synchronization. This means it just has to know the local and remote path to synchronize and the algorithm to perform it. All the rest should be added in the "client" (GUI and CLI) when a new feature is requested.

### Verification

- **CRITICAL**: Always build the project and run the tests after any code change and before declaring the task complete.
- The integration tests should use the dev test environment, that can be created with `make dev-up`. If needed check the logs of the `revad` container (`docker compose -f dev/docker-compose.yaml logs revad`).
