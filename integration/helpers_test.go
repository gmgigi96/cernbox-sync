//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/webdav"
)

const (
	webdavBase   = "http://localhost/remote.php/webdav/eos/user/e/einstein"
	webdavUser   = "einstein"
	webdavPass   = "relativity"
	folderName   = "test"
	syncInterval = "24h" // disable periodic syncs; tests drive sync explicitly
)

var (
	cliPath    string // path to the built cernbox-sync binary
	daemonPath string // path to the built cernbox-syncd binary
)

// TestMain checks that the dev environment is reachable, builds both binaries,
// and runs all integration tests.
func TestMain(m *testing.M) {
	if !webdavReachable() {
		fmt.Fprintln(os.Stderr, "===> dev environment not reachable — start it with 'make dev-up'")
		os.Exit(1)
	}

	buildDir, err := os.MkdirTemp("", "cernbox-integration-bins-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MkdirTemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(buildDir)

	cliPath = filepath.Join(buildDir, "cernbox-sync")
	daemonPath = filepath.Join(buildDir, "cernbox-syncd")

	root := repoRoot()
	if err := goBuild(root, ".", cliPath); err != nil {
		fmt.Fprintf(os.Stderr, "build cernbox-sync: %v\n", err)
		os.Exit(1)
	}
	if err := goBuild(root, "./cmd/cernbox-syncd", daemonPath); err != nil {
		fmt.Fprintf(os.Stderr, "build cernbox-syncd: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func webdavReachable() bool {
	c := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("PROPFIND", webdavBase+"/", nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth(webdavUser, webdavPass)
	req.Header.Set("Depth", "0")
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusMultiStatus
}

// repoRoot returns the repository root. Tests run with cwd set to the
// integration/ package directory, so the root is one level up.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Dir(dir)
}

func goBuild(repoRoot, pkg, out string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = repoRoot
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, b)
	}
	return nil
}

// ── testEnv ──────────────────────────────────────────────────────────────────

// testEnv manages one isolated daemon instance, one local sync root, and one
// remote subdirectory for the duration of a single test.
type testEnv struct {
	t          *testing.T
	tmpDir     string
	localDir   string         // local sync root
	remoteID   string         // unique remote subdir name, e.g. "it-deadbeef01234567"
	sockPath   string         // Unix socket used to talk to this test's daemon
	daemonCmd  *exec.Cmd      // running cernbox-syncd process
	rootClient *webdav.Client // client rooted at webdavBase (for mkdir/delete of the test subdir)
	testClient *webdav.Client // client rooted at webdavBase/remoteID (for per-file operations)
}

// setup starts an isolated daemon, creates a remote subdirectory, and
// registers the sync pair. Cleanup (daemon stop + remote dir removal) is
// registered with t.Cleanup.
func setup(t *testing.T) *testEnv {
	t.Helper()

	tmpDir := t.TempDir()
	localDir := filepath.Join(tmpDir, "local")
	runDir := filepath.Join(tmpDir, "run")
	configDir := filepath.Join(tmpDir, "config")
	for _, d := range []string{localDir, runDir, configDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("setup mkdir %s: %v", d, err)
		}
	}

	remoteID := "it-" + randHex()
	sockPath := filepath.Join(runDir, "cernbox-sync.sock")

	rootClient := webdav.NewClient(webdavBase, webdavUser, webdavPass)
	testClient := webdav.NewClient(webdavBase+"/"+remoteID, webdavUser, webdavPass)

	// Create the per-test remote subdirectory.
	if err := rootClient.Mkcol(remoteID); err != nil {
		t.Fatalf("create remote dir %s: %v", remoteID, err)
	}

	// Capture daemon output to a file; dump it on failure.
	logFile, err := os.CreateTemp(tmpDir, "daemon-*.log")
	if err != nil {
		t.Fatalf("create daemon log: %v", err)
	}

	env := &testEnv{
		t:          t,
		tmpDir:     tmpDir,
		localDir:   localDir,
		remoteID:   remoteID,
		sockPath:   sockPath,
		rootClient: rootClient,
		testClient: testClient,
	}

	// Start cernbox-syncd with:
	//   -socket   → isolated socket path
	//   -interval → 24 h to prevent background syncs during the test
	// XDG_CONFIG_HOME → isolated config DB so no real user config is touched.
	env.daemonCmd = exec.Command(daemonPath,
		"-interval", syncInterval,
		"-socket", sockPath,
	)
	env.daemonCmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+runDir,
		"XDG_CONFIG_HOME="+configDir,
	)
	env.daemonCmd.Stdout = logFile
	env.daemonCmd.Stderr = logFile

	if err := env.daemonCmd.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start daemon: %v", err)
	}

	t.Cleanup(func() {
		env.stopDaemon()

		// On failure, print daemon log to help diagnose the problem.
		if t.Failed() {
			if _, err := logFile.Seek(0, io.SeekStart); err == nil {
				scanner := bufio.NewScanner(logFile)
				for scanner.Scan() {
					t.Logf("[daemon] %s", scanner.Text())
				}
			}
		}
		logFile.Close()

		rootClient.Delete(remoteID) // best-effort; ignore error
	})

	env.waitDaemonReady()

	// Set the account credentials before registering the sync pair.
	if _, err := ipc.Send(sockPath, ipc.Request{
		Cmd: ipc.CmdSetAccount,
		Account: &ipc.AccountPayload{
			Username: webdavUser,
			Password: webdavPass,
		},
	}); err != nil {
		t.Fatalf("set-account: %v", err)
	}

	// Register the sync pair via the CLI binary.
	env.cli("add",
		"-name", folderName,
		"-local", localDir,
		"-remote", webdavBase+"/"+remoteID,
	)

	return env
}

