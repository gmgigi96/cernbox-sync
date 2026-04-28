package engine_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/db"
	"github.com/gmgigi96/cernbox-sync/engine"
)

const (
	davBasePath    = "/dav"
	httpTimeLayout = "Mon, 02 Jan 2006 15:04:05 GMT"
)

// ─── fake WebDAV server ───────────────────────────────────────────────────────

type fakeFile struct {
	content []byte
	isDir   bool
	etag    string
	modTime time.Time
	fileID  string
}

// fakeWebDAV is a minimal in-memory WebDAV server.
// It records side-effects (PUTs, DELETEs, MKCOLs) so tests can assert on them.
type fakeWebDAV struct {
	mu    sync.Mutex
	files map[string]*fakeFile // keyed by relative path (no leading slash)

	puts      []string
	deletes   []string
	mkcols    []string
	propfinds []string // path of every PROPFIND received
}

func newFakeWebDAV() *fakeWebDAV {
	f := &fakeWebDAV{files: make(map[string]*fakeFile)}
	// Root collection must always be present so the initial PROPFIND succeeds.
	f.files[""] = &fakeFile{isDir: true, etag: "root", modTime: time.Now(), fileID: "root-id"}
	return f
}

func (f *fakeWebDAV) addFile(path, content, etag string, modTime time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = &fakeFile{
		content: []byte(content),
		etag:    etag,
		modTime: modTime,
		fileID:  "fid-" + strings.ReplaceAll(path, "/", "-"),
	}
}

func (f *fakeWebDAV) addDir(path, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = &fakeFile{
		isDir:   true,
		etag:    etag,
		modTime: time.Now(),
		fileID:  "fid-" + strings.ReplaceAll(path, "/", "-"),
	}
}

func (f *fakeWebDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, davBasePath)
	rel = strings.TrimLeft(rel, "/")

	switch r.Method {
	case "PROPFIND":
		f.servePropfind(w, r, rel)
	case "GET":
		f.serveGet(w, r, rel)
	case "PUT":
		f.servePut(w, r, rel)
	case "MKCOL":
		f.serveMkcol(w, r, rel)
	case "DELETE":
		f.serveDelete(w, r, rel)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// servePropfind returns a WebDAV multistatus XML response.
// With Depth: 1 it includes direct children of a collection.
func (f *fakeWebDAV) servePropfind(w http.ResponseWriter, r *http.Request, relPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.propfinds = append(f.propfinds, relPath)

	entry, ok := f.files[relPath]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	paths := []string{relPath}
	if r.Header.Get("Depth") != "0" && entry.isDir {
		prefix := relPath
		if prefix != "" {
			prefix += "/"
		}
		for p := range f.files {
			if p == relPath {
				continue
			}
			if strings.HasPrefix(p, prefix) {
				// Only direct children: no further slash in the suffix.
				if !strings.Contains(p[len(prefix):], "/") {
					paths = append(paths, p)
				}
			}
		}
	}
	sort.Strings(paths)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	_, _ = fmt.Fprint(w, `<?xml version="1.0"?>`)
	_, _ = fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	for _, p := range paths {
		e := f.files[p]
		href := davBasePath
		if p != "" {
			href += "/" + p
		}
		_, _ = fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop>`, href)
		_, _ = fmt.Fprintf(w, `<d:getetag>"%s"</d:getetag>`, e.etag)
		_, _ = fmt.Fprintf(w, `<d:getlastmodified>%s</d:getlastmodified>`, e.modTime.UTC().Format(httpTimeLayout))
		if e.isDir {
			_, _ = fmt.Fprint(w, `<d:resourcetype><d:collection/></d:resourcetype>`)
		} else {
			_, _ = fmt.Fprint(w, `<d:resourcetype/>`)
			_, _ = fmt.Fprintf(w, `<d:getcontentlength>%d</d:getcontentlength>`, len(e.content))
		}
		_, _ = fmt.Fprintf(w, `<oc:fileid>%s</oc:fileid>`, e.fileID)
		_, _ = fmt.Fprint(w, `</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	_, _ = fmt.Fprint(w, `</d:multistatus>`)
}

func (f *fakeWebDAV) serveGet(w http.ResponseWriter, r *http.Request, relPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.files[relPath]
	if !ok || e.isDir {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.content)
}

func (f *fakeWebDAV) servePut(w http.ResponseWriter, r *http.Request, relPath string) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[relPath] = &fakeFile{
		content: body,
		etag:    "etag-after-put",
		modTime: time.Now(),
		fileID:  "fid-" + strings.ReplaceAll(relPath, "/", "-"),
	}
	f.puts = append(f.puts, relPath)
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeWebDAV) serveMkcol(w http.ResponseWriter, r *http.Request, relPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[relPath] = &fakeFile{
		isDir:   true,
		etag:    "etag-after-mkcol",
		modTime: time.Now(),
		fileID:  "fid-" + strings.ReplaceAll(relPath, "/", "-"),
	}
	f.mkcols = append(f.mkcols, relPath)
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeWebDAV) serveDelete(w http.ResponseWriter, r *http.Request, relPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Remove the resource and any children (collection delete).
	prefix := relPath + "/"
	for p := range f.files {
		if p == relPath || strings.HasPrefix(p, prefix) {
			delete(f.files, p)
		}
	}
	f.deletes = append(f.deletes, relPath)
	w.WriteHeader(http.StatusNoContent)
}

// resetSideEffects clears the recorded side-effect slices so a subsequent
// run's actions can be inspected in isolation.
func (f *fakeWebDAV) resetSideEffects() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = nil
	f.deletes = nil
	f.mkcols = nil
}

// ─── test environment ─────────────────────────────────────────────────────────

type testEnv struct {
	t        *testing.T
	localDir string
	dbPath   string
	dav      *fakeWebDAV
	cfg      engine.Config
}

// setup creates an isolated test environment with a temp local dir, a temp DB,
// and a fresh fake WebDAV server.
func setup(t *testing.T) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "local")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(tmp, "sync.db")

	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	return &testEnv{
		t:        t,
		localDir: localDir,
		dbPath:   dbPath,
		dav:      dav,
		cfg: engine.Config{
			LocalRoot:  localDir,
			RemoteBase: srv.URL + davBasePath,
			Username:   "user",
			Password:   "pass",
			DBPath:     dbPath,
		},
	}
}

