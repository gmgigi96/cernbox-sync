# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

```bash
make          # Build both binaries: cernbox-sync (CLI) and cernbox-syncd (daemon)
make cli      # Build CLI only
make daemon   # Build daemon only
make test     # Run all tests (go test ./...)
make clean    # Remove built binaries
```

Run a single test:
```bash
go test ./engine/ -run TestSyncRemoteNewFile
```

## Architecture

**Two-binary design:**
- `cernbox-syncd` — background daemon that owns all sync state, runs periodic sync cycles (default 5 min), and listens on a Unix domain socket for commands
- `cernbox-sync` — thin CLI that forwards commands to the daemon via IPC; does no sync work itself

**IPC:** Newline-delimited JSON over a Unix domain socket (`$XDG_RUNTIME_DIR/cernbox-sync.sock` on Linux). The `ipc` package defines `Request`/`Response` structs and `Send()`.

**Sync algorithm** (`engine/engine.go`) runs in 5 phases per cycle:
1. Remote scan — BFS via `PROPFIND Depth:1`
2. Local scan — `filepath.WalkDir`
3. Load state — reads per-folder SQLite DB (`.sync.db`)
4. Classify — compares remote, local, and DB state to produce an action list
5. Execute — applies actions in safe order (dirs before files; deletions deepest-first)

Conflict resolution is hardcoded to server wins; the local file is renamed `.conflict-YYYYMMDD-HHMMSS<ext>`.

**Two databases:**
- Config DB (`<user-config-dir>/cernbox-sync/config.db`) — global, lists all registered sync pairs (name, local root, remote URL, credentials)
- Sync state DB (`<local_root>/.sync.db`) — per folder, tracks last-synced snapshot (path, etag, size, etc.) for change detection

**Key packages:**
- `engine` — core sync logic and tests (fake WebDAV server used in tests)
- `daemon` — sync loop, IPC server, goroutine-per-folder dispatch
- `webdav` — HTTP WebDAV client (PROPFIND, GET, PUT, MKCOL, DELETE) with basic auth
- `config` — CRUD for the global config DB
- `db` — CRUD for the per-folder sync state DB
- `ipc` — shared protocol types and socket path resolution

## Project Rules

### Verification

- **CRITICAL**: Always build the project and run the tests after any code change and before declaring the task complete.
- The integration tests should use the dev test environment, that can be created with `make dev-up`. If needed check the logs of the `revad` container (`docker compose -f dev/docker-compose.yaml logs revad`).
