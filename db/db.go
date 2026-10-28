package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS sync_state (
    path         TEXT    NOT NULL,
    etag         TEXT    NOT NULL,
    is_dir       INTEGER NOT NULL DEFAULT 0,
    size         INTEGER NOT NULL DEFAULT 0,
    last_modified INTEGER NOT NULL DEFAULT 0,
    file_id      TEXT    NOT NULL DEFAULT '',
    permissions  TEXT    NOT NULL DEFAULT '',
    inode        INTEGER NOT NULL DEFAULT 0,
    device       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (path)
);
CREATE TABLE IF NOT EXISTS conflicts (
    path          TEXT    NOT NULL,  -- relative path of the original file
    conflict_path TEXT    NOT NULL,  -- absolute path of the .conflict-* copy
    created_at    INTEGER NOT NULL,  -- unix timestamp when the conflict was recorded
    PRIMARY KEY (conflict_path)
);
`

// Entry represents one row in the sync_state table.
type Entry struct {
	Path         string
	ETag         string
	IsDir        bool
	Size         int64
	LastModified time.Time
	FileID       string
	// Permissions is the oc:permissions string last seen on the server
	// (empty when unknown).
	Permissions string
	// Inode and Device identify the local file or directory as last synced
	// (0 when unknown or unsupported). They let the engine recognise a local
	// rename or move.
	Inode  uint64
	Device uint64
}

const cols = `path, etag, is_dir, size, last_modified, file_id, permissions, inode, device`

type scanner interface{ Scan(dest ...any) error }

func scanEntry(r scanner) (*Entry, error) {
	var e Entry
	var modUnix int64
	var isDir int
	var ino, dev int64
	if err := r.Scan(&e.Path, &e.ETag, &isDir, &e.Size, &modUnix, &e.FileID, &e.Permissions, &ino, &dev); err != nil {
		return nil, err
	}
	e.IsDir = isDir != 0
	e.LastModified = time.Unix(modUnix, 0)
	e.Inode, e.Device = uint64(ino), uint64(dev)
	return &e, nil
}

// DB wraps the SQLite connection.
type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the SQLite DB at the given file path.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// A single connection serialises every statement, so concurrent sync
	// workers never hit SQLITE_BUSY on this file. The busy timeout covers
	// other processes opening the same DB (e.g. a desktop-client import).
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(`PRAGMA busy_timeout = 10000`); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	// WAL with synchronous=NORMAL syncs the disk only at checkpoints instead of
	// on every committed upsert, which made syncs of many small files crawl
	// (notably on Windows). The DB stays consistent after a crash; at worst
	// the last few entries are lost and the next cycle rechecks those files.
	if _, err := conn.Exec(`PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL`); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set journal mode: %w", err)
	}
	if _, err := conn.Exec(schema); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	if err := migrate(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &DB{conn: conn}, nil
}

// migrate upgrades a DB created by an older version.
func migrate(conn *sql.DB) error {
	rows, err := conn.Query(`PRAGMA table_info(sync_state)`)
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("inspect schema: %w", err)
		}
		have[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	for _, c := range []struct{ name, def string }{
		{"permissions", "TEXT NOT NULL DEFAULT ''"},
		{"inode", "INTEGER NOT NULL DEFAULT 0"},
		{"device", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if have[c.name] {
			continue
		}
		if _, err := conn.Exec(`ALTER TABLE sync_state ADD COLUMN ` + c.name + ` ` + c.def); err != nil {
			return fmt.Errorf("migrate schema: %w", err)
		}
	}
	return nil
}

// Close closes the underlying connection.
func (d *DB) Close() error { return d.conn.Close() }

// Get returns the entry for path, or (nil, nil) if not found.
func (d *DB) Get(path string) (*Entry, error) {
	e, err := scanEntry(d.conn.QueryRow(`SELECT `+cols+` FROM sync_state WHERE path = ?`, path))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db get %q: %w", path, err)
	}
	return e, nil
}

func collect(rows *sql.Rows) (map[string]*Entry, error) {
	result := make(map[string]*Entry)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		result[e.Path] = e
	}
	return result, rows.Err()
}

// All returns every entry in the DB keyed by path.
func (d *DB) All() (map[string]*Entry, error) {
	rows, err := d.conn.Query(`SELECT ` + cols + ` FROM sync_state`)
	if err != nil {
		return nil, fmt.Errorf("db all: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collect(rows)
}

const upsertSQL = `INSERT INTO sync_state (` + cols + `)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(path) DO UPDATE SET
           etag=excluded.etag,
           is_dir=excluded.is_dir,
           size=excluded.size,
           last_modified=excluded.last_modified,
           file_id=excluded.file_id,
           permissions=excluded.permissions,
           inode=excluded.inode,
           device=excluded.device`

func entryArgs(e Entry) []any {
	isDir := 0
	if e.IsDir {
		isDir = 1
	}
	return []any{e.Path, e.ETag, isDir, e.Size, e.LastModified.Unix(), e.FileID, e.Permissions, int64(e.Inode), int64(e.Device)}
}

// Upsert inserts or updates an entry.
func (d *DB) Upsert(e Entry) error {
	if _, err := d.conn.Exec(upsertSQL, entryArgs(e)...); err != nil {
		return fmt.Errorf("db upsert %q: %w", e.Path, err)
	}
	return nil
}

// Move describes one entry of a rename: the row at From is replaced by To.
type Move struct {
	From string
	To   Entry
}

// Rename applies moves in a single transaction: every From row is deleted
// first, then every To entry is written (so overlapping paths are safe).
func (d *DB) Rename(moves []Move) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("db rename: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, m := range moves {
		if _, err := tx.Exec(`DELETE FROM sync_state WHERE path = ?`, m.From); err != nil {
			return fmt.Errorf("db rename %q: %w", m.From, err)
		}
	}
	for _, m := range moves {
		if _, err := tx.Exec(upsertSQL, entryArgs(m.To)...); err != nil {
			return fmt.Errorf("db rename %q: %w", m.To.Path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db rename: %w", err)
	}
	return nil
}

// SetIdentities records the inode and device of the given entries (matched by
// path) in a single transaction, leaving everything else untouched.
func (d *DB) SetIdentities(entries []Entry) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("db identities: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`UPDATE sync_state SET inode = ?, device = ? WHERE path = ?`)
	if err != nil {
		return fmt.Errorf("db identities: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range entries {
		if _, err := stmt.Exec(int64(e.Inode), int64(e.Device), e.Path); err != nil {
			return fmt.Errorf("db identities %q: %w", e.Path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db identities: %w", err)
	}
	return nil
}

// ClearMoveHints forgets the inode, device and file id of path and everything
// below it, so that they are no longer candidates for rename detection. It is
// used after a rename could not be carried out, so the next cycle falls back to
// a plain delete and transfer.
func (d *DB) ClearMoveHints(path string) error {
	_, err := d.conn.Exec(
		`UPDATE sync_state SET inode = 0, device = 0, file_id = ''
		 WHERE path = ? OR path LIKE ? ESCAPE '\'`,
		path, escapeLike(path)+"/%",
	)
	if err != nil {
		return fmt.Errorf("db clear move hints %q: %w", path, err)
	}
	return nil
}