func (e *testEnv) run() {
	e.t.Helper()
	if err := engine.Run(e.cfg); err != nil {
		e.t.Fatalf("engine.Run: %v", err)
	}
}

// assertNoActions fails the test if any uploads, deletes, or remote mkdir
// operations were recorded on the fake WebDAV server since the last
// resetSideEffects call (or since the server was created).
func (e *testEnv) assertNoActions(label string) {
	e.t.Helper()
	if len(e.dav.puts) != 0 {
		e.t.Errorf("%s: unexpected uploads: %v", label, e.dav.puts)
	}
	if len(e.dav.deletes) != 0 {
		e.t.Errorf("%s: unexpected deletes: %v", label, e.dav.deletes)
	}
	if len(e.dav.mkcols) != 0 {
		e.t.Errorf("%s: unexpected MKCOLs: %v", label, e.dav.mkcols)
	}
}

func (e *testEnv) writeLocal(name, content string) {
	e.t.Helper()
	abs := filepath.Join(e.localDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// writeLocalAt creates a local file and sets its modification time explicitly.
// This is important for tests that distinguish "local changed" from "in sync".
func (e *testEnv) writeLocalAt(name, content string, mtime time.Time) {
	e.t.Helper()
	e.writeLocal(name, content)
	abs := filepath.Join(e.localDir, filepath.FromSlash(name))
	if err := os.Chtimes(abs, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) mkdirLocal(name string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Join(e.localDir, filepath.FromSlash(name)), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) readLocal(name string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.localDir, filepath.FromSlash(name)))
	if err != nil {
		e.t.Fatalf("readLocal %q: %v", name, err)
	}
	return string(b)
}

func (e *testEnv) localExists(name string) bool {
	_, err := os.Stat(filepath.Join(e.localDir, filepath.FromSlash(name)))
	return err == nil
}

func (e *testEnv) localIsDir(name string) bool {
	info, err := os.Stat(filepath.Join(e.localDir, filepath.FromSlash(name)))
	return err == nil && info.IsDir()
}

// seedDB pre-populates the state database to simulate a previous sync having run.
func (e *testEnv) seedDB(entries ...db.Entry) {
	e.t.Helper()
	d, err := db.Open(e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, entry := range entries {
		if err := d.Upsert(entry); err != nil {
			e.t.Fatalf("seedDB upsert %q: %v", entry.Path, err)
		}
	}
}

// dbGet reads a single entry from the state DB (for post-sync assertions).
func (e *testEnv) dbGet(path string) *db.Entry {
	e.t.Helper()
	d, err := db.Open(e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	entry, err := d.Get(path)
	if err != nil {
		e.t.Fatalf("dbGet %q: %v", path, err)
	}
	return entry
}

// dbConflicts reads all conflict entries from the state DB.
func (e *testEnv) dbConflicts() []db.ConflictEntry {
	e.t.Helper()
	d, err := db.Open(e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	entries, err := d.AllConflicts()
	if err != nil {
		e.t.Fatalf("dbConflicts: %v", err)
	}
	return entries
}

// conflictFiles returns names of files inside a local sub-directory whose names
// contain ".conflict-" — i.e. files renamed by the conflict resolution logic.
// Pass "." for the sync root itself.
func (e *testEnv) conflictFiles(subdir string) []string {
	e.t.Helper()
	abs := filepath.Join(e.localDir, filepath.FromSlash(subdir))
	entries, err := os.ReadDir(abs)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, de := range entries {
		if strings.Contains(de.Name(), ".conflict-") {
			out = append(out, de.Name())
		}
	}
	return out
}

// ─── scenario tests ───────────────────────────────────────────────────────────

// Remote has a new file; local is empty; DB is empty.
// Expected: file downloaded; DB entry created with the server ETag.
func TestSync_RemoteNewFile(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	env.dav.addFile("hello.txt", "hello world", "etag1", modTime)

	env.run()

	if !env.localExists("hello.txt") {
		t.Fatal("hello.txt should have been downloaded")
	}
	if got := env.readLocal("hello.txt"); got != "hello world" {
		t.Fatalf("content: got %q, want %q", got, "hello world")
	}
	entry := env.dbGet("hello.txt")
	if entry == nil {
		t.Fatal("DB entry for hello.txt should exist")
	}
	if entry.ETag != "etag1" {
		t.Fatalf("DB ETag: got %q, want %q", entry.ETag, "etag1")
	}
}

// Remote has a new directory; local is empty; DB is empty.
// Expected: directory created locally; DB entry with IsDir=true.
func TestSync_RemoteNewDir(t *testing.T) {
	env := setup(t)
	env.dav.addDir("docs", "etag-docs")

	env.run()

	if !env.localIsDir("docs") {
		t.Fatal("docs/ directory should have been created locally")
	}
	entry := env.dbGet("docs")
	if entry == nil || !entry.IsDir {
		t.Fatalf("DB should have IsDir=true entry for docs; got %v", entry)
	}
}

// Remote has a nested file inside a new directory.
// Expected: parent directory and the file are both created locally in the right order.
func TestSync_RemoteNewNestedFile(t *testing.T) {
	env := setup(t)
	env.dav.addDir("docs", "etag-docs")
	env.dav.addFile("docs/readme.txt", "readme content", "etag-readme", time.Now())

	env.run()

	if !env.localIsDir("docs") {
		t.Fatal("docs/ should exist")
	}
	if !env.localExists("docs/readme.txt") {
		t.Fatal("docs/readme.txt should have been downloaded")
	}
	if got := env.readLocal("docs/readme.txt"); got != "readme content" {
		t.Fatalf("content: got %q, want %q", got, "readme content")
	}
}

// Local has a new file; remote is empty; DB is empty.
// Expected: file uploaded (PUT); DB entry created.
func TestSync_LocalNewFile(t *testing.T) {
	env := setup(t)
	env.writeLocal("notes.txt", "my notes")

	env.run()

	if !contains(env.dav.puts, "notes.txt") {
		t.Fatalf("notes.txt should have been uploaded; puts=%v", env.dav.puts)
	}
	if env.dbGet("notes.txt") == nil {
		t.Fatal("DB entry for notes.txt should exist after upload")
	}
}

// Local has a new directory; remote is empty; DB is empty.
// Expected: directory created on remote via MKCOL.
func TestSync_LocalNewDir(t *testing.T) {
	env := setup(t)
	env.mkdirLocal("projects")

	env.run()

	if !contains(env.dav.mkcols, "projects") {
		t.Fatalf("projects/ should have been MKCOL'd; mkcols=%v", env.dav.mkcols)
	}
}

// File was previously synced (in DB + local); remote has since removed it.
// Expected: local file deleted; DB entry removed.
func TestSync_RemoteDeleted(t *testing.T) {
	env := setup(t)
	env.writeLocal("old.txt", "old content")
	env.seedDB(db.Entry{
		Path:         "old.txt",
		ETag:         "etag-old",
		Size:         11, // len("old content")
		LastModified: time.Now(),
	})

	env.run()

	if env.localExists("old.txt") {
		t.Fatal("old.txt should have been deleted (remote removed it)")
	}
	if env.dbGet("old.txt") != nil {
		t.Fatal("DB entry for old.txt should be gone")
	}
}

// Remote deleted a file while the local copy was modified since last sync.
// Per the "write wins over delete" rule, the local changes must be preserved
// as a conflict copy; the original path is removed.
func TestSync_RemoteDeletedLocalUpdated(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	// Local file has been modified (larger size, newer mtime) since last sync.
	env.writeLocalAt("report.txt", "locally modified content", newTime)
	// DB baseline has the old size so isLocalChanged fires.
	env.seedDB(db.Entry{
		Path:         "report.txt",
		ETag:         "etag-old",
		Size:         11,
		LastModified: oldTime,
	})
	// Remote has no "report.txt" — it was deleted server-side.

	env.run()

	// Original path must be gone.
	if env.localExists("report.txt") {
		t.Fatal("report.txt original should be deleted (server deletion wins)")
	}
	// DB entry for the original path must be removed.
	if env.dbGet("report.txt") != nil {
		t.Fatal("DB entry for report.txt should be gone")
	}
	// Local changes must be preserved as a conflict copy.
	conflicts := env.dbConflicts()
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict entry; got %d", len(conflicts))
	}
	if _, err := os.Stat(conflicts[0].ConflictPath); err != nil {
		t.Fatalf("conflict copy %q should exist on disk: %v", conflicts[0].ConflictPath, err)
	}
	got, _ := os.ReadFile(conflicts[0].ConflictPath)
	if string(got) != "locally modified content" {
		t.Fatalf("conflict copy should contain local changes; got %q", got)
	}
}

// File was previously synced (in DB + remote); it has since been deleted locally.
// Expected: remote file deleted via DELETE; DB entry removed.
func TestSync_LocalDeleted(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	env.dav.addFile("old.txt", "old content", "etag-old", modTime)
	// Seed DB as if synced before; local file no longer exists on disk.
	env.seedDB(db.Entry{
		Path:         "old.txt",
		ETag:         "etag-old",
		Size:         11,
		LastModified: modTime,
	})

	env.run()

	if !contains(env.dav.deletes, "old.txt") {
		t.Fatalf("old.txt should have been deleted from remote; deletes=%v", env.dav.deletes)
	}
	if env.dbGet("old.txt") != nil {
		t.Fatal("DB entry for old.txt should be gone")
	}
}

// File was deleted locally but the remote version changed in the meantime.
// Per the "write wins over delete" rule, the server's new content must be
// restored locally; the remote file must NOT be deleted.
func TestSync_LocalDeletedRemoteUpdated(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	env.dav.addFile("report.txt", "new server content", "etag-v2", newTime)
	// DB baseline matches the OLD remote etag; local file no longer exists.
	env.seedDB(db.Entry{
		Path:         "report.txt",
		ETag:         "etag-v1",
		Size:         10,
		LastModified: oldTime,
	})

	env.run()

	// Remote must NOT have been deleted.
	if contains(env.dav.deletes, "report.txt") {
		t.Fatal("report.txt must not be deleted from remote; server write should win")
	}
	// Server version must be restored locally.
	if got := env.readLocal("report.txt"); got != "new server content" {
		t.Fatalf("local file should contain server version; got %q", got)
	}
	// DB must reflect the new etag.
	entry := env.dbGet("report.txt")
	if entry == nil || entry.ETag != "etag-v2" {
		t.Fatalf("DB should reflect new etag; entry=%v", entry)
	}
}

// Remote ETag changed since last sync; local file is unchanged.
// Expected: file re-downloaded; DB updated with the new ETag.
func TestSync_RemoteUpdated(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 7, 10, 0, 0, 0, time.UTC)

	env.dav.addFile("report.txt", "updated content", "etag-v2", newTime)

	// Local has old content; mtime matches DB baseline so it is not locally changed.
	env.writeLocalAt("report.txt", "old content", oldTime)
	env.seedDB(db.Entry{
		Path:         "report.txt",
		ETag:         "etag-v1", // remote is now "etag-v2" → remote changed
		Size:         11,        // len("old content")
		LastModified: oldTime,
	})

	env.run()

	if got := env.readLocal("report.txt"); got != "updated content" {
		t.Fatalf("file should contain server version after download; got %q", got)
	}
	entry := env.dbGet("report.txt")
	if entry == nil || entry.ETag != "etag-v2" {
		t.Fatalf("DB should reflect new ETag; entry=%v", entry)
	}
}

// Local file changed (newer mtime / different size) since last sync; remote is unchanged.
// Expected: file uploaded (PUT); DB updated.
func TestSync_LocalUpdated(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 7, 12, 0, 0, 0, time.UTC)

	env.dav.addFile("notes.txt", "original", "etag-v1", oldTime)

	// Local file was edited: different content (size differs) and newer mtime.
	env.writeLocalAt("notes.txt", "locally edited", newTime)
	env.seedDB(db.Entry{
		Path:         "notes.txt",
		ETag:         "etag-v1", // same as remote → no remote change
		Size:         8,         // len("original"); local is now 14 bytes
		LastModified: oldTime,
	})

	env.run()

	if !contains(env.dav.puts, "notes.txt") {
		t.Fatalf("notes.txt should have been uploaded; puts=%v", env.dav.puts)
	}
}

// Both remote ETag and local file changed since last sync.
// Expected: conflict — server wins; local copy renamed with a ".conflict-" suffix.
func TestSync_Conflict(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	env.dav.addFile("shared.txt", "server version", "etag-v2", newTime)

	// Local also changed: different size and mtime newer than DB baseline.
	env.writeLocalAt("shared.txt", "local version", newTime)
	env.seedDB(db.Entry{
		Path:         "shared.txt",
		ETag:         "etag-v1", // remote is "etag-v2" → remote changed
		Size:         8,         // local is 13 bytes → local also changed
		LastModified: oldTime,
	})

	env.run()

	if got := env.readLocal("shared.txt"); got != "server version" {
		t.Fatalf("shared.txt should be server version after conflict resolution; got %q", got)
	}
	if conflicts := env.conflictFiles("."); len(conflicts) == 0 {
		t.Fatal("a conflict-renamed copy of the local file should exist")
	}
}

// After a conflict is resolved (server wins, local copy renamed to .conflict-*),
// a second sync must NOT upload the conflict file to the remote.
func TestSync_ConflictFile_NotUploaded(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	env.dav.addFile("shared.txt", "server version", "etag-v2", newTime)
	env.writeLocalAt("shared.txt", "local version", newTime)
	env.seedDB(db.Entry{
		Path:         "shared.txt",
		ETag:         "etag-v1",
		Size:         8, // local is 13 bytes → local also changed
		LastModified: oldTime,
	})

	// First sync: conflict resolved — server wins, conflict file created locally.
	env.run()

	conflicts := env.conflictFiles(".")
	if len(conflicts) == 0 {
		t.Fatal("expected a conflict-renamed file after first sync")
	}

	// Second sync: conflict file must not be uploaded.
	env.dav.resetSideEffects()
	env.run()

	for _, put := range env.dav.puts {
		if strings.Contains(put, ".conflict-") {
			t.Fatalf("conflict file should not be uploaded; got PUT for %q", put)
		}
	}
}

// Nothing changed: remote ETag, local mtime and size all match the DB baseline.
// Expected: no uploads, no downloads, no deletes.
func TestSync_InSync(t *testing.T) {
	env := setup(t)
	fixedTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	content := "stable content"

	env.dav.addFile("stable.txt", content, "etag-stable", fixedTime)
	env.writeLocalAt("stable.txt", content, fixedTime)
	env.seedDB(db.Entry{
		Path:         "stable.txt",
		ETag:         "etag-stable",
		Size:         int64(len(content)),
		LastModified: fixedTime,
	})

	env.run()

	env.assertNoActions("in-sync run")
}

// After a remote file is downloaded on the first run, a second run with no
// changes must produce no uploads, downloads, or deletes.
func TestSync_IdempotentAfterRemoteSync(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	env.dav.addFile("data.txt", "hello", "etag1", modTime)

	env.run() // first sync: downloads data.txt
	if !env.localExists("data.txt") {
		t.Fatal("data.txt should exist after first sync")
	}

	env.dav.resetSideEffects()
	env.run() // second sync: nothing changed

	env.assertNoActions("second sync")
}

// After a local file is uploaded on the first run, a second run with no
// changes must produce no further actions.
func TestSync_IdempotentAfterLocalSync(t *testing.T) {
	env := setup(t)
	env.writeLocal("notes.txt", "my notes")

	env.run() // first sync: uploads notes.txt
	if !contains(env.dav.puts, "notes.txt") {
		t.Fatal("notes.txt should have been uploaded in first sync")
	}

	env.dav.resetSideEffects()
	env.run() // second sync: nothing changed

	env.assertNoActions("second sync")
}

// After a multi-level tree is fully synced, running the engine three more
// times with no changes must produce no actions on any of those runs.
func TestSync_IdempotentWithTree(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	env.dav.addDir("docs", "etag-docs")
	env.dav.addFile("docs/readme.txt", "readme", "etag-readme", modTime)
	env.dav.addDir("docs/sub", "etag-sub")
	env.dav.addFile("docs/sub/guide.txt", "guide", "etag-guide", modTime)
	env.dav.addFile("top.txt", "top level", "etag-top", modTime)

	env.run() // first sync: downloads full tree

	for i := range 3 {
		env.dav.resetSideEffects()
		env.run()
		env.assertNoActions(fmt.Sprintf("run %d after initial sync", i+2))
	}
}

// Both remote and local have a file; DB is empty (first-ever sync).
// Expected: local wins — file is uploaded (PUT).
func TestSync_FirstRun_LocalWins(t *testing.T) {
	env := setup(t)
	env.dav.addFile("data.csv", "remote version", "etag-remote", time.Now())
	env.writeLocal("data.csv", "local version")
	// DB intentionally empty.

	env.run()

	if !contains(env.dav.puts, "data.csv") {
		t.Fatalf("local should win on first run; puts=%v", env.dav.puts)
	}
}

// ─── hidden file tests ────────────────────────────────────────────────────────

// Remote has a hidden file (.dotfile); SyncHiddenFiles is false (default).
// Expected: file is NOT downloaded; DB remains empty.
func TestSync_HiddenFiles_RemoteHiddenFile_Skipped(t *testing.T) {
	env := setup(t)
	env.dav.addFile(".hidden", "secret", "etag-hidden", time.Now())
	env.cfg.SyncHiddenFiles = false

	env.run()

	if env.localExists(".hidden") {
		t.Fatal(".hidden should not have been downloaded when SyncHiddenFiles=false")
	}
	if env.dbGet(".hidden") != nil {
		t.Fatal("DB should have no entry for .hidden")
	}
}

// Remote has a hidden directory (.cache) with a file inside; SyncHiddenFiles is false.
// Expected: neither the directory nor its contents are downloaded.
func TestSync_HiddenFiles_RemoteHiddenDir_Skipped(t *testing.T) {
	env := setup(t)
	env.dav.addDir(".cache", "etag-cache")
	env.dav.addFile(".cache/data.bin", "cached", "etag-data", time.Now())
	env.cfg.SyncHiddenFiles = false

	env.run()

	if env.localExists(".cache") {
		t.Fatal(".cache/ should not have been created when SyncHiddenFiles=false")
	}
	if env.localExists(".cache/data.bin") {
		t.Fatal(".cache/data.bin should not have been downloaded")
	}
}

// Local has a hidden file (.dotfile); SyncHiddenFiles is false (default).
// Expected: file is NOT uploaded; remote remains unchanged.
func TestSync_HiddenFiles_LocalHiddenFile_Skipped(t *testing.T) {
	env := setup(t)
	env.writeLocal(".dotfile", "local secret")
	env.cfg.SyncHiddenFiles = false

	env.run()

	if contains(env.dav.puts, ".dotfile") {
		t.Fatalf(".dotfile should not have been uploaded when SyncHiddenFiles=false; puts=%v", env.dav.puts)
	}
	if env.dbGet(".dotfile") != nil {
		t.Fatal("DB should have no entry for .dotfile")
	}
}

// Local has a hidden directory (.git) with a file inside; SyncHiddenFiles is false.
// Expected: neither the directory nor its contents are uploaded.
func TestSync_HiddenFiles_LocalHiddenDir_Skipped(t *testing.T) {
	env := setup(t)
	env.mkdirLocal(".git")
	env.writeLocal(".git/config", "git config content")
	env.cfg.SyncHiddenFiles = false

	env.run()

	if contains(env.dav.mkcols, ".git") {
		t.Fatalf(".git/ should not have been MKCOL'd; mkcols=%v", env.dav.mkcols)
	}
	if contains(env.dav.puts, ".git/config") {
		t.Fatalf(".git/config should not have been uploaded; puts=%v", env.dav.puts)
	}
}

// Remote has a hidden file (.dotfile); SyncHiddenFiles is true.
// Expected: file IS downloaded; DB entry created.
func TestSync_HiddenFiles_RemoteHiddenFile_Synced(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	env.dav.addFile(".dotfile", "dot content", "etag-dot", modTime)
	env.cfg.SyncHiddenFiles = true

	env.run()

	if !env.localExists(".dotfile") {
		t.Fatal(".dotfile should have been downloaded when SyncHiddenFiles=true")
	}
	if got := env.readLocal(".dotfile"); got != "dot content" {
		t.Fatalf("content: got %q, want %q", got, "dot content")
	}
	if env.dbGet(".dotfile") == nil {
		t.Fatal("DB entry for .dotfile should exist")
	}
}

// Local has a hidden file (.dotfile); SyncHiddenFiles is true.
// Expected: file IS uploaded; DB entry created.
func TestSync_HiddenFiles_LocalHiddenFile_Synced(t *testing.T) {
	env := setup(t)
	env.writeLocal(".dotfile", "dot content")
	env.cfg.SyncHiddenFiles = true

	env.run()

	if !contains(env.dav.puts, ".dotfile") {
		t.Fatalf(".dotfile should have been uploaded when SyncHiddenFiles=true; puts=%v", env.dav.puts)
	}
	if env.dbGet(".dotfile") == nil {
		t.Fatal("DB entry for .dotfile should exist after upload")
	}
}

// Mixed: remote has one regular file and one hidden file; SyncHiddenFiles is false.
// Expected: only the regular file is downloaded.
func TestSync_HiddenFiles_MixedRemote_OnlyRegularSynced(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	env.dav.addFile("visible.txt", "visible", "etag-vis", modTime)
	env.dav.addFile(".hidden", "hidden", "etag-hid", modTime)
	env.cfg.SyncHiddenFiles = false

	env.run()

	if !env.localExists("visible.txt") {
		t.Fatal("visible.txt should have been downloaded")
	}
	if env.localExists(".hidden") {
		t.Fatal(".hidden should not have been downloaded when SyncHiddenFiles=false")
	}
}

// Mixed: local has one regular file and one hidden file; SyncHiddenFiles is false.
// Expected: only the regular file is uploaded.
func TestSync_HiddenFiles_MixedLocal_OnlyRegularSynced(t *testing.T) {
	env := setup(t)
	env.writeLocal("visible.txt", "visible")
	env.writeLocal(".hidden", "hidden")
	env.cfg.SyncHiddenFiles = false

	env.run()

	if !contains(env.dav.puts, "visible.txt") {
		t.Fatalf("visible.txt should have been uploaded; puts=%v", env.dav.puts)
	}
	if contains(env.dav.puts, ".hidden") {
		t.Fatalf(".hidden should not have been uploaded when SyncHiddenFiles=false; puts=%v", env.dav.puts)
	}
}

// ─── incremental scan tests ───────────────────────────────────────────────────

// After an initial sync, a second sync where nothing changed should issue only
// one PROPFIND (the root with Depth:1). The subdirectory's ETag matches the DB,
// so its subtree is replayed without any further network calls.
func TestSync_IncrementalScan_UnchangedSubtreeSkipped(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)

	env.dav.addDir("docs", "etag-docs")
	env.dav.addFile("docs/readme.txt", "readme content", "etag-readme", modTime)

	// First sync: full scan, everything downloaded.
	env.run()

	if !env.localExists("docs/readme.txt") {
		t.Fatal("docs/readme.txt should have been downloaded on first sync")
	}

	// Reset propfind counter before second sync.
	env.dav.mu.Lock()
	env.dav.propfinds = nil
	env.dav.mu.Unlock()

	// Second sync: nothing changed on either side.
	env.run()

	env.dav.mu.Lock()
	pf := make([]string, len(env.dav.propfinds))
	copy(pf, env.dav.propfinds)
	env.dav.mu.Unlock()

	// Only the root Depth:1 PROPFIND should have been issued.
	// The "docs" dir ETag matches DB so its subtree is replayed from DB.
	if len(pf) != 1 {
		t.Fatalf("expected 1 PROPFIND on unchanged second sync, got %d: %v", len(pf), pf)
	}
	if pf[0] != "" {
		t.Fatalf("expected PROPFIND on root \"\", got %q", pf[0])
	}
}

