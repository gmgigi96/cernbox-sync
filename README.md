# cernbox-sync

A bidirectional WebDAV sync client written in Go, targeting CernBox (ownCloud/Nextcloud-compatible servers). Authentication is basic auth. The sync is always client-initiated.

## Architecture

The system is split into two binaries:

- **`cernbox-syncd`** — background daemon that owns the config DB, runs sync cycles on a schedule, and serves IPC requests.
- **`cernbox-sync`** — CLI client that forwards every command to the daemon over a Unix domain socket.

Communication between the two uses a Unix domain socket (JSON over IPC).

## Building

```sh
make          # build both binaries (cernbox-sync and cernbox-syncd)
make cli      # build CLI only
make daemon   # build daemon only
make test     # run tests
make clean    # remove built binaries
```

## Usage

### Start the daemon

```sh
cernbox-syncd                        # default interval: 5 minutes
cernbox-syncd -interval 10m
cernbox-syncd -interval 30s -socket /tmp/my.sock
```

The daemon writes logs to stderr.

### Register a sync folder pair

```sh
cernbox-sync add \
  -name    documents \
  -local   /path/to/local/dir \
  -remote  "https://cernbox.cern.ch/remote.php/dav/spaces/<space-id>/Documents" \
  -user    <username> \
  -pass    <password>
```

### List registered pairs

```sh
cernbox-sync list
```

### Trigger an immediate sync

```sh
# All registered pairs
cernbox-sync run

# One specific pair
cernbox-sync run -name documents
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

1. **Remote scan** — BFS over the remote tree via `PROPFIND Depth:1`.
2. **Local scan** — `filepath.WalkDir` over the local root.
3. **Load state** — reads the last-synced snapshot from the per-folder SQLite DB.
4. **Classify** — compares remote, local, and DB state to determine the action for each path.
5. **Execute** — applies actions (download, upload, mkdir, delete); parent directories before children, deletions deepest-first. Errors are logged and skipped so a partial sync never aborts the run.

### Conflict resolution

When both sides have changed since the last sync, the local file is renamed to `<name>.conflict-YYYYMMDD-HHMMSS<ext>` and the server version is downloaded in its place (server wins).

## Repository layout

```
.
├── main.go                      — CLI entry-point (add, list, remove, run, status, stop)
├── cmd/
│   └── cernbox-syncd/
│       └── main.go              — Daemon entry-point
├── ipc/
│   └── ipc.go                   — Shared IPC protocol: socket path, Request/Response types, Send()
├── daemon/
│   └── daemon.go                — Daemon: sync loop, IPC server, command dispatch
├── config/
│   └── config.go                — Global config DB: registered sync folder pairs (Add, Get, All, Remove)
├── webdav/
│   ├── types.go                 — XML/response types
│   └── client.go                — WebDAV HTTP client: PROPFIND, GET, PUT, MKCOL, DELETE
├── db/
│   └── db.go                    — Per-folder SQLite state store
└── engine/
    └── engine.go                — Bidirectional sync algorithm
```

## Known limitations

- No incremental remote scan: the full remote tree is fetched every cycle.
- Conflict resolution is hard-coded to server wins.
- Basic auth credentials are stored in plaintext in the config DB.
- No concurrency within a sync cycle (uploads/downloads are serial).
- Multiple sync pairs run sequentially in the daemon's sync loop.
- No file-watcher; syncs are purely interval-driven.
- No TLS certificate pinning or mutual-TLS support.