// SeedBaseline records entries as the last-synced snapshot of a folder that
// has never been synced by this engine, e.g. one previously managed by another
// sync client. It is a no-op returning false when the DB already holds sync
// state, so an existing baseline is never overwritten. All entries are written
// in a single transaction.
func (d *DB) SeedBaseline(entries []Entry) (bool, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return false, fmt.Errorf("db seed: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sync_state`).Scan(&n); err != nil {
		return false, fmt.Errorf("db seed: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	stmt, err := tx.Prepare(
		`INSERT INTO sync_state (` + cols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return false, fmt.Errorf("db seed: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range entries {
		if _, err := stmt.Exec(entryArgs(e)...); err != nil {
			return false, fmt.Errorf("db seed %q: %w", e.Path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("db seed: %w", err)
	}
	return true, nil
}

// AllUnder returns every entry whose path is strictly under the given prefix
// (i.e. path starts with prefix+"/"). The prefix itself is not included.
func (d *DB) AllUnder(prefix string) (map[string]*Entry, error) {
	rows, err := d.conn.Query(
		`SELECT `+cols+` FROM sync_state WHERE path LIKE ? ESCAPE '\'`,
		escapeLike(prefix)+"/%",
	)
	if err != nil {
		return nil, fmt.Errorf("db allunder %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	return collect(rows)
}

// escapeLike escapes special LIKE characters in s so it can be used as a
// literal prefix in a LIKE pattern (with ESCAPE '\').
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// Delete removes the entry for path.
func (d *DB) Delete(path string) error {
	_, err := d.conn.Exec(`DELETE FROM sync_state WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("db delete %q: %w", path, err)
	}
	return nil
}

// ConflictEntry represents one row in the conflicts table.
type ConflictEntry struct {
	Path         string    // relative path of the original file
	ConflictPath string    // absolute path of the .conflict-* copy
	CreatedAt    time.Time // when the conflict was recorded
}

// AddConflict records a new conflict.
func (d *DB) AddConflict(path, conflictPath string, ts time.Time) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO conflicts (path, conflict_path, created_at) VALUES (?, ?, ?)`,
		path, conflictPath, ts.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db add conflict %q: %w", path, err)
	}
	return nil
}

// AllConflicts returns all recorded conflicts.
func (d *DB) AllConflicts() ([]ConflictEntry, error) {
	rows, err := d.conn.Query(`SELECT path, conflict_path, created_at FROM conflicts`)
	if err != nil {
		return nil, fmt.Errorf("db all conflicts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ConflictEntry
	for rows.Next() {
		var e ConflictEntry
		var ts int64
		if err := rows.Scan(&e.Path, &e.ConflictPath, &ts); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteConflict removes a conflict record by its conflict file path.
func (d *DB) DeleteConflict(conflictPath string) error {
	_, err := d.conn.Exec(`DELETE FROM conflicts WHERE conflict_path = ?`, conflictPath)
	if err != nil {
		return fmt.Errorf("db delete conflict %q: %w", conflictPath, err)
	}
	return nil
}