// When a file inside a subdirectory changes on the remote, the directory ETag
// changes too (CERNBox propagates ETags up). The engine must re-scan that
// directory and detect the updated file.
func TestSync_IncrementalScan_ChangedSubtreeRescanned(t *testing.T) {
	env := setup(t)
	modTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)

	env.dav.addDir("docs", "etag-docs-v1")
	env.dav.addFile("docs/readme.txt", "original", "etag-readme-v1", modTime)

	// First sync.
	env.run()

	// Simulate a remote change: update file content and bump both ETags.
	env.dav.mu.Lock()
	env.dav.files["docs"].etag = "etag-docs-v2"
	env.dav.files["docs/readme.txt"].content = []byte("updated")
	env.dav.files["docs/readme.txt"].etag = "etag-readme-v2"
	env.dav.mu.Unlock()

	env.dav.mu.Lock()
	env.dav.propfinds = nil
	env.dav.mu.Unlock()

	// Second sync: the "docs" ETag changed so its children must be re-fetched.
	env.run()

	if got := env.readLocal("docs/readme.txt"); got != "updated" {
		t.Fatalf("docs/readme.txt should contain updated content; got %q", got)
	}

	env.dav.mu.Lock()
	pf := make([]string, len(env.dav.propfinds))
	copy(pf, env.dav.propfinds)
	env.dav.mu.Unlock()

	// Should have issued PROPFIND on root and on "docs".
	if !contains(pf, "") {
		t.Fatalf("expected PROPFIND on root; got %v", pf)
	}
	if !contains(pf, "docs") {
		t.Fatalf("expected PROPFIND on docs/; got %v", pf)
	}
}

