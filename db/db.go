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
    PRIMARY KEY (path)
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
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Close closes the underlying connection.
func (d *DB) Close() error { return d.conn.Close() }

// Get returns the entry for path, or (nil, nil) if not found.
func (d *DB) Get(path string) (*Entry, error) {
	row := d.conn.QueryRow(
		`SELECT path, etag, is_dir, size, last_modified, file_id FROM sync_state WHERE path = ?`,
		path,
	)
	var e Entry
	var modUnix int64
	var isDir int
	err := row.Scan(&e.Path, &e.ETag, &isDir, &e.Size, &modUnix, &e.FileID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db get %q: %w", path, err)
	}
	e.IsDir = isDir != 0
	e.LastModified = time.Unix(modUnix, 0)
	return &e, nil
}

// All returns every entry in the DB keyed by path.
func (d *DB) All() (map[string]*Entry, error) {
	rows, err := d.conn.Query(
		`SELECT path, etag, is_dir, size, last_modified, file_id FROM sync_state`,
	)
	if err != nil {
		return nil, fmt.Errorf("db all: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*Entry)
	for rows.Next() {
		var e Entry
		var modUnix int64
		var isDir int
		if err := rows.Scan(&e.Path, &e.ETag, &isDir, &e.Size, &modUnix, &e.FileID); err != nil {
			return nil, err
		}
		e.IsDir = isDir != 0
		e.LastModified = time.Unix(modUnix, 0)
		result[e.Path] = &e
	}
	return result, rows.Err()
}

// Upsert inserts or updates an entry.
func (d *DB) Upsert(e Entry) error {
	isDir := 0
	if e.IsDir {
		isDir = 1
	}
	_, err := d.conn.Exec(
		`INSERT INTO sync_state (path, etag, is_dir, size, last_modified, file_id)
         VALUES (?, ?, ?, ?, ?, ?)
         ON CONFLICT(path) DO UPDATE SET
           etag=excluded.etag,
           is_dir=excluded.is_dir,
           size=excluded.size,
           last_modified=excluded.last_modified,
           file_id=excluded.file_id`,
		e.Path, e.ETag, isDir, e.Size, e.LastModified.Unix(), e.FileID,
	)
	if err != nil {
		return fmt.Errorf("db upsert %q: %w", e.Path, err)
	}
	return nil
}

// AllUnder returns every entry whose path is strictly under the given prefix
// (i.e. path starts with prefix+"/"). The prefix itself is not included.
func (d *DB) AllUnder(prefix string) (map[string]*Entry, error) {
	rows, err := d.conn.Query(
		`SELECT path, etag, is_dir, size, last_modified, file_id FROM sync_state WHERE path LIKE ? ESCAPE '\'`,
		escapeLike(prefix)+"/%",
	)
	if err != nil {
		return nil, fmt.Errorf("db allunder %q: %w", prefix, err)
	}
	defer rows.Close()

	result := make(map[string]*Entry)
	for rows.Next() {
		var e Entry
		var modUnix int64
		var isDir int
		if err := rows.Scan(&e.Path, &e.ETag, &isDir, &e.Size, &modUnix, &e.FileID); err != nil {
			return nil, err
		}
		e.IsDir = isDir != 0
		e.LastModified = time.Unix(modUnix, 0)
		result[e.Path] = &e
	}
	return result, rows.Err()
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