// waitDaemonReady polls until the Unix socket accepts connections.
func (e *testEnv) waitDaemonReady() {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", e.sockPath)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatal("daemon did not become ready within 15 s")
}

// stopDaemon sends a stop command and waits for the process to exit.
func (e *testEnv) stopDaemon() {
	if e.daemonCmd == nil || e.daemonCmd.Process == nil {
		return
	}
	ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdStop}) // best-effort

	done := make(chan struct{})
	go func() {
		e.daemonCmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		e.daemonCmd.Process.Kill()
		<-done
	}
}

// cli runs cernbox-sync with the given arguments, routing it to this test's
// daemon socket via XDG_RUNTIME_DIR. Fails the test on any error.
func (e *testEnv) cli(args ...string) {
	e.t.Helper()
	cmd := exec.Command(cliPath, args...)
	// XDG_RUNTIME_DIR points to the run dir so ipc.SocketPath() resolves to
	// our isolated socket (<runDir>/cernbox-sync.sock).
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+filepath.Dir(e.sockPath))
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("cli %v: %v\n%s", args, err, bytes.TrimSpace(out))
	}
}

// triggerSync sends a sync command and blocks until the cycle completes.
// It polls the daemon's status via IPC until the folder is no longer in the
// Syncing list and its LastSync timestamp is at or after the trigger time.
func (e *testEnv) triggerSync() {
	e.t.Helper()
	// RFC3339 truncates to whole seconds; truncate triggerTime to the same
	// precision so a same-second completion is not missed.
	triggerTime := time.Now().Truncate(time.Second)

	e.cli("run", "-name", folderName)

	// Brief pause so the sync goroutine has time to start and mark itself in
	// the Syncing list before our first poll. Without this, a very fast sync
	// can complete before we even check, making sawSyncing always false and
	// forcing us to rely on the timestamp fallback alone.
	time.Sleep(100 * time.Millisecond)

	deadline := time.Now().Add(60 * time.Second)
	sawSyncing := false // becomes true the first time we observe the folder syncing
	failCount := 0
	for time.Now().Before(deadline) {
		resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdStatus})
		if err != nil {
			failCount++
			if failCount >= 5 {
				e.t.Fatalf("triggerSync: daemon appears to be dead: %v", err)
			}
			time.Sleep(300 * time.Millisecond)
			continue
		}
		failCount = 0

		if sliceContains(resp.Status.Syncing, folderName) {
			sawSyncing = true
		} else {
			if sawSyncing {
				// We observed the sync in-flight; now it is gone → done.
				return
			}
			// Sync either finished before our first poll or hasn't started yet.
			// Fall back to comparing the lastSync timestamp.
			if lastStr, ok := resp.Status.LastSync[folderName]; ok {
				if last, err := time.Parse(time.RFC3339, lastStr); err == nil && !last.Before(triggerTime) {
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatal("triggerSync: timed out waiting for sync to complete")
}

// ── local filesystem helpers ──────────────────────────────────────────────────

func (e *testEnv) writeLocal(relPath, content string) {
	e.t.Helper()
	full := filepath.Join(e.localDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		e.t.Fatalf("writeLocal %s: mkdir: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		e.t.Fatalf("writeLocal %s: %v", relPath, err)
	}
}

// writeLocalAt writes a file and then sets its mtime to t.
// Use this when the test needs the engine's mtime-based change detection to
// fire reliably: isLocalChanged requires modTime > DB.LastModified + 1s.
func (e *testEnv) writeLocalAt(relPath, content string, t time.Time) {
	e.t.Helper()
	e.writeLocal(relPath, content)
	full := filepath.Join(e.localDir, relPath)
	if err := os.Chtimes(full, t, t); err != nil {
		e.t.Fatalf("writeLocalAt chtimes %s: %v", relPath, err)
	}
}

func (e *testEnv) readLocal(relPath string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.localDir, relPath))
	if err != nil {
		e.t.Fatalf("readLocal %s: %v", relPath, err)
	}
	return string(b)
}

func (e *testEnv) mkdirLocal(relPath string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Join(e.localDir, relPath), 0o755); err != nil {
		e.t.Fatalf("mkdirLocal %s: %v", relPath, err)
	}
}