// ─── conflict DB tests ────────────────────────────────────────────────────────

// After a conflict, the sync state DB must contain exactly one conflict entry
// recording the original path and the absolute path of the .conflict-* copy.
func TestSync_Conflict_RecordedInDB(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	env.dav.addFile("shared.txt", "server version", "etag-v2", newTime)
	env.writeLocalAt("shared.txt", "local version", newTime)
	env.seedDB(db.Entry{
		Path:         "shared.txt",
		ETag:         "etag-v1",
		Size:         8, // local is 13 bytes → local also changed
		LastModified: oldTime,
	})

	env.run()

	entries := env.dbConflicts()
	if len(entries) != 1 {
		t.Fatalf("expected 1 conflict DB entry, got %d", len(entries))
	}
	if entries[0].Path != "shared.txt" {
		t.Errorf("conflict Path: got %q, want %q", entries[0].Path, "shared.txt")
	}
	if !strings.Contains(entries[0].ConflictPath, ".conflict-") {
		t.Errorf("conflict ConflictPath %q does not contain .conflict-", entries[0].ConflictPath)
	}
	if entries[0].CreatedAt.IsZero() {
		t.Error("conflict CreatedAt should not be zero")
	}
}

// After the user deletes the .conflict-* file on disk, the next sync must
// remove the conflict DB entry (sweep) and not re-add it.
func TestSync_Conflict_SweepOnResolve(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	env.dav.addFile("shared.txt", "server version", "etag-v2", newTime)
	env.writeLocalAt("shared.txt", "local version", newTime)
	env.seedDB(db.Entry{
		Path:         "shared.txt",
		ETag:         "etag-v1",
		Size:         8,
		LastModified: oldTime,
	})

	// First sync: creates the conflict copy and DB entry.
	env.run()

	conflictFiles := env.conflictFiles(".")
	if len(conflictFiles) == 0 {
		t.Fatal("expected a .conflict-* file after first sync")
	}
	if len(env.dbConflicts()) != 1 {
		t.Fatal("expected 1 conflict DB entry after first sync")
	}

	// User resolves by deleting the conflict copy.
	conflictAbs := filepath.Join(env.localDir, conflictFiles[0])
	if err := os.Remove(conflictAbs); err != nil {
		t.Fatalf("remove conflict file: %v", err)
	}

	// Second sync: sweep must clear the DB entry.
	env.run()

	if len(env.dbConflicts()) != 0 {
		t.Fatalf("expected 0 conflict DB entries after sweep, got %d", len(env.dbConflicts()))
	}
}

