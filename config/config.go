// Package config manages the global application configuration stored in a
// SQLite database. It tracks the set of sync folder pairs (local ↔ remote)
// that the user has registered.
package config

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS sync_folders (
    name        TEXT    NOT NULL,
    local_root  TEXT    NOT NULL,
    remote_base TEXT    NOT NULL,
    username    TEXT    NOT NULL DEFAULT '',
    password    TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (name)
);
`

// Folder represents one registered sync pair.
type Folder struct {
	Name       string
	LocalRoot  string
	RemoteBase string
	Username   string
	Password   string
}

// DB is the global application configuration store.
type DB struct {
	conn *sql.DB
}

// DefaultPath returns the default path for the global config database.
//
// The location is platform-specific:
//   - Linux:   $XDG_CONFIG_HOME/cernbox-sync/config.db  (default ~/.config/…)
//   - macOS:   ~/Library/Application Support/cernbox-sync/config.db
//   - Windows: %AppData%\cernbox-sync\config.db
func DefaultPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	dir := filepath.Join(base, "cernbox-sync")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create config dir %s: %w", dir, err)
	}
	return filepath.Join(dir, "config.db"), nil
}

// Open opens (or creates) the global config DB at path.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open config db: %w", err)
	}
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create config schema: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Close closes the underlying connection.
func (d *DB) Close() error { return d.conn.Close() }

// Add registers a new sync folder pair. Returns an error if the name already exists.
func (d *DB) Add(f Folder) error {
	_, err := d.conn.Exec(
		`INSERT INTO sync_folders (name, local_root, remote_base, username, password)
		 VALUES (?, ?, ?, ?, ?)`,
		f.Name, f.LocalRoot, f.RemoteBase, f.Username, f.Password,
	)
	if err != nil {
		return fmt.Errorf("add folder %q: %w", f.Name, err)
	}
	return nil
}

// Get returns the folder with the given name, or (nil, nil) if not found.
func (d *DB) Get(name string) (*Folder, error) {
	row := d.conn.QueryRow(
		`SELECT name, local_root, remote_base, username, password
		 FROM sync_folders WHERE name = ?`, name,
	)
	var f Folder
	if err := row.Scan(&f.Name, &f.LocalRoot, &f.RemoteBase, &f.Username, &f.Password); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("get folder %q: %w", name, err)
	}
	return &f, nil
}

// All returns every registered folder.
func (d *DB) All() ([]Folder, error) {
	rows, err := d.conn.Query(
		`SELECT name, local_root, remote_base, username, password
		 FROM sync_folders ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()

	var result []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.Name, &f.LocalRoot, &f.RemoteBase, &f.Username, &f.Password); err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}

// Remove deletes the folder with the given name.
func (d *DB) Remove(name string) error {
	res, err := d.conn.Exec(`DELETE FROM sync_folders WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("remove folder %q: %w", name, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("folder %q not found", name)
	}
	return nil
}
