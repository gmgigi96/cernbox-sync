package daemon

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/ipc"
)

// ── fake WebDAV server (minimal, in-memory) ───────────────────────────────────

const specDavBase = "/dav"

type specEntry struct {
	content []byte
	isDir   bool
	etag    string
}

type specDAV struct {
	mu      sync.Mutex
	entries map[string]*specEntry // relative path, "" is the root
	version int

	// hook, when set, runs before normal handling; returning true means the
	// request was fully handled by the hook.
	hook func(w http.ResponseWriter, r *http.Request, rel string) bool

	gets []string
	puts []string
}

func newSpecDAV() *specDAV {
	return &specDAV{entries: map[string]*specEntry{"": {isDir: true, etag: "root"}}}
}

func (s *specDAV) nextETag() string {
	s.version++
	return fmt.Sprintf("v%d", s.version)
}

func (s *specDAV) addFile(path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// make parents
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		p := strings.Join(parts[:i], "/")
		if _, ok := s.entries[p]; !ok {
			s.entries[p] = &specEntry{isDir: true, etag: s.nextETag()}
		}
	}
	s.entries[path] = &specEntry{content: []byte(content), etag: s.nextETag()}
}

func (s *specDAV) setHook(h func(w http.ResponseWriter, r *http.Request, rel string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = h
}

func (s *specDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimLeft(strings.TrimPrefix(r.URL.Path, specDavBase), "/")
	s.mu.Lock()
	hook := s.hook
	s.mu.Unlock()
	if hook != nil && hook(w, r, rel) {
		return
	}
	switch r.Method {
	case "PROPFIND":
		s.propfind(w, r, rel)
	case "GET":
		s.mu.Lock()
		e, ok := s.entries[rel]
		if ok && !e.isDir {
			s.gets = append(s.gets, rel)
		}
		var body []byte
		if ok {
			body = e.content
		}
		s.mu.Unlock()
		if !ok || e.isDir {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	case "PUT":
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.entries[rel] = &specEntry{content: body, etag: s.nextETag()}
		s.puts = append(s.puts, rel)
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case "MKCOL":
		s.mu.Lock()
		s.entries[rel] = &specEntry{isDir: true, etag: s.nextETag()}
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case "DELETE":
		s.mu.Lock()
		for p := range s.entries {
			if p == rel || strings.HasPrefix(p, rel+"/") {
				delete(s.entries, p)
			}
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *specDAV) propfind(w http.ResponseWriter, r *http.Request, rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[rel]
	if !ok {
		http.NotFound(w, r)
		return
	}
	paths := []string{rel}
	if r.Header.Get("Depth") != "0" && e.isDir {
		prefix := rel
		if prefix != "" {
			prefix += "/"
		}
		for p := range s.entries {
			if p != rel && strings.HasPrefix(p, prefix) && !strings.Contains(p[len(prefix):], "/") {
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	for _, p := range paths {
		x := s.entries[p]
		href := specDavBase
		if p != "" {
			href += "/" + p
		}
		_, _ = fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>"%s"</d:getetag>`, href, x.etag)
		_, _ = fmt.Fprint(w, `<d:getlastmodified>Mon, 01 Jan 2024 00:00:00 GMT</d:getlastmodified>`)
		if x.isDir {
			_, _ = fmt.Fprint(w, `<d:resourcetype><d:collection/></d:resourcetype>`)
		} else {
			_, _ = fmt.Fprintf(w, `<d:resourcetype/><d:getcontentlength>%d</d:getcontentlength>`, len(x.content))
		}
		_, _ = fmt.Fprintf(w, `<oc:fileid>fid-%s</oc:fileid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, strings.ReplaceAll(p, "/", "-"))
	}
	_, _ = fmt.Fprint(w, `</d:multistatus>`)
}

func startSpecDAV(t *testing.T) (*specDAV, string) {
	t.Helper()
	dav := newSpecDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)
	return dav, srv.URL + specDavBase
}

// drainEvents returns all events currently buffered on ch.
func drainEvents(ch chan ipc.Event) []ipc.Event {
	var out []ipc.Event
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ── 13.1 ──────────────────────────────────────────────────────────────────────

// Plan 13.1: changing sync_interval via set-settings resets the running ticker
// without a daemon restart.
func TestSpec_13_1_SetSettingsResetsTicker(t *testing.T) {
	dav, url := startSpecDAV(t)
	dav.addFile("a.txt", "A")
	d := newTestDaemon(t, time.Hour) // starts with a 24h interval
	registerFolder(t, d, "f", t.TempDir(), url, false)

	sock := filepath.Join(t.TempDir(), "d.sock")
	done := make(chan struct{})
	go func() { _ = d.Run(t.Context(), sock); close(done) }()
	t.Cleanup(func() { d.cancel(); <-done })

	if !waitSync(t, d, "f", 5*time.Second) {
		t.Fatal("initial sync did not happen")
	}
	// Let the sync loop reach its select with the 24h ticker.
	time.Sleep(100 * time.Millisecond)

	ch := d.bus.subscribe()
	resp := d.dispatch(ipc.Request{Cmd: ipc.CmdSetSettings, Settings: ipc.SettingsPayload{SyncInterval: "200ms", UploadBandwidth: -1, DownloadBandwidth: -1, TransferStreams: -1, MetadataStreams: -1}})
	if !resp.OK {
		t.Fatalf("set-settings failed: %s", resp.Error)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == ipc.EventSyncCompleted {
				return // a periodic sync ran at the new interval
			}
		case <-deadline:
			t.Fatal("no periodic sync within 3s after setting sync_interval=200ms")
		}
	}
}

// ── 13.5 ──────────────────────────────────────────────────────────────────────

// Plan 13.5: removing a sync pair keeps the local files and directory.
func TestSpec_13_5_RemoveKeepsLocalFiles(t *testing.T) {
	dav, url := startSpecDAV(t)
	dav.addFile("docs/a.txt", "A")
	dav.addFile("b.txt", "B")
	d := newTestDaemon(t, time.Hour)
	local := t.TempDir()
	f := registerFolder(t, d, "f", local, url, false)

	d.syncFolder(f)
	if got := readFile(t, filepath.Join(local, "docs/a.txt")); got != "A" {
		t.Fatalf("setup: sync did not download docs/a.txt (%q)", got)
	}

	resp := d.dispatch(ipc.Request{Cmd: ipc.CmdRemove, Name: "f"})
	if !resp.OK {
		t.Fatalf("remove failed: %s", resp.Error)
	}
	if got := readFile(t, filepath.Join(local, "docs/a.txt")); got != "A" {
		t.Errorf("docs/a.txt changed after remove: %q", got)
	}
	if got := readFile(t, filepath.Join(local, "b.txt")); got != "B" {
		t.Errorf("b.txt changed after remove: %q", got)
	}
	if st, err := os.Stat(local); err != nil || !st.IsDir() {
		t.Errorf("folder directory must stay on disk: %v", err)
	}
	list := d.dispatch(ipc.Request{Cmd: ipc.CmdList})
	for _, x := range list.Folders {
		if x.Name == "f" {
			t.Error("removed pair still listed")
		}
	}
}

// Plan 13.5: a daemon stopped in the middle of a sync is replaced by a new
// one on the same config dir; the next sync completes and the local tree
// matches the server, with no duplicates or conflict copies.
func TestSpec_13_5_RestartDuringSync(t *testing.T) {
	dav, url := startSpecDAV(t)
	want := map[string]string{
		"a.txt":     "alpha",
		"b.txt":     "bravo",
		"c/d.txt":   "delta",
		"c/e/f.txt": "foxtrot",
	}
	for p, c := range want {
		dav.addFile(p, c)
	}

	// Every GET blocks until released (or the client goes away).
	release := make(chan struct{})
	kill := make(chan struct{}) // simulates the process dying: connections drop
	started := make(chan struct{}, 16)
	dav.setHook(func(w http.ResponseWriter, r *http.Request, rel string) bool {
		if r.Method != "GET" {
			return false
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return false
		case <-kill:
			panic(http.ErrAbortHandler)
		case <-r.Context().Done():
			return true
		}
	})

	cfgPath := filepath.Join(t.TempDir(), "config.db")
	local := t.TempDir()

	cfg1, err := config.Open(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d1 := New(cfg1, 24*time.Hour, nil)
	f := config.Folder{Name: "f", LocalRoot: local, RemoteBase: url}
	if err := cfg1.Add(f); err != nil {
		t.Fatal(err)
	}
	syncDone := make(chan struct{})
	go func() { d1.syncFolder(f); close(syncDone) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("sync never reached the download phase")
	}

	// "Kill" the first daemon: abort its in-flight sync.
	d1.mu.Lock()
	for _, cancel := range d1.syncCancels {
		cancel()
	}
	d1.mu.Unlock()
	close(kill)
	select {
	case <-syncDone:
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted sync did not stop")
	}
	_ = cfg1.Close()

	// Second daemon, same config dir; server is now fast.
	dav.setHook(nil)
	close(release)
	cfg2, err := config.Open(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cfg2.Close() }()
	d2 := New(cfg2, 24*time.Hour, nil)
	folders, err := cfg2.All()
	if err != nil || len(folders) != 1 {
		t.Fatalf("pair lost across restart: %v %v", folders, err)
	}
	ch := d2.bus.subscribe()
	d2.syncFolder(folders[0])
	for _, e := range drainEvents(ch) {
		if e.Type == ipc.EventSyncFailed {
			t.Fatalf("post-restart sync failed: %s", e.Error)
		}
	}

	got := map[string]string{}
	_ = filepath.WalkDir(local, func(p string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() || strings.HasPrefix(de.Name(), ".sync") {
			return nil
		}
		rel, _ := filepath.Rel(local, p)
		got[filepath.ToSlash(rel)] = readFile(t, p)
		return nil
	})
	for p, c := range want {
		if got[p] != c {
			t.Errorf("%s = %q, want %q", p, got[p], c)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected local file %s (duplicate/conflict copy?)", p)
		}
	}
}

// ── 1.3 ───────────────────────────────────────────────────────────────────────

// Plan 1.3: a 401 from the server yields a sync-failed event whose error
// identifies an authentication failure (contains "unauthorized"), and nothing
// local is deleted.
func TestSpec_1_3_Unauthorized_PublishesAuthFailure(t *testing.T) {
	dav, url := startSpecDAV(t)
	dav.addFile("remote.txt", "R")
	d := newTestDaemon(t, time.Hour)
	local := t.TempDir()
	f := registerFolder(t, d, "f", local, url, false)

	d.syncFolder(f) // healthy first sync
	if got := readFile(t, filepath.Join(local, "remote.txt")); got != "R" {
		t.Fatalf("setup: remote.txt = %q", got)
	}
	if err := os.WriteFile(filepath.Join(local, "local.txt"), []byte("L"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Credentials expire; the server also "forgets" its content, so a sync
	// that misread the failure as "everything deleted" would wipe the disk.
	dav.setHook(func(w http.ResponseWriter, r *http.Request, rel string) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return true
	})
	ch := d.bus.subscribe()
	d.syncFolder(f)

	var failed *ipc.Event
	for _, e := range drainEvents(ch) {
		if e.Type == ipc.EventSyncCompleted {
			t.Error("sync-completed published for an unauthorized cycle")
		}
		if e.Type == ipc.EventSyncFailed {
			failed = &e
		}
	}
	if failed == nil {
		t.Fatal("no sync-failed event published")
	}
	if !strings.Contains(strings.ToLower(failed.Error), "unauthorized") {
		t.Errorf("sync-failed error %q does not identify an authentication failure (want substring \"unauthorized\")", failed.Error)
	}
	for _, name := range []string{"remote.txt", "local.txt"} {
		if _, err := os.Stat(filepath.Join(local, name)); err != nil {
			t.Errorf("%s must not be deleted on 401: %v", name, err)
		}
	}
}

// ── 12.x ──────────────────────────────────────────────────────────────────────

// Plan 12.1/12.4: when one file fails to upload, the cycle is reported as
// failed (never as a silent sync-completed), naming the file, and the other
// files still sync.
func TestSpec_12_PartialFailure_IsReported(t *testing.T) {
	dav, url := startSpecDAV(t)
	d := newTestDaemon(t, time.Hour)
	local := t.TempDir()
	f := registerFolder(t, d, "f", local, url, false)
	for name, c := range map[string]string{"good.txt": "G", "bad.txt": "B"} {
		if err := os.WriteFile(filepath.Join(local, name), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dav.setHook(func(w http.ResponseWriter, r *http.Request, rel string) bool {
		if r.Method == "PUT" && rel == "bad.txt" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusInsufficientStorage) // 507
			return true
		}
		return false
	})

	ch := d.bus.subscribe()
	d.syncFolder(f)

	var failed *ipc.Event
	for _, e := range drainEvents(ch) {
		if e.Type == ipc.EventSyncCompleted {
			t.Error("sync-completed published although a file failed")
		}
		if e.Type == ipc.EventSyncFailed {
			failed = &e
		}
	}
	if failed == nil {
		t.Fatal("no sync-failed event: the user would not see the failure")
	}
	if !strings.Contains(failed.Error, "bad.txt") {
		t.Errorf("error %q does not name the failing file", failed.Error)
	}
	dav.mu.Lock()
	_, goodUp := dav.entries["good.txt"]
	dav.mu.Unlock()
	if !goodUp {
		t.Error("good.txt should still have been uploaded")
	}
}
