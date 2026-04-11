# cernbox-sync

A bidirectional WebDAV sync client written in Go, targeting CernBox (ownCloud/Nextcloud-compatible servers).
Authentication is basic auth. The sync is always client-initiated.

## Repository layout

```
.
├── main.go          — CLI entry-point (flag parsing, config assembly)
├── webdav/
│   ├── types.go     — XML/response types: Multistatus, Response, Propstat, Props, Resource
│   └── client.go    — WebDAV HTTP client: PROPFIND, GET, PUT, MKCOL, DELETE
├── db/
│   └── db.go        — SQLite state store: Open, Get, All, Upsert, Delete
└── engine/
    └── engine.go    — Bidirectional sync algorithm: Run, scanRemote, scanLocal, classify, execute
```

## Packages

### `webdav`

A thin HTTP wrapper with basic auth. Every method takes a path **relative to the configured base URL**.

| Method | HTTP verb | Purpose |
|---|---|---|
| `Propfind(path, depth)` | `PROPFIND` | List properties of a resource and (with `depth=1`) its direct children |
| `Get(path)` | `GET` | Download a file; returns `io.ReadCloser` |
| `Put(path, reader, size)` | `PUT` | Upload a file |
| `Mkcol(path)` | `MKCOL` | Create a remote directory |
| `Delete(path)` | `DELETE` | Delete a file or directory tree |

`parseResponse` strips the base URL path prefix from each `<d:href>` in the PROPFIND response to produce a clean relative path. The server returns two `<d:propstat>` blocks per resource (200 OK for found props, 404 for missing ones); only the 200 block is read.

The fields extracted from every resource are: `ETag`, `Size`, `LastModified`, `FileID`, `Permissions`, `IsDir`.

### `db`

SQLite-backed state store using `modernc.org/sqlite` (pure Go, no CGo).

Schema — one table `sync_state`:

| Column | Type | Meaning |
|---|---|---|
| `path` | TEXT PK | Forward-slash relative path within the sync root |
| `etag` | TEXT | Last-seen server ETag (quotes stripped) |
| `is_dir` | INTEGER | 1 for directories, 0 for files |
| `size` | INTEGER | File size in bytes |
| `last_modified` | INTEGER | Unix timestamp of server `Last-Modified` |
| `file_id` | TEXT | Server-assigned immutable file identifier (`oc:fileid`) |

`Upsert` uses SQLite's `ON CONFLICT … DO UPDATE` so insert and update are a single statement. `All` returns every row as `map[path]*Entry` which is the main input to the classifier.

### `engine`

The sync algorithm. Entry-point is `engine.Run(cfg Config)`.

#### Config

```go
type Config struct {
    LocalRoot  string  // absolute path of local directory
    RemoteBase string  // full WebDAV URL of the remote folder
    Username   string
    Password   string
    DBPath     string  // path to .sync.db (defaults to <LocalRoot>/.sync.db)
}
```

#### Algorithm — five phases

**Phase 1 — Remote scan (`scanRemote`)**

BFS over the remote tree using `PROPFIND Depth:1`. Starting from the root (`""`), each discovered subdirectory is appended to the work queue. Returns `map[relativePath]*Resource`.

Using Depth 1 (rather than Infinity) avoids oversized responses on servers that cap depth, at the cost of O(depth) round-trips.

**Phase 2 — Local scan (`scanLocal`)**

`filepath.WalkDir` over `LocalRoot`. All paths are converted to forward-slash and made relative to `LocalRoot`. Returns `map[relativePath]*localInfo` where `localInfo` holds absolute path, size, mtime, isDir.

**Phase 3 — Load DB (`db.All`)**

Loads the entire `sync_state` table into a `map[relativePath]*Entry`. This is the "last successfully synced" snapshot.

**Phase 4 — Classify**

Every path seen in either the remote map or the local map is examined against the DB baseline. The classification table:

| Remote | Local | DB | Verdict | Action |
|---|---|---|---|---|
| present | absent | absent | new on remote | `mkdirLocal` / `download` |
| present | present | absent | both exist, no baseline (first run) | `mkcolRemote` / `upload` (local wins) |
| present | absent | present | local was deleted | `deleteRemote` |
| present | present | etag same, local unchanged | in sync | nothing |
| present | present | etag changed | remote updated | `mkdirLocal` / `download` |
| present | present | local mtime/size changed | local updated | `mkcolRemote` / `upload` |
| present | present | both changed | conflict | `conflictTake` (server wins) |
| absent | present | absent | new local | `mkcolRemote` / `upload` |
| absent | present | present | remote was deleted | `deleteLocal` |

Local change detection for files: `size != db.Size OR mtime > db.LastModified + 1s`. The 1-second tolerance absorbs filesystem timestamp rounding. Directories are never considered locally changed (their content is reflected by the children).

**Conflict resolution:** the local file is renamed to `<name>.conflict-YYYYMMDD-HHMMSS<ext>` and the server version is downloaded in its place.

**Phase 5 — Execute (`execute` / `execOne`)**

Actions are sorted before execution:
- Non-delete actions (creates/downloads/uploads): sorted shallow-first (parents before children, by `/`-count).
- Delete actions: sorted deep-first (children before parents).
- All non-deletes come before deletes.

Each action is attempted independently; errors are logged and skipped so a partial sync never aborts the rest of the run.

After every successful transfer the DB is updated with the new ETag from the server (re-fetched via `PROPFIND Depth:0` after PUT/MKCOL so the DB always reflects the server's authoritative value).

Downloads use an atomic write: content is streamed to a `.tmp-sync-*` temp file in the same directory, then `os.Rename`d into place to avoid leaving a partial file visible.

## CLI

```
go build -o syncclient .

./syncclient \
  -local  /path/to/local/dir \
  -remote "https://cernbox.cern.ch/remote.php/dav/spaces/<space-id>/Documents" \
  -user   <username> \
  -pass   <password> \
  [-db    /path/to/state.db]
```

The DB defaults to `<local>/.sync.db`.

## Dependencies

| Module | Purpose |
|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGo) |
| `golang.org/x/net` | Pulled in transitively |

## Known limitations / future work

- No incremental remote scan: the entire remote tree is fetched every sync cycle. An optimisation would be to check only the root ETag first and skip the full scan if it has not changed since the last run.
- Conflict resolution is hard-coded to "server wins". A pluggable strategy would be cleaner.
- Basic auth credentials are passed on the command line (visible in `ps`). A secrets manager or keyring integration would be safer for production use.
- No concurrency: uploads and downloads happen serially. A worker-pool over the action list would speed up syncs with many small files.
- The `.sync.db` file is stored inside the sync root, so it appears as an untracked local file. It should be excluded from the local scan.