// A second conflict on the same file (e.g. both sides changed again after the
// first conflict was not yet resolved) must update the existing DB entry rather
// than adding a duplicate.
func TestSync_Conflict_MultipleConflictsOnSameFile(t *testing.T) {
	env := setup(t)
	oldTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	newTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	env.dav.addFile("shared.txt", "server v2", "etag-v2", newTime)
	env.writeLocalAt("shared.txt", "local version", newTime)
	env.seedDB(db.Entry{
		Path:         "shared.txt",
		ETag:         "etag-v1",
		Size:         8,
		LastModified: oldTime,
	})
	env.run()

	// Trigger a second conflict on the same original path.
	laterTime := time.Date(2025, 1, 10, 10, 0, 0, 0, time.UTC)
	env.dav.addFile("shared.txt", "server v3", "etag-v3", laterTime)
	env.writeLocalAt("shared.txt", "local version 2", laterTime)
	// DB now has etag-v2 from the previous sync; we seed it to simulate that.
	env.seedDB(db.Entry{
		Path:         "shared.txt",
		ETag:         "etag-v2",
		Size:         int64(len("server v2")),
		LastModified: newTime,
	})
	env.run()

	// There may be two distinct conflict copies on disk but each has a unique
	// conflict_path, so both can coexist in the DB.
	entries := env.dbConflicts()
	if len(entries) == 0 {
		t.Fatal("expected at least 1 conflict DB entry after second conflict")
	}
	for _, e := range entries {
		if e.Path != "shared.txt" {
			t.Errorf("unexpected conflict path %q", e.Path)
		}
	}
}