func (e *testEnv) deleteLocal(relPath string) {
	e.t.Helper()
	if err := os.Remove(filepath.Join(e.localDir, relPath)); err != nil {
		e.t.Fatalf("deleteLocal %s: %v", relPath, err)
	}
}

func (e *testEnv) localExists(relPath string) bool {
	_, err := os.Lstat(filepath.Join(e.localDir, relPath))
	return err == nil
}

// ── remote WebDAV helpers ─────────────────────────────────────────────────────

func (e *testEnv) writeRemote(relPath, content string) {
	e.t.Helper()
	b := []byte(content)
	if err := e.testClient.Put(relPath, bytes.NewReader(b), int64(len(b))); err != nil {
		e.t.Fatalf("writeRemote %s: %v", relPath, err)
	}
}

func (e *testEnv) readRemote(relPath string) string {
	e.t.Helper()
	rc, err := e.testClient.Get(relPath)
	if err != nil {
		e.t.Fatalf("readRemote %s: %v", relPath, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		e.t.Fatalf("readRemote read %s: %v", relPath, err)
	}
	return string(b)
}

func (e *testEnv) mkdirRemote(relPath string) {
	e.t.Helper()
	if err := e.testClient.Mkcol(relPath); err != nil {
		e.t.Fatalf("mkdirRemote %s: %v", relPath, err)
	}
}

func (e *testEnv) deleteRemote(relPath string) {
	e.t.Helper()
	if err := e.testClient.Delete(relPath); err != nil {
		e.t.Fatalf("deleteRemote %s: %v", relPath, err)
	}
}

// ── assertInSync ─────────────────────────────────────────────────────────────

// assertInSync asserts that the local sync root and the remote test directory
// contain exactly the same files with the same content. Internal files
// (.sync.db, .sync.log) and conflict copies are excluded from the comparison.
func (e *testEnv) assertInSync() {
	e.t.Helper()

	local := e.collectLocal()
	remote := e.collectRemote()

	for path, lh := range local {
		rh, ok := remote[path]
		if !ok {
			e.t.Errorf("assertInSync: %q exists locally but not on remote", path)
			continue
		}
		if lh != rh {
			e.t.Errorf("assertInSync: content mismatch for %q (local≠remote)", path)
		}
	}
	for path := range remote {
		if _, ok := local[path]; !ok {
			e.t.Errorf("assertInSync: %q exists on remote but not locally", path)
		}
	}
}

// collectLocal walks the local directory and returns a map of
// relative path → SHA-256 hex (files) or "dir" (directories).
// Internal state files and conflict copies are skipped.
func (e *testEnv) collectLocal() map[string]string {
	out := make(map[string]string)
	err := filepath.WalkDir(e.localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.localDir, path)
		if rel == "." {
			return nil
		}
		base := filepath.Base(path)
		if base == ".sync.db" || base == ".sync.log" {
			return nil
		}
		// Conflict copies are expected in conflict tests but are not part of
		// the canonical synced state.
		if strings.Contains(base, ".conflict-") {
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = "dir"
		} else {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[rel] = hashBytes(content)
		}
		return nil
	})
	if err != nil {
		e.t.Fatalf("collectLocal: %v", err)
	}
	return out
}

