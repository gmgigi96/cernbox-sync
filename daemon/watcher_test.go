package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
)

// ── test helpers ──────────────────────────────────────────────────────────────

// newTestDaemon returns a Daemon wired to a temp config DB.
// debounceDur controls how quickly debounce timers fire; use time.Hour in unit
// tests (timer must not fire) and ~50ms in integration tests.
func newTestDaemon(t *testing.T, debounceDur time.Duration) *Daemon {
	t.Helper()
	tmp := t.TempDir()
	cfgDB, err := config.Open(filepath.Join(tmp, "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cfgDB.Close() })

	d := New(cfgDB, 24*time.Hour, nil) // 24h interval: periodic sync never fires
	d.debounceDuration = debounceDur

	// Stop all pending debounce timers on cleanup so AfterFunc goroutines
	// do not outlive the test.
	t.Cleanup(func() {
		d.debounceMu.Lock()
		for _, timer := range d.debounceTimers {
			timer.Stop()
		}
		d.debounceMu.Unlock()
	})
	return d
}

// registerFolder adds a folder to d's config DB and returns it.
func registerFolder(t *testing.T, d *Daemon, name, localRoot, remoteBase string, autoSync bool) config.Folder {
	t.Helper()
	f := config.Folder{
		Name:       name,
		LocalRoot:  localRoot,
		RemoteBase: remoteBase,
		Settings:   config.FolderSettings{AutoSyncOnChange: autoSync},
	}
	if err := d.cfgDB.Add(f); err != nil {
		t.Fatal(err)
	}
	return f
}

// isWatched reports whether localRoot is currently in the daemon's watchedRoots.
func isWatched(d *Daemon, localRoot string) bool {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	_, ok := d.watchedRoots[localRoot]
	return ok
}

// waitSync polls d.lastSync[name] until it is set or the timeout expires.
// Returns true if a sync was recorded within the deadline.
func waitSync(t *testing.T, d *Daemon, name string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		_, ok := d.lastSync[name]
		d.mu.Unlock()
		if ok {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// minimalWebDAV returns an httptest.Server that responds to WebDAV requests
// with an empty root collection and accepts PUT/MKCOL/DELETE without
// side-effects. This is enough for engine.Run to complete on an empty local dir.
func minimalWebDAV(t *testing.T) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprintf(w,
				`<?xml version="1.0"?>`+
					`<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`+
					`<d:response><d:href>%s</d:href><d:propstat><d:prop>`+
					`<d:getetag>"etag-root"</d:getetag>`+
					`<d:getlastmodified>Mon, 01 Jan 2024 00:00:00 GMT</d:getlastmodified>`+
					`<d:resourcetype><d:collection/></d:resourcetype>`+
					`<oc:fileid>root</oc:fileid>`+
					`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`+
					`</d:multistatus>`,
				r.URL.Path,
			)
		case "PUT":
			io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// ── watch-management unit tests ───────────────────────────────────────────────

// startWatcher should add folders whose AutoSyncOnChange is true to watchedRoots.
func TestWatcher_StartWatcher_AddsEnabledFolders(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	localRoot := t.TempDir()
	registerFolder(t, d, "myspace", localRoot, "http://fake/dav", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	if !isWatched(d, localRoot) {
		t.Fatal("folder with AutoSyncOnChange=true should be in watchedRoots after startWatcher")
	}
}

// Folders with AutoSyncOnChange=false must not be added to watchedRoots.
func TestWatcher_StartWatcher_IgnoresDisabledFolders(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	localRoot := t.TempDir()
	registerFolder(t, d, "myspace", localRoot, "http://fake/dav", false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	if isWatched(d, localRoot) {
		t.Fatal("folder with AutoSyncOnChange=false should not be in watchedRoots")
	}
}

// Calling updateFolderWatch with AutoSyncOnChange=true should start watching.
func TestWatcher_UpdateFolderWatch_Enable(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	localRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	f := config.Folder{
		Name:      "myspace",
		LocalRoot: localRoot,
		Settings:  config.FolderSettings{AutoSyncOnChange: true},
	}
	d.updateFolderWatch(f)

	if !isWatched(d, localRoot) {
		t.Fatal("updateFolderWatch(AutoSyncOnChange=true) should add folder to watchedRoots")
	}
}

// Calling updateFolderWatch with AutoSyncOnChange=false should stop watching.
func TestWatcher_UpdateFolderWatch_Disable(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	localRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	f := config.Folder{
		Name:      "myspace",
		LocalRoot: localRoot,
		Settings:  config.FolderSettings{AutoSyncOnChange: true},
	}
	d.addFolderWatch(f)

	f.Settings.AutoSyncOnChange = false
	d.updateFolderWatch(f)

	if isWatched(d, localRoot) {
		t.Fatal("updateFolderWatch(AutoSyncOnChange=false) should remove folder from watchedRoots")
	}
}

// removeFolderWatch should remove the path from watchedRoots.
func TestWatcher_RemoveFolderWatch_RemovesFromMap(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	localRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	f := config.Folder{Name: "myspace", LocalRoot: localRoot}
	d.addFolderWatch(f)
	d.removeFolderWatch(f.Name, localRoot)

	if isWatched(d, localRoot) {
		t.Fatal("removeFolderWatch should remove the folder from watchedRoots")
	}
}

// removeFolderWatch must cancel any pending debounce timer for that folder.
func TestWatcher_RemoveFolderWatch_CancelsPendingDebounce(t *testing.T) {
	d := newTestDaemon(t, time.Hour) // long debounce: timer must not fire during test
	localRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	f := config.Folder{Name: "myspace", LocalRoot: localRoot}
	d.addFolderWatch(f)
	d.triggerDebouncedSync(f) // plant a pending timer

	d.debounceMu.Lock()
	_, pending := d.debounceTimers[f.Name]
	d.debounceMu.Unlock()
	if !pending {
		t.Fatal("debounce timer should be set before removal")
	}

	d.removeFolderWatch(f.Name, localRoot)

	d.debounceMu.Lock()
	_, pending = d.debounceTimers[f.Name]
	d.debounceMu.Unlock()
	if pending {
		t.Fatal("removeFolderWatch should cancel the pending debounce timer")
	}
}

// ── debounce unit tests ───────────────────────────────────────────────────────

// The first call to triggerDebouncedSync should plant a timer.
func TestWatcher_Debounce_TimerIsSet(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	f := config.Folder{Name: "myspace"}

	d.triggerDebouncedSync(f)

	d.debounceMu.Lock()
	_, ok := d.debounceTimers[f.Name]
	d.debounceMu.Unlock()
	if !ok {
		t.Fatal("triggerDebouncedSync should set a debounce timer")
	}
}

// A second call before the first fires should replace the timer (reset the clock).
func TestWatcher_Debounce_SecondCallResetsTimer(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	f := config.Folder{Name: "myspace"}

	d.triggerDebouncedSync(f)
	d.debounceMu.Lock()
	first := d.debounceTimers[f.Name]
	d.debounceMu.Unlock()

	d.triggerDebouncedSync(f)
	d.debounceMu.Lock()
	second := d.debounceTimers[f.Name]
	d.debounceMu.Unlock()

	if first == second {
		t.Fatal("second triggerDebouncedSync should replace the timer with a new one")
	}
}

// Timers for independent folders must not interfere with each other.
func TestWatcher_Debounce_IndependentPerFolder(t *testing.T) {
	d := newTestDaemon(t, time.Hour)
	fa := config.Folder{Name: "spaceA"}
	fb := config.Folder{Name: "spaceB"}

	d.triggerDebouncedSync(fa)
	d.triggerDebouncedSync(fb)

	d.debounceMu.Lock()
	timerA, okA := d.debounceTimers[fa.Name]
	timerB, okB := d.debounceTimers[fb.Name]
	d.debounceMu.Unlock()

	if !okA || !okB {
		t.Fatal("both folders should have independent debounce timers")
	}
	if timerA == timerB {
		t.Fatal("each folder should have its own timer instance")
	}
}

// ── integration tests ─────────────────────────────────────────────────────────

// Creating a file in a watched folder should trigger a sync after the debounce.
func TestWatcher_FileChange_TriggerSync(t *testing.T) {
	srv := minimalWebDAV(t)
	d := newTestDaemon(t, 50*time.Millisecond)
	localRoot := t.TempDir()
	registerFolder(t, d, "myspace", localRoot, srv.URL+"/dav", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	if err := os.WriteFile(filepath.Join(localRoot, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !waitSync(t, d, "myspace", 5*time.Second) {
		t.Fatal("expected a sync to be triggered by the filesystem event, but none happened within 5s")
	}
}

// Writing to .sync.db must not trigger any sync (avoids engine feedback loops).
func TestWatcher_SyncDB_DoesNotTriggerSync(t *testing.T) {
	d := newTestDaemon(t, 50*time.Millisecond)
	localRoot := t.TempDir()
	// Use an unreachable remote so that if a sync is accidentally triggered it
	// completes quickly (with an error) and is detectable via lastSync.
	registerFolder(t, d, "myspace", localRoot, "http://127.0.0.1:0/dav", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	if err := os.WriteFile(filepath.Join(localRoot, ".sync.db"), []byte("db-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait well beyond the debounce window; a spurious sync would appear here.
	time.Sleep(300 * time.Millisecond)

	d.mu.Lock()
	_, synced := d.lastSync["myspace"]
	d.mu.Unlock()
	if synced {
		t.Fatal(".sync.db changes should not trigger a sync")
	}
}

// A folder with AutoSyncOnChange=false should not be synced on filesystem events.
func TestWatcher_DisabledFolder_NoSync(t *testing.T) {
	d := newTestDaemon(t, 50*time.Millisecond)
	localRoot := t.TempDir()
	registerFolder(t, d, "myspace", localRoot, "http://127.0.0.1:0/dav", false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	if err := os.WriteFile(filepath.Join(localRoot, "data.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	d.mu.Lock()
	_, synced := d.lastSync["myspace"]
	d.mu.Unlock()
	if synced {
		t.Fatal("folder with AutoSyncOnChange=false should not be synced on filesystem events")
	}
}

// A burst of rapid file writes should coalesce into a single sync after the
// debounce window rather than triggering one sync per write.
func TestWatcher_Debounce_BurstCoalesces(t *testing.T) {
	srv := minimalWebDAV(t)
	d := newTestDaemon(t, 100*time.Millisecond)
	localRoot := t.TempDir()
	registerFolder(t, d, "myspace", localRoot, srv.URL+"/dav", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	// Write 10 files in rapid succession — each write should reset the debounce timer.
	for i := range 10 {
		path := filepath.Join(localRoot, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond) // gap smaller than debounce window
	}

	burstEnd := time.Now()

	// Sync should fire once after the burst settles.
	if !waitSync(t, d, "myspace", 5*time.Second) {
		t.Fatal("expected a sync after the burst of writes, but none happened within 5s")
	}

	// The sync must not have fired before the burst was even complete.
	d.mu.Lock()
	syncTime := d.lastSync["myspace"]
	d.mu.Unlock()
	if syncTime.Before(burstEnd) {
		t.Fatal("sync was recorded before the burst was complete — debounce did not coalesce events")
	}
}

// Filesystem events inside a sub-directory of the watched root should also
// trigger a sync for the parent folder.
func TestWatcher_SubdirChange_TriggerSync(t *testing.T) {
	srv := minimalWebDAV(t)
	d := newTestDaemon(t, 50*time.Millisecond)
	localRoot := t.TempDir()

	// Pre-create nested sub-directories so addFolderWatch can pick them up.
	subdir := filepath.Join(localRoot, "docs", "2024")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	registerFolder(t, d, "myspace", localRoot, srv.URL+"/dav", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startWatcher(ctx)

	if err := os.WriteFile(filepath.Join(subdir, "report.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !waitSync(t, d, "myspace", 5*time.Second) {
		t.Fatal("a change in a nested sub-directory should trigger a sync for the root folder")
	}
}