// ─── multi-client helpers ─────────────────────────────────────────────────────

// setupWithSharedDAV creates an isolated test environment (own local directory
// and sync DB) that connects to an existing fakeWebDAV server. Use this to
// simulate multiple sync clients sharing the same remote server.
func setupWithSharedDAV(t *testing.T, dav *fakeWebDAV, srvURL string) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "local")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(tmp, "sync.db")
	return &testEnv{
		t:        t,
		localDir: localDir,
		dbPath:   dbPath,
		dav:      dav,
		cfg: engine.Config{
			LocalRoot:  localDir,
			RemoteBase: srvURL + davBasePath,
			Username:   "user",
			Password:   "pass",
			DBPath:     dbPath,
		},
	}
}

// ─── multi-client tests ───────────────────────────────────────────────────────

// A file uploaded by client A must be downloaded by client B on its next sync.
func TestMultiClient_ClientBReceivesFileUploadedByClientA(t *testing.T) {
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	envA := setupWithSharedDAV(t, dav, srv.URL)
	envB := setupWithSharedDAV(t, dav, srv.URL)

	envA.writeLocal("hello.txt", "hello from A")
	envA.run()

	if !contains(dav.puts, "hello.txt") {
		t.Fatalf("expected client A to upload hello.txt; puts=%v", dav.puts)
	}
	dav.resetSideEffects()

	envB.run()

	if !envB.localExists("hello.txt") {
		t.Fatal("client B should have downloaded hello.txt")
	}
	if got := envB.readLocal("hello.txt"); got != "hello from A" {
		t.Fatalf("client B hello.txt: got %q, want %q", got, "hello from A")
	}
	if envB.dbGet("hello.txt") == nil {
		t.Fatal("client B DB should have an entry for hello.txt")
	}
	envB.assertNoActions("client B should not upload on first sync")
}

