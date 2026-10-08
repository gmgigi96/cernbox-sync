package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gmgigi96/cernbox-sync/db"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// The desktop client's journal (SyncJournalDb) lives in the root of each sync
// folder. It records the last-synced state of every item and the
// selective-sync lists.

// Item types in the journal's metadata table (ItemType in syncfileitem.h);
// placeholders and the like are not real local files and are skipped.
const (
	itemTypeFile      = 0
	itemTypeDirectory = 2
)

// selectivesync.type values of folders that are not synced: the "do not
// sync" list and big folders still awaiting the user's decision.
const (
	selectiveSyncBlacklist = 1
	selectiveSyncUndecided = 3
)

// errJournalLocked is returned while the desktop client runs: it keeps its
// journal exclusively locked.
var errJournalLocked = errors.New("journal locked by the desktop client")

type journal struct {
	excluded []string
	entries  []db.Entry
}

// Baseline returns the last-synced state the desktop client recorded for f,
// to seed the state of the new folder so that its first sync only transfers
// what changed since then.
func Baseline(f Folder) ([]db.Entry, error) {
	if f.VirtualFiles {
		return nil, fmt.Errorf("%q uses virtual files, which cannot be imported", f.DisplayName)
	}
	if f.JournalPath == "" {
		if f.LocalIsEmpty {
			return nil, nil
		}
		return nil, fmt.Errorf("no sync database found in %s; importing it would upload every local file again", f.LocalPath)
	}
	j, err := readJournal(f.JournalPath)
	if errors.Is(err, errJournalLocked) {
		return nil, fmt.Errorf("%q is still in use by the desktop client; quit it first", f.DisplayName)
	}
	if err != nil {
		return nil, err
	}
	return j.entries, nil
}

// journalFile locates the folder's journal. Releases that predate the
// journalPath key named it after a hash of the account and remote path, so
// the folder is searched for it instead (most recently used first).
func journalFile(localPath, journalPath string) string {
	if journalPath != "" {
		p := filepath.Join(localPath, journalPath)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
		return ""
	}
	entries, err := os.ReadDir(localPath)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if !isJournalName(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(bestTime) {
			best, bestTime = filepath.Join(localPath, e.Name()), info.ModTime()
		}
	}
	return best
}

// isJournalName matches .sync_journal.db (2.9+), .sync_<hash>.db (2.6-2.8),
// ._sync_<hash>.db (up to 2.5) and .csync_journal.db (1.x).
func isJournalName(name string) bool {
	return ((strings.HasPrefix(name, ".sync_") || strings.HasPrefix(name, "._sync_")) && strings.HasSuffix(name, ".db")) ||
		name == ".csync_journal.db"
}

// isClientMetadata matches the files the client keeps in a sync folder,
// which are not user data.
func isClientMetadata(name string) bool {
	for _, prefix := range []string{".sync_", "._sync_", ".csync_journal.db", ".owncloudsync.log"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func localIsEmpty(localPath string) bool {
	entries, err := os.ReadDir(localPath)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !isClientMetadata(e.Name()) {
			return false
		}
	}
	return true
}

func readJournal(path string) (*journal, error) {
	conn, err := openJournal(path)
	if err != nil {
		return nil, journalError(path, err)
	}
	defer func() { _ = conn.Close() }()

	j := &journal{excluded: []string{}}
	// The table is absent when selective sync was never used.
	var tables int
	err = conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'selectivesync'`).Scan(&tables)
	if err != nil {
		return nil, journalError(path, err)
	}
	if tables > 0 {
		rows, err := conn.Query(`SELECT DISTINCT path FROM selectivesync WHERE type IN (?, ?) ORDER BY path`,
			selectiveSyncBlacklist, selectiveSyncUndecided)
		if err != nil {
			return nil, journalError(path, err)
		}
		for rows.Next() {
			var p any
			if err := rows.Scan(&p); err != nil {
				_ = rows.Close()
				return nil, journalError(path, err)
			}
			if p := strings.Trim(text(p), "/"); p != "" {
				j.excluded = append(j.excluded, p)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, journalError(path, err)
		}
	}

	rows, err := conn.Query(`SELECT path, type, md5, modtime, filesize, fileid FROM metadata WHERE type IN (?, ?)`,
		itemTypeFile, itemTypeDirectory)
	if err != nil {
		return nil, journalError(path, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p, etag, fileID any
		var typ int64
		var mtime, size sql.NullInt64
		if err := rows.Scan(&p, &typ, &etag, &mtime, &size, &fileID); err != nil {
			return nil, journalError(path, err)
		}
		j.entries = append(j.entries, db.Entry{
			Path:         text(p),
			ETag:         text(etag),
			IsDir:        typ == itemTypeDirectory,
			Size:         size.Int64,
			LastModified: time.Unix(mtime.Int64, 0),
			FileID:       text(fileID),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, journalError(path, err)
	}
	return j, nil
}

// text converts a column the client may store as text, blob or integer.
func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func journalError(path string, err error) error {
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		if code := se.Code() & 0xff; code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED {
			return errJournalLocked
		}
	}
	return fmt.Errorf("cannot read %s: %w", path, err)
}

// openJournal opens the journal read-only, failing right away when it is
// locked. Opening a WAL-mode database normally recreates its -wal and -shm
// files; when the -wal file is gone the client closed the journal cleanly and
// the main file is complete, so it is read as immutable instead.
func openJournal(path string) (*sql.DB, error) {
	params := "mode=ro&_pragma=busy_timeout(0)"
	if _, err := os.Stat(path + "-wal"); errors.Is(err, os.ErrNotExist) && isWALMode(path) {
		params = "immutable=1"
	}
	p := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(filepath.ToSlash(path))
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive paths: file:///C:/...
	}
	conn, err := sql.Open("sqlite", "file://"+p+"?"+params)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	return conn, nil
}

// isWALMode reports whether the database file format versions (bytes 18-19
// of the SQLite header) are those of WAL mode.
func isWALMode(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 20)
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return header[18] == 2
}
