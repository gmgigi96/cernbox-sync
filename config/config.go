// Package config manages the global application configuration stored in a
// SQLite database. It tracks the set of sync folder pairs (local ↔ remote)
// that the user has registered.
package config

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
`

// Setting keys stored in the settings table.
const (
	// KeyLogRotateMaxAge holds the maximum age of log entries as a
	// Go duration string (e.g. "30d", "168h"). Empty string means no rotation.
	KeyLogRotateMaxAge = "log_rotate_max_age"
)

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

// ── settings ──────────────────────────────────────────────────────────────────

// Settings holds global daemon settings.
type Settings struct {
	// LogRotateMaxAge is the maximum age of entries kept in per-folder sync
	// logs. Entries older than this are removed when Rotate is called at the
	// start of each sync cycle. Zero means no rotation.
	LogRotateMaxAge time.Duration
}

// GetSettings reads the current settings from the DB, returning defaults for
// any key that has not been explicitly set.
func (d *DB) GetSettings() (Settings, error) {
	rows, err := d.conn.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	defer rows.Close()

	var s Settings
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Settings{}, err
		}
		switch key {
		case KeyLogRotateMaxAge:
			if value != "" {
				d, err := time.ParseDuration(value)
				if err != nil {
					return Settings{}, fmt.Errorf("invalid %s %q: %w", KeyLogRotateMaxAge, value, err)
				}
				s.LogRotateMaxAge = d
			}
		}
	}
	return s, rows.Err()
}

// SetSettings persists the given settings to the DB, overwriting any
// previously stored values.
func (d *DB) SetSettings(s Settings) error {
	maxAge := ""
	if s.LogRotateMaxAge > 0 {
		maxAge = s.LogRotateMaxAge.String()
	}
	_, err := d.conn.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		KeyLogRotateMaxAge, maxAge,
	)
	if err != nil {
		return fmt.Errorf("set settings: %w", err)
	}
	return nil
}