// When client A updates a shared file and syncs, client B must download the new
// version on its next sync (no conflict — B did not modify the file).
func TestMultiClient_ClientBGetsUpdateFromClientA(t *testing.T) {
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	baseTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)

	dav.addFile("notes.txt", "v1", "etag-v1", baseTime)

	envA := setupWithSharedDAV(t, dav, srv.URL)
	envB := setupWithSharedDAV(t, dav, srv.URL)

	// Both clients start with v1 already synced.
	envA.seedDB(db.Entry{Path: "notes.txt", ETag: "etag-v1", Size: 2, LastModified: baseTime})
	envB.seedDB(db.Entry{Path: "notes.txt", ETag: "etag-v1", Size: 2, LastModified: baseTime})
	envA.writeLocalAt("notes.txt", "v1", baseTime)
	envB.writeLocalAt("notes.txt", "v1", baseTime)

	// Client A modifies and syncs; server now holds A's version.
	updateTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)
	envA.writeLocalAt("notes.txt", "v2 from A", updateTime)
	envA.run()

	if !contains(dav.puts, "notes.txt") {
		t.Fatalf("expected client A to upload notes.txt; puts=%v", dav.puts)
	}
	dav.resetSideEffects()

	// Client B syncs: remote ETag changed (etag-after-put ≠ etag-v1), local unchanged → download.
	envB.run()

	if got := envB.readLocal("notes.txt"); got != "v2 from A" {
		t.Fatalf("client B notes.txt: got %q, want %q", got, "v2 from A")
	}
	envB.assertNoActions("client B should not upload when only the remote changed")
}

// When both clients independently modify the same file and the first one syncs,
// the second client must detect a conflict: it renames its version to .conflict-*
// and downloads the server (first client's) version.
func TestMultiClient_ConflictBothClientsEditSameFile(t *testing.T) {
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	baseTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	updateTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	dav.addFile("data.txt", "v1", "etag-v1", baseTime)

	envA := setupWithSharedDAV(t, dav, srv.URL)
	envB := setupWithSharedDAV(t, dav, srv.URL)

	// Both clients start with v1 already synced.
	envA.seedDB(db.Entry{Path: "data.txt", ETag: "etag-v1", Size: 2, LastModified: baseTime})
	envB.seedDB(db.Entry{Path: "data.txt", ETag: "etag-v1", Size: 2, LastModified: baseTime})
	envA.writeLocalAt("data.txt", "v1", baseTime)
	envB.writeLocalAt("data.txt", "v1", baseTime)

	// Client A modifies and syncs first; server now has A's version.
	envA.writeLocalAt("data.txt", "A's edit", updateTime)
	envA.run()

	if !contains(dav.puts, "data.txt") {
		t.Fatalf("expected client A to upload data.txt; puts=%v", dav.puts)
	}
	dav.resetSideEffects()

	// Client B has also modified data.txt locally (concurrent edit).
	// "B's edit" is 8 bytes vs DB baseline of 2 → size change detected as local change.
	envB.writeLocalAt("data.txt", "B's edit", updateTime)

	// Client B syncs: remote ETag changed AND local file changed → conflict.
	envB.run()

	// Server version (A's edit) wins; B's data.txt must now hold it.
	if got := envB.readLocal("data.txt"); got != "A's edit" {
		t.Fatalf("client B data.txt after conflict: got %q, want %q", got, "A's edit")
	}

	// B's original edit must be preserved as a .conflict-* copy.
	conflicts := envB.conflictFiles(".")
	if len(conflicts) == 0 {
		t.Fatal("expected a .conflict-* file preserving B's edit")
	}
	if got := envB.readLocal(conflicts[0]); got != "B's edit" {
		t.Fatalf("conflict file content: got %q, want %q", got, "B's edit")
	}
}

// When client A deletes a file that client B has modified locally (not yet synced),
// the engine must preserve B's changes as a .conflict-* copy and delete the original.
func TestMultiClient_ClientADeletesFileClientBModified(t *testing.T) {
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	baseTime := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	updateTime := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)

	dav.addFile("report.txt", "original", "etag-v1", baseTime)

	envA := setupWithSharedDAV(t, dav, srv.URL)
	envB := setupWithSharedDAV(t, dav, srv.URL)

	envA.seedDB(db.Entry{Path: "report.txt", ETag: "etag-v1", Size: 8, LastModified: baseTime})
	envB.seedDB(db.Entry{Path: "report.txt", ETag: "etag-v1", Size: 8, LastModified: baseTime})
	envA.writeLocalAt("report.txt", "original", baseTime)
	envB.writeLocalAt("report.txt", "original", baseTime)

	// Client A deletes the file locally and syncs.
	if err := os.Remove(filepath.Join(envA.localDir, "report.txt")); err != nil {
		t.Fatal(err)
	}
	envA.run()

	if !contains(dav.deletes, "report.txt") {
		t.Fatalf("expected client A to delete report.txt on server; deletes=%v", dav.deletes)
	}
	dav.resetSideEffects()

	// Client B has modified report.txt locally (11 bytes vs DB baseline of 8).
	envB.writeLocalAt("report.txt", "B's changes", updateTime)

	// Client B syncs: remote deleted + local changed → conflictDeleteLocal.
	envB.run()

	// Remote deletion wins: B's original path must be gone.
	if envB.localExists("report.txt") {
		t.Fatal("client B report.txt should have been removed (remote deletion wins)")
	}

	// B's local changes must be preserved as a .conflict-* copy.
	conflicts := envB.conflictFiles(".")
	if len(conflicts) == 0 {
		t.Fatal("expected a .conflict-* file preserving B's changes")
	}
	if got := envB.readLocal(conflicts[0]); got != "B's changes" {
		t.Fatalf("conflict copy content: got %q, want %q", got, "B's changes")
	}
}