// collectRemote BFS-traverses the remote test directory and returns a map of
// relative path → SHA-256 hex (files) or "dir" (directories).
func (e *testEnv) collectRemote() map[string]string {
	out := make(map[string]string)
	queue := []string{""}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		resources, err := e.testClient.Propfind(current, 1)
		if err != nil {
			e.t.Fatalf("collectRemote propfind %q: %v", current, err)
		}
		for _, res := range resources {
			// Depth:1 includes the directory itself; skip it.
			// Strip any trailing slash from res.Path before comparisons —
			// WebDAV servers return directory hrefs with a trailing slash,
			// which parseResponse preserves, so normalise here.
			p := strings.TrimRight(res.Path, "/")
			if p == current {
				continue
			}
			if res.IsDir {
				out[p+"/"] = "dir"
				queue = append(queue, p)
			} else {
				rc, err := e.testClient.Get(p)
				if err != nil {
					e.t.Fatalf("collectRemote get %q: %v", p, err)
				}
				content, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					e.t.Fatalf("collectRemote read %q: %v", p, err)
				}
				out[p] = hashBytes(content)
			}
		}
	}
	return out
}

// pause sends a pause command for the given folder name (or globally if empty).
func (e *testEnv) pause(name string) {
	e.t.Helper()
	resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdPause, Name: name})
	if err != nil {
		e.t.Fatalf("pause %q: %v", name, err)
	}
	if !resp.OK {
		e.t.Fatalf("pause %q: daemon error: %s", name, resp.Error)
	}
}

// resume sends a resume command for the given folder name (or globally if empty).
func (e *testEnv) resume(name string) {
	e.t.Helper()
	resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdResume, Name: name})
	if err != nil {
		e.t.Fatalf("resume %q: %v", name, err)
	}
	if !resp.OK {
		e.t.Fatalf("resume %q: daemon error: %s", name, resp.Error)
	}
}

// isPaused returns true if the named folder is listed in the daemon's paused folders.
func (e *testEnv) isPaused(name string) bool {
	e.t.Helper()
	resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdStatus})
	if err != nil {
		e.t.Fatalf("status: %v", err)
	}
	return sliceContains(resp.Status.PausedFolders, name)
}

// isGloballyPaused returns true when the daemon reports global pause.
func (e *testEnv) isGloballyPaused() bool {
	e.t.Helper()
	resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdStatus})
	if err != nil {
		e.t.Fatalf("status: %v", err)
	}
	return resp.Status.GlobalPaused
}

// trySync sends a sync command and returns the daemon's response without
// failing the test. Used when the caller expects the command to be rejected.
func (e *testEnv) trySync(name string) *ipc.Response {
	e.t.Helper()
	resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdSync, Name: name})
	if err != nil {
		e.t.Fatalf("trySync %q: %v", name, err)
	}
	return resp
}

// listConflicts sends list-conflicts to the daemon and returns the entries.
// If name is non-empty only conflicts for that folder are returned.
func (e *testEnv) listConflicts(name string) []ipc.ConflictEntry {
	e.t.Helper()
	resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdListConflicts, Name: name})
	if err != nil {
		e.t.Fatalf("list-conflicts: %v", err)
	}
	if !resp.OK {
		e.t.Fatalf("list-conflicts: daemon error: %s", resp.Error)
	}
	return resp.Conflicts
}

// conflictFiles returns the names (not full paths) of .conflict-* files in the
// local sync root, or a subdirectory when subdir is non-empty.
func (e *testEnv) conflictFiles(subdir string) []string {
	e.t.Helper()
	dir := e.localDir
	if subdir != "" {
		dir = filepath.Join(dir, subdir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		e.t.Fatalf("conflictFiles ReadDir %s: %v", dir, err)
	}
	var out []string
	for _, de := range entries {
		if strings.Contains(de.Name(), ".conflict-") {
			out = append(out, de.Name())
		}
	}
	return out
}

// ── misc helpers ──────────────────────────────────────────────────────────────

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func randHex() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func sliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