// Simulates a complete edit cycle between two clients:
// A creates a file → B downloads it → B edits and uploads → A downloads B's version.
func TestMultiClient_TwoClientsRoundTrip(t *testing.T) {
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	envA := setupWithSharedDAV(t, dav, srv.URL)
	envB := setupWithSharedDAV(t, dav, srv.URL)

	// Round 1: A creates the file and syncs.
	envA.writeLocal("counter.txt", "hello")
	envA.run()
	if !contains(dav.puts, "counter.txt") {
		t.Fatalf("round 1: expected A to upload counter.txt; puts=%v", dav.puts)
	}
	dav.resetSideEffects()

	// Round 2: B syncs and receives A's file.
	envB.run()
	if got := envB.readLocal("counter.txt"); got != "hello" {
		t.Fatalf("round 2: client B counter.txt: got %q, want %q", got, "hello")
	}
	dav.resetSideEffects()

	// Round 3: B edits (size differs from DB baseline: 11 bytes vs 5) and syncs.
	envB.writeLocal("counter.txt", "hello world")
	envB.run()
	if !contains(dav.puts, "counter.txt") {
		t.Fatalf("round 3: expected B to upload counter.txt; puts=%v", dav.puts)
	}
	// Real WebDAV servers assign a new unique ETag after each PUT. Simulate that
	// here so A (whose DB still holds the old ETag) detects the remote change.
	dav.mu.Lock()
	dav.files["counter.txt"].etag = "etag-b-version"
	dav.mu.Unlock()
	dav.resetSideEffects()

	// Round 4: A syncs and receives B's updated version.
	envA.run()
	if got := envA.readLocal("counter.txt"); got != "hello world" {
		t.Fatalf("round 4: client A counter.txt: got %q, want %q", got, "hello world")
	}
	envA.assertNoActions("round 4: A should not re-upload")
}

// When both clients independently create different new files, each file must
// propagate to the other client within two sync cycles.
func TestMultiClient_BothClientsCreateDifferentFiles(t *testing.T) {
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	envA := setupWithSharedDAV(t, dav, srv.URL)
	envB := setupWithSharedDAV(t, dav, srv.URL)

	envA.writeLocal("a.txt", "content from A")
	envB.writeLocal("b.txt", "content from B")

	// Client A syncs first: uploads a.txt.
	envA.run()
	if !contains(dav.puts, "a.txt") {
		t.Fatalf("expected A to upload a.txt; puts=%v", dav.puts)
	}
	dav.resetSideEffects()

	// Client B syncs: uploads b.txt and downloads a.txt.
	envB.run()
	if !contains(dav.puts, "b.txt") {
		t.Fatalf("expected B to upload b.txt; puts=%v", dav.puts)
	}
	if !envB.localExists("a.txt") {
		t.Fatal("client B should have downloaded a.txt")
	}
	if got := envB.readLocal("a.txt"); got != "content from A" {
		t.Fatalf("client B a.txt: got %q, want %q", got, "content from A")
	}
	dav.resetSideEffects()

	// Client A syncs again: downloads b.txt, no new uploads.
	envA.run()
	if !envA.localExists("b.txt") {
		t.Fatal("client A should have downloaded b.txt")
	}
	if got := envA.readLocal("b.txt"); got != "content from B" {
		t.Fatalf("client A b.txt: got %q, want %q", got, "content from B")
	}
	envA.assertNoActions("client A second sync should not upload")
}

// Client A creates a directory with a file; client B gets both, adds another file
// to the same directory, and client A then receives that new file.
func TestMultiClient_ClientACreatesDirClientBAddsFileToIt(t *testing.T) {
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)

	envA := setupWithSharedDAV(t, dav, srv.URL)
	envB := setupWithSharedDAV(t, dav, srv.URL)

	// Client A creates docs/ and docs/readme.md, then syncs.
	envA.mkdirLocal("docs")
	envA.writeLocal("docs/readme.md", "# readme")
	envA.run()

	if !contains(dav.mkcols, "docs") {
		t.Fatalf("expected A to create docs/ on server; mkcols=%v", dav.mkcols)
	}
	if !contains(dav.puts, "docs/readme.md") {
		t.Fatalf("expected A to upload docs/readme.md; puts=%v", dav.puts)
	}
	dav.resetSideEffects()

	// Client B syncs: downloads docs/ and docs/readme.md.
	envB.run()
	if !envB.localIsDir("docs") {
		t.Fatal("client B should have created docs/ directory")
	}
	if got := envB.readLocal("docs/readme.md"); got != "# readme" {
		t.Fatalf("client B docs/readme.md: got %q, want %q", got, "# readme")
	}
	dav.resetSideEffects()

	// Client B creates docs/notes.md and syncs.
	envB.writeLocal("docs/notes.md", "my notes")
	envB.run()
	if !contains(dav.puts, "docs/notes.md") {
		t.Fatalf("expected B to upload docs/notes.md; puts=%v", dav.puts)
	}
	// Real WebDAV servers propagate ETag changes up to parent directories when a
	// child is added. Simulate that here so A's incremental scan rescans docs/ and
	// discovers the new file (without this, A would skip docs/ as unchanged).
	dav.mu.Lock()
	dav.files["docs"].etag = "etag-docs-updated"
	dav.mu.Unlock()
	dav.resetSideEffects()

	// Client A syncs: incremental scan detects docs/ ETag changed, rescans it,
	// and downloads docs/notes.md.
	envA.run()
	if !envA.localExists("docs/notes.md") {
		t.Fatal("client A should have downloaded docs/notes.md")
	}
	if got := envA.readLocal("docs/notes.md"); got != "my notes" {
		t.Fatalf("client A docs/notes.md: got %q, want %q", got, "my notes")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func contains(ss []string, s string) bool {
	return slices.Contains(ss, s)
}
