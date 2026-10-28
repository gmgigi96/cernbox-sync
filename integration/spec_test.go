//go:build integration

package integration_test

// Integration tests for the "Sync client tests against reva" plan
// (see docs/reva-test-coverage.md). Each test is annotated with its plan ID.
//
// Dev-environment facts these tests rely on (probed against the real stack):
//   - sharing: OCS share creation fails ("ShareManager is not online"), so all
//     share-dependent checks skip cleanly.
//   - trash-bin: PROPFIND /remote.php/dav/trash-bin/<user>/ answers 207 but
//     lists nothing, so restore-from-trash skips.
//   - versions: PROPFIND /remote.php/dav/meta/<fileid>/v lists no versions, so
//     restore-version skips.
//   - EOS refuses names containing a newline (HTTP 500) while Linux allows
//     them, which is used to test that one rejected file does not block others.
//
// Large scenarios (2 GB / 10 GB / 10,000 files) run only with CS_IT_LARGE=1.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/ipc"
)

// ── raw WebDAV helper (independent of the client under test) ─────────────────

const davRoot = "http://localhost/remote.php/dav"

type rawDAV struct {
	base, user, pass string
}

// rdav returns a raw WebDAV helper rooted at the test's remote directory.
func (e *testEnv) rdav() rawDAV {
	return rawDAV{base: webdavBase + "/" + e.remoteID, user: webdavUser, pass: webdavPass}
}

func escPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func (d rawDAV) url(p string) string {
	if p == "" {
		return d.base + "/"
	}
	return d.base + "/" + escPath(p)
}

func (d rawDAV) do(method, rawURL string, body io.Reader, size int64, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	req.SetBasicAuth(d.user, d.pass)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

// status performs a request and returns the status code (body discarded).
func (d rawDAV) status(method, p string, body []byte, hdr map[string]string) (int, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	resp, err := d.do(method, d.url(p), r, int64(len(body)), hdr)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}

func okStatus(c int) bool { return c >= 200 && c < 300 }

func (d rawDAV) put(t *testing.T, p string, data []byte) {
	t.Helper()
	c, err := d.status("PUT", p, data, nil)
	if err != nil || !okStatus(c) {
		t.Fatalf("raw PUT %q: status %d err %v", p, c, err)
	}
}

func (d rawDAV) putFile(t *testing.T, p, local string) {
	t.Helper()
	f, err := os.Open(local)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	resp, err := d.do("PUT", d.url(p), f, st.Size(), nil)
	if err != nil {
		t.Fatalf("raw PUT file %q: %v", p, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if !okStatus(resp.StatusCode) {
		t.Fatalf("raw PUT file %q: status %d", p, resp.StatusCode)
	}
}

func (d rawDAV) get(t *testing.T, p string) []byte {
	t.Helper()
	resp, err := d.do("GET", d.url(p), nil, 0, nil)
	if err != nil {
		t.Fatalf("raw GET %q: %v", p, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("raw GET %q: status %d", p, resp.StatusCode)
	}
	return b
}

// hashOf streams a remote file and returns its sha256 and size.
func (d rawDAV) hashOf(t *testing.T, p string) (string, int64) {
	t.Helper()
	resp, err := d.do("GET", d.url(p), nil, 0, nil)
	if err != nil {
		t.Fatalf("raw GET %q: %v", p, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("raw GET %q: status %d", p, resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(h, resp.Body)
	if err != nil {
		t.Fatalf("raw GET %q body: %v", p, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n
}

func (d rawDAV) mkcol(t *testing.T, p string) {
	t.Helper()
	c, err := d.status("MKCOL", p, nil, nil)
	if err != nil || (!okStatus(c) && c != 405) { // 405 = already exists
		t.Fatalf("raw MKCOL %q: status %d err %v", p, c, err)
	}
}

// mkcolAll creates p and all its parents.
func (d rawDAV) mkcolAll(t *testing.T, p string) {
	t.Helper()
	parts := strings.Split(p, "/")
	for i := range parts {
		d.mkcol(t, strings.Join(parts[:i+1], "/"))
	}
}

func (d rawDAV) del(t *testing.T, p string) {
	t.Helper()
	c, err := d.status("DELETE", p, nil, nil)
	if err != nil || !okStatus(c) {
		t.Fatalf("raw DELETE %q: status %d err %v", p, c, err)
	}
}

func (d rawDAV) move(t *testing.T, src, dst string) {
	t.Helper()
	c, err := d.status("MOVE", src, nil, map[string]string{
		"Destination": d.url(dst),
		"Overwrite":   "F",
	})
	if err != nil || !okStatus(c) {
		t.Fatalf("raw MOVE %q -> %q: status %d err %v", src, dst, c, err)
	}
}

func (d rawDAV) exists(p string) bool {
	_, ok := d.stat(p)
	return ok
}

type davEntry struct {
	Path    string // decoded, relative to the base, no trailing slash
	IsDir   bool
	ETag    string
	FileID  string
	Size    int64
	Perms   string
	ModTime string
}

type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Status string `xml:"status"`
			Prop   struct {
				ETag   string `xml:"getetag"`
				FileID string `xml:"fileid"`
				Perms  string `xml:"permissions"`
				Len    string `xml:"getcontentlength"`
				Size   string `xml:"oc:size"`
				Mod    string `xml:"getlastmodified"`
				RT     struct {
					Coll *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func (d rawDAV) basePath() string {
	u, _ := url.Parse(d.base)
	return u.Path
}

// propfindURL runs a PROPFIND on an absolute URL and parses the result;
// hrefs are decoded and made relative to prefix.
func (d rawDAV) propfindURL(rawURL string, depth int, prefix string) ([]davEntry, int, error) {
	resp, err := d.do("PROPFIND", rawURL, nil, 0, map[string]string{"Depth": strconv.Itoa(depth)})
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 207 {
		return nil, resp.StatusCode, nil
	}
	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse multistatus: %w", err)
	}
	var out []davEntry
	for _, r := range ms.Responses {
		href, err := url.PathUnescape(r.Href)
		if err != nil {
			href = r.Href
		}
		e := davEntry{Path: strings.Trim(strings.TrimPrefix(href, prefix), "/")}
		for _, ps := range r.Propstat {
			if !strings.Contains(ps.Status, " 200") {
				continue
			}
			p := ps.Prop
			if p.ETag != "" {
				e.ETag = p.ETag
			}
			if p.FileID != "" {
				e.FileID = p.FileID
			}
			if p.Perms != "" {
				e.Perms = p.Perms
			}
			if p.Mod != "" {
				e.ModTime = p.Mod
			}
			if p.RT.Coll != nil {
				e.IsDir = true
			}
			if p.Len != "" {
				e.Size, _ = strconv.ParseInt(p.Len, 10, 64)
			}
		}
		out = append(out, e)
	}
	return out, resp.StatusCode, nil
}

func (d rawDAV) stat(p string) (davEntry, bool) {
	es, code, err := d.propfindURL(d.url(p), 0, d.basePath())
	if err != nil || code != 207 || len(es) == 0 {
		return davEntry{}, false
	}
	return es[0], true
}

func (d rawDAV) fileID(t *testing.T, p string) string {
	t.Helper()
	e, ok := d.stat(p)
	if !ok || e.FileID == "" {
		t.Fatalf("remote %q: no fileid (exists=%v)", p, ok)
	}
	return e.FileID
}

// list returns the direct children of dir.
func (d rawDAV) list(t *testing.T, dir string) []davEntry {
	t.Helper()
	es, code, err := d.propfindURL(d.url(dir), 1, d.basePath())
	if err != nil || code != 207 {
		t.Fatalf("raw PROPFIND %q: code %d err %v", dir, code, err)
	}
	var out []davEntry
	for _, e := range es {
		if e.Path == strings.Trim(dir, "/") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// tree returns every resource below the base keyed by path (dirs have a
// trailing slash).
func (d rawDAV) tree(t *testing.T) map[string]davEntry {
	t.Helper()
	out := map[string]davEntry{}
	queue := []string{""}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range d.list(t, cur) {
			if e.IsDir {
				out[e.Path+"/"] = e
				queue = append(queue, e.Path)
			} else {
				out[e.Path] = e
			}
		}
	}
	return out
}

// ── environment helpers ──────────────────────────────────────────────────────

// syncWait triggers a sync of the test folder and waits (up to timeout) for
// the cycle to finish; a failed cycle also counts as finished.
func syncWait(t *testing.T, e *testEnv, timeout time.Duration) {
	t.Helper()
	trigger := time.Now().Truncate(time.Second)
	if resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdSync, Name: folderName}); err != nil {
		t.Fatalf("sync: %v", err)
	} else if !resp.OK {
		t.Fatalf("sync: daemon error: %s", resp.Error)
	}
	time.Sleep(100 * time.Millisecond)
	deadline := time.Now().Add(timeout)
	saw := false
	fails := 0
	for time.Now().Before(deadline) {
		resp, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdStatus})
		if err != nil {
			if fails++; fails >= 5 {
				t.Fatalf("syncWait: daemon appears dead: %v", err)
			}
			time.Sleep(300 * time.Millisecond)
			continue
		}
		fails = 0
		if sliceContains(resp.Status.Syncing, folderName) {
			saw = true
		} else if saw {
			return
		} else if s, ok := resp.Status.LastSync[folderName]; ok {
			if last, err := time.Parse(time.RFC3339, s); err == nil && !last.Before(trigger) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("syncWait: sync did not finish within %v", timeout)
}

func quick(t *testing.T, e *testEnv) { t.Helper(); syncWait(t, e, 5*time.Minute) }

// setSettings changes bandwidth/stream settings, keeping the 24h interval.
// Pass -1 to leave a numeric value unchanged.
func setSettings(t *testing.T, e *testEnv, up, down int64, transfer, meta int) {
	t.Helper()
	resp, err := ipc.Send(e.sockPath, ipc.Request{
		Cmd: ipc.CmdSetSettings,
		Settings: ipc.SettingsPayload{
			SyncInterval:      syncInterval,
			UploadBandwidth:   up,
			DownloadBandwidth: down,
			TransferStreams:   transfer,
			MetadataStreams:   meta,
		},
	})
	if err != nil || !resp.OK {
		t.Fatalf("set-settings: err %v resp %+v", err, resp)
	}
}

func daemonAlive(e *testEnv) bool {
	_, err := ipc.Send(e.sockPath, ipc.Request{Cmd: ipc.CmdStatus})
	return err == nil
}

// startDaemonAt launches a daemon on the env's socket/config dirs.
func startDaemonAt(t *testing.T, e *testEnv) (*exec.Cmd, *os.File) {
	t.Helper()
	runDir := filepath.Dir(e.sockPath)
	configDir := filepath.Join(e.tmpDir, "config")
	logFile, err := os.CreateTemp(e.tmpDir, "daemon-restart-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(daemonPath, "-interval", syncInterval, "-socket", e.sockPath)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runDir, "XDG_CONFIG_HOME="+configDir)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	return cmd, logFile
}

// setupAs is like setup but for another user and an arbitrary remote URL.
// The remote must already exist.
func setupAs(t *testing.T, user, pass, remoteURL string) *testEnv {
	t.Helper()
	tmpDir := t.TempDir()
	localDir := filepath.Join(tmpDir, "local")
	runDir := filepath.Join(tmpDir, "run")
	configDir := filepath.Join(tmpDir, "config")
	for _, d := range []string{localDir, runDir, configDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := &testEnv{t: t, tmpDir: tmpDir, localDir: localDir, remoteID: "", sockPath: filepath.Join(runDir, "cernbox-sync.sock")}
	logFile, err := os.CreateTemp(tmpDir, "daemon-*.log")
	if err != nil {
		t.Fatal(err)
	}
	env.daemonCmd = exec.Command(daemonPath, "-interval", syncInterval, "-socket", env.sockPath)
	env.daemonCmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runDir, "XDG_CONFIG_HOME="+configDir)
	env.daemonCmd.Stdout, env.daemonCmd.Stderr = logFile, logFile
	if err := env.daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		env.stopDaemon()
		if t.Failed() {
			logFile.Seek(0, io.SeekStart)
			sc := bufio.NewScanner(logFile)
			for sc.Scan() {
				t.Logf("[daemon %s] %s", user, sc.Text())
			}
		}
		logFile.Close()
	})
	env.waitDaemonReady()
	if resp, err := ipc.Send(env.sockPath, ipc.Request{Cmd: ipc.CmdSetAccount, Account: &ipc.AccountPayload{Username: user, Password: pass}}); err != nil || !resp.OK {
		t.Fatalf("set-account: %v %+v", err, resp)
	}
	env.cli("add", "-name", folderName, "-local", localDir, "-remote", remoteURL)
	return env
}

// eventLog collects events pushed by a subscription.
type eventLog struct {
	mu   sync.Mutex
	evs  []ipc.Event
	conn net.Conn
}

func subscribeEvents(t *testing.T, e *testEnv) *eventLog {
	t.Helper()
	conn, err := net.Dial("unix", e.sockPath)
	if err != nil {
		t.Fatalf("subscribe dial: %v", err)
	}
	if _, err := conn.Write([]byte(`{"cmd":"subscribe"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(conn)
	var first ipc.SubscribeResponse
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("subscribe handshake: %v", err)
	}
	l := &eventLog{conn: conn}
	t.Cleanup(func() { conn.Close() })
	go func() {
		for {
			var ev ipc.Event
			if err := dec.Decode(&ev); err != nil {
				return
			}
			l.mu.Lock()
			l.evs = append(l.evs, ev)
			l.mu.Unlock()
		}
	}()
	return l
}

// failures returns the Error text of every sync-failed event seen so far.
func (l *eventLog) failures() []string {
	time.Sleep(300 * time.Millisecond) // let the last event arrive
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, ev := range l.evs {
		if ev.Type == ipc.EventSyncFailed {
			out = append(out, ev.Error)
		}
	}
	return out
}

// localFiles lists regular files and dirs below the sync root (skipping
// internal files), as relative paths (dirs get a trailing slash) -> size.
func localTree(t *testing.T, e *testEnv) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(e.localDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.localDir, p)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(filepath.Base(p), ".sync.") {
			return nil
		}
		if d.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = 0
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = st.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("localTree: %v", err)
	}
	return out
}

// assertSameNamesAndSizes compares the local tree with the remote tree
// (names, kinds and file sizes; conflict copies are ignored).
func assertSameNamesAndSizes(t *testing.T, e *testEnv) {
	t.Helper()
	local := localTree(t, e)
	remote := e.rdav().tree(t)
	for p, sz := range local {
		if strings.Contains(p, ".conflict-") {
			continue
		}
		r, ok := remote[p]
		if !ok {
			t.Errorf("%q exists locally but not on the server", p)
			continue
		}
		if !strings.HasSuffix(p, "/") && r.Size != sz {
			t.Errorf("%q: size local %d, remote %d", p, sz, r.Size)
		}
	}
	for p := range remote {
		if _, ok := local[p]; !ok {
			t.Errorf("%q exists on the server but not locally", p)
		}
	}
}

func noConflictCopies(t *testing.T, e *testEnv) {
	t.Helper()
	for p := range localTree(t, e) {
		if strings.Contains(p, ".conflict-") {
			t.Errorf("unexpected conflict copy %q", p)
		}
	}
}

// assertNoOpSecondSync syncs once more and verifies that nothing changes on
// the server (same etags/fileids) and locally (same tree, no conflicts).
func assertNoOpSecondSync(t *testing.T, e *testEnv) {
	t.Helper()
	d := e.rdav()
	snap := func() map[string]string {
		m := map[string]string{}
		for p, en := range d.tree(t) {
			m[p] = en.ETag + "|" + en.FileID
		}
		return m
	}
	beforeRemote, beforeLocal := snap(), localTree(t, e)
	quick(t, e)
	afterRemote, afterLocal := snap(), localTree(t, e)
	for p, v := range beforeRemote {
		if afterRemote[p] != v {
			t.Errorf("second sync changed remote %q: %q -> %q", p, v, afterRemote[p])
		}
	}
	for p := range afterRemote {
		if _, ok := beforeRemote[p]; !ok {
			t.Errorf("second sync created remote %q", p)
		}
	}
	if len(beforeLocal) != len(afterLocal) {
		t.Errorf("second sync changed the local tree: %d -> %d entries", len(beforeLocal), len(afterLocal))
	}
	for p := range afterLocal {
		if _, ok := beforeLocal[p]; !ok {
			t.Errorf("second sync created local %q", p)
		}
	}
	noConflictCopies(t, e)
}

// bigFile writes a pseudo-random file of the given size and returns its hash.
func bigFile(t *testing.T, p string, size int64, seed byte) string {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var key [32]byte
	key[0] = seed
	rng := rand.NewChaCha8(key)
	h := sha256.New()
	w := io.MultiWriter(f, h)
	buf := make([]byte, 1<<20)
	for left := size; left > 0; {
		n := min(left, int64(len(buf)))
		rng.Read(buf[:n])
		if _, err := w.Write(buf[:n]); err != nil {
			t.Fatal(err)
		}
		left -= n
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fileHash(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return st.Sys().(*syscall.Stat_t).Ino
}

func requireLarge(t *testing.T) {
	t.Helper()
	if os.Getenv("CS_IT_LARGE") != "1" {
		t.Skip("set CS_IT_LARGE=1 to run large-data scenarios")
	}
}

// writeMany writes n small files "dir/f%05d.txt" locally.
func writeMany(t *testing.T, e *testEnv, dir string, n int) {
	t.Helper()
	e.mkdirLocal(dir)
	for i := range n {
		e.writeLocal(fmt.Sprintf("%s/f%05d.txt", dir, i), fmt.Sprintf("content %d", i))
	}
}

// ── OCS sharing helpers ──────────────────────────────────────────────────────

var ocsStatusRe = regexp.MustCompile(`"status"\s*:\s*"ok"`)

// createShare tries to share a path (absolute EOS path) and reports whether
// the server accepted it.
func createShare(owner, ownerPass, sharePath, with string, perms int) (bool, string) {
	form := url.Values{"path": {sharePath}, "shareType": {"0"}, "shareWith": {with}, "permissions": {strconv.Itoa(perms)}}
	req, _ := http.NewRequest("POST", "http://localhost/ocs/v1.php/apps/files_sharing/api/v1/shares?format=json", strings.NewReader(form.Encode()))
	req.SetBasicAuth(owner, ownerPass)
	req.Header.Set("OCS-APIREQUEST", "true")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	// The server may answer two JSON documents; look at the first one.
	if i := strings.Index(s, "}}"); i >= 0 {
		s = s[:i+2]
	}
	return ocsStatusRe.MatchString(s), s
}

// listShares returns the raw OCS answer listing the owner's shares.
func listShares(owner, ownerPass string) string {
	req, _ := http.NewRequest("GET", "http://localhost/ocs/v1.php/apps/files_sharing/api/v1/shares?format=json", nil)
	req.SetBasicAuth(owner, ownerPass)
	req.Header.Set("OCS-APIREQUEST", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func einsteinAbs(e *testEnv, rel string) string {
	return "/eos/user/e/einstein/" + e.remoteID + "/" + rel
}

// versionHrefs lists the versions reva exposes for a file id (may be empty).
func versionHrefs(d rawDAV, fileID string) []string {
	u := davRoot + "/meta/" + url.PathEscape(fileID) + "/v"
	es, code, err := d.propfindURL(u, 1, "/remote.php/dav/meta/"+fileID+"/v")
	if err != nil || code != 207 {
		return nil
	}
	var out []string
	for _, e := range es {
		if e.Path != "" {
			out = append(out, e.Path)
		}
	}
	return out
}

// ── 2.1 / 2.2 nested trees ───────────────────────────────────────────────────

func nestedTree() map[string]string {
	m := map[string]string{}
	for _, a := range []string{"alpha", "beta", "gamma"} {
		for _, b := range []string{"one", "two"} {
			for _, c := range []string{"x", "y"} {
				dir := path.Join(a, b, c)
				m[dir+"/file.txt"] = "content of " + dir
				m[dir+"/deeper/leaf.txt"] = "leaf of " + dir
			}
			m[path.Join(a, b)+"/mid.txt"] = "mid " + a + b
		}
		m[a+"/top.txt"] = "top " + a
	}
	return m
}

// Plan 2.1: a nested tree created locally reaches the server.
func TestSpec_2_1_NestedTree_LocalToRemote(t *testing.T) {
	env := setup(t)
	for p, c := range nestedTree() {
		env.writeLocal(p, c)
	}
	quick(t, env)
	d := env.rdav()
	for p, c := range nestedTree() {
		if got := string(d.get(t, p)); got != c {
			t.Errorf("%s: got %q want %q", p, got, c)
		}
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

// Plan 2.2: a nested tree created on the server reaches the local folder.
func TestSpec_2_2_NestedTree_RemoteToLocal(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	tree := nestedTree()
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		d.mkcolAll(t, path.Dir(p))
		d.put(t, p, []byte(tree[p]))
	}
	quick(t, env)
	for p, c := range tree {
		if got := env.readLocal(p); got != c {
			t.Errorf("%s: got %q want %q", p, got, c)
		}
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

// ── 2.5 zero-byte file and empty folder ──────────────────────────────────────

// Plan 2.5: an empty file and an empty folder sync in both directions.
func TestSpec_2_5_ZeroByteFileAndEmptyFolder(t *testing.T) {
	env := setup(t)
	d := env.rdav()

	env.writeLocal("local-empty.txt", "")
	env.mkdirLocal("local-empty-dir")
	env.mkdirLocal("parent/local-empty-sub")
	d.put(t, "remote-empty.txt", nil)
	d.mkcol(t, "remote-empty-dir")
	quick(t, env)

	if e, ok := d.stat("local-empty.txt"); !ok || e.Size != 0 {
		t.Errorf("local-empty.txt on server: exists=%v size=%d", ok, e.Size)
	}
	if e, ok := d.stat("local-empty-dir"); !ok || !e.IsDir {
		t.Errorf("local-empty-dir not created on server (exists=%v)", ok)
	}
	if e, ok := d.stat("parent/local-empty-sub"); !ok || !e.IsDir {
		t.Errorf("parent/local-empty-sub not created on server (exists=%v)", ok)
	}
	if st, err := os.Stat(filepath.Join(env.localDir, "remote-empty.txt")); err != nil || st.Size() != 0 || st.IsDir() {
		t.Errorf("remote-empty.txt not downloaded as empty file: %v", err)
	}
	if st, err := os.Stat(filepath.Join(env.localDir, "remote-empty-dir")); err != nil || !st.IsDir() {
		t.Errorf("remote-empty-dir not created locally: %v", err)
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

// ── 2.6 large files ──────────────────────────────────────────────────────────

// Plan 2.6: a 100 MB file transfers in both directions, hash-verified.
func TestSpec_2_6_LargeFile100MB(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	const size = 100 << 20

	upHash := bigFile(t, filepath.Join(env.localDir, "up.bin"), size, 1)
	srcDown := filepath.Join(t.TempDir(), "down.bin")
	downHash := bigFile(t, srcDown, size, 2)
	d.putFile(t, "down.bin", srcDown)
	os.Remove(srcDown)

	syncWait(t, env, 10*time.Minute)

	if h, n := d.hashOf(t, "up.bin"); n != size || h != upHash {
		t.Errorf("uploaded file mismatch: size %d hash ok=%v", n, h == upHash)
	}
	if got := fileHash(t, filepath.Join(env.localDir, "down.bin")); got != downHash {
		t.Errorf("downloaded file hash mismatch")
	}
	noConflictCopies(t, env)
}

// Plan 2.6: a transfer that takes longer than 60 s must complete (the old
// client had a 60 s overall HTTP timeout). The daemon's bandwidth limit slows
// a ~20 MB file in each direction to well over a minute.
func TestSpec_2_6_TransferLongerThan60s(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	const size = 20 << 20
	const limit = 250 << 10 // bytes/sec -> ~80 s

	upHash := bigFile(t, filepath.Join(env.localDir, "slow-up.bin"), size, 3)
	setSettings(t, env, limit, -1, -1, -1)
	start := time.Now()
	syncWait(t, env, 10*time.Minute)
	elapsed := time.Since(start)
	if elapsed < 60*time.Second {
		t.Logf("upload only took %v: bandwidth limit may not apply to uploads", elapsed)
	}
	if h, n := d.hashOf(t, "slow-up.bin"); n != size || h != upHash {
		t.Errorf("slow upload: size %d, hash ok=%v (took %v)", n, h == upHash, elapsed)
	}

	src := filepath.Join(t.TempDir(), "slow-down.bin")
	downHash := bigFile(t, src, size, 4)
	d.putFile(t, "slow-down.bin", src)
	os.Remove(src)
	setSettings(t, env, 0, limit, -1, -1)
	start = time.Now()
	syncWait(t, env, 10*time.Minute)
	elapsed = time.Since(start)
	if elapsed < 60*time.Second {
		t.Logf("download only took %v: bandwidth limit may not apply to downloads", elapsed)
	}
	if got := fileHash(t, filepath.Join(env.localDir, "slow-down.bin")); got != downHash {
		t.Errorf("slow download hash mismatch (took %v)", elapsed)
	}
	noConflictCopies(t, env)
}

// Plan 2.6 (opt-in): 2 GB and 10 GB files, both ways.
func TestSpec_2_6_HugeFiles(t *testing.T) {
	requireLarge(t)
	for _, tc := range []struct {
		name string
		size int64
	}{{"2GB", 2 << 30}, {"10GB", 10 << 30}} {
		t.Run(tc.name, func(t *testing.T) {
			env := setup(t)
			d := env.rdav()
			upHash := bigFile(t, filepath.Join(env.localDir, "up.bin"), tc.size, 5)
			syncWait(t, env, 90*time.Minute)
			if h, n := d.hashOf(t, "up.bin"); n != tc.size || h != upHash {
				t.Errorf("upload mismatch (size %d)", n)
			}
			os.Remove(filepath.Join(env.localDir, "up.bin"))
			quick(t, env) // propagates the delete

			src := filepath.Join(t.TempDir(), "down.bin")
			downHash := bigFile(t, src, tc.size, 6)
			d.putFile(t, "down.bin", src)
			os.Remove(src)
			syncWait(t, env, 90*time.Minute)
			if got := fileHash(t, filepath.Join(env.localDir, "down.bin")); got != downHash {
				t.Errorf("download hash mismatch")
			}
		})
	}
}

// ── 2.7 many files / deep trees ──────────────────────────────────────────────

// Plan 2.7: 1,000 files in one folder, uploaded and downloaded.
func TestSpec_2_7_ThousandFilesInOneFolder(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	setSettings(t, env, -1, -1, 8, 8)

	writeMany(t, env, "up", 1000)
	d.mkcol(t, "down")
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range 1000 {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			c, err := d.status("PUT", fmt.Sprintf("down/f%05d.txt", i), []byte(fmt.Sprintf("content %d", i)), nil)
			if err != nil || !okStatus(c) {
				t.Errorf("seed remote file %d: %d %v", i, c, err)
			}
		}()
	}
	wg.Wait()

	syncWait(t, env, 20*time.Minute)

	if n := len(d.list(t, "up")); n != 1000 {
		t.Errorf("server has %d files in up/, want 1000", n)
	}
	n := 0
	entries, _ := os.ReadDir(filepath.Join(env.localDir, "down"))
	n = len(entries)
	if n != 1000 {
		t.Errorf("local down/ has %d files, want 1000", n)
	}
	if got := env.readLocal("down/f00999.txt"); got != "content 999" {
		t.Errorf("down/f00999.txt = %q", got)
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

func deepPath(levels int) string {
	parts := make([]string, levels)
	for i := range parts {
		parts[i] = fmt.Sprintf("d%02d", i)
	}
	return strings.Join(parts, "/")
}

// Plan 2.7: a 50-level deep tree, created locally and a second one on the
// server.
func TestSpec_2_7_DeepTree50Levels(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	deep := deepPath(50)

	env.writeLocal("fromlocal/"+deep+"/leaf.txt", "local leaf")
	d.mkcolAll(t, "fromremote/"+deep)
	d.put(t, "fromremote/"+deep+"/leaf.txt", []byte("remote leaf"))

	quick(t, env)

	if got := string(d.get(t, "fromlocal/"+deep+"/leaf.txt")); got != "local leaf" {
		t.Errorf("deep local leaf on server: %q", got)
	}
	if got := env.readLocal("fromremote/" + deep + "/leaf.txt"); got != "remote leaf" {
		t.Errorf("deep remote leaf locally: %q", got)
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

// Plan 2.7 (opt-in): 10,000 files.
func TestSpec_2_7_TenThousandFiles(t *testing.T) {
	requireLarge(t)
	env := setup(t)
	d := env.rdav()
	setSettings(t, env, -1, -1, 16, 16)
	writeMany(t, env, "many", 10000)
	syncWait(t, env, 60*time.Minute)
	if n := len(d.list(t, "many")); n != 10000 {
		t.Errorf("server has %d files, want 10000", n)
	}
	assertNoOpSecondSync(t, env)
}

// ── 3.x rename / move ────────────────────────────────────────────────────────

// Plan 3.1: renaming a file that has versions (and a share) locally is a
// server-side MOVE: fileid, versions and share survive.
func TestSpec_3_1_RenameKeepsFileIDVersionsAndShare(t *testing.T) {
	env := setup(t)
	d := env.rdav()

	now := time.Now()
	env.writeLocalAt("doc.txt", "version one", now)
	quick(t, env)
	env.writeLocalAt("doc.txt", "version number two", now.Add(5*time.Second))
	quick(t, env)
	if got := env.readRemote("doc.txt"); got != "version number two" {
		t.Fatalf("setup: remote doc.txt = %q", got)
	}
	fid := d.fileID(t, "doc.txt")
	versionsBefore := versionHrefs(d, fid)

	shared, msg := createShare(webdavUser, webdavPass, einsteinAbs(env, "doc.txt"), "marie", 31)
	if !shared {
		t.Logf("share part skipped: dev reva cannot create shares (%s)", strings.TrimSpace(msg))
	}

	if err := os.Rename(filepath.Join(env.localDir, "doc.txt"), filepath.Join(env.localDir, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	quick(t, env)

	if d.exists("doc.txt") {
		t.Errorf("doc.txt still on the server after local rename")
	}
	if !d.exists("renamed.txt") {
		t.Fatalf("renamed.txt not on the server")
	}
	if got := d.fileID(t, "renamed.txt"); got != fid {
		t.Errorf("fileid changed on rename: %q -> %q (rename was delete+upload, not MOVE)", fid, got)
	}
	if len(versionsBefore) == 0 {
		t.Logf("versions check skipped: dev reva lists no versions under /remote.php/dav/meta/<fileid>/v")
	} else if after := versionHrefs(d, fid); len(after) < len(versionsBefore) {
		t.Errorf("versions lost on rename: %d -> %d", len(versionsBefore), len(after))
	}
	if shared {
		if s := listShares(webdavUser, webdavPass); !strings.Contains(s, "renamed.txt") {
			t.Errorf("share not present on renamed file: %s", s)
		}
	}
}

// Plan 3.2: renaming a folder with 1,000 files is a single MOVE: the folder's
// fileid is unchanged.
func TestSpec_3_2_RenameFolderWith1000Files(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	setSettings(t, env, -1, -1, 8, 8)
	writeMany(t, env, "bigdir", 1000)
	syncWait(t, env, 20*time.Minute)
	fid := d.fileID(t, "bigdir")

	if err := os.Rename(filepath.Join(env.localDir, "bigdir"), filepath.Join(env.localDir, "bigdir-renamed")); err != nil {
		t.Fatal(err)
	}
	syncWait(t, env, 20*time.Minute)

	if d.exists("bigdir") {
		t.Errorf("old folder still on the server")
	}
	if got := d.fileID(t, "bigdir-renamed"); got != fid {
		t.Errorf("folder fileid changed: %q -> %q (not a MOVE)", fid, got)
	}
	if n := len(d.list(t, "bigdir-renamed")); n != 1000 {
		t.Errorf("renamed folder holds %d files, want 1000", n)
	}
}

// Plan 3.3: moving files and folders between directories keeps their ids.
func TestSpec_3_3_Moves(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	env.writeLocal("src/file.txt", "movable file")
	env.writeLocal("src/sub/inner.txt", "inner")
	env.mkdirLocal("dst")
	env.writeLocal("keep/other.txt", "other")
	quick(t, env)
	fileID := d.fileID(t, "src/file.txt")
	subID := d.fileID(t, "src/sub")

	mv := func(from, to string) {
		if err := os.Rename(filepath.Join(env.localDir, from), filepath.Join(env.localDir, to)); err != nil {
			t.Fatal(err)
		}
	}
	mv("src/file.txt", "dst/file.txt")
	mv("src/sub", "dst/sub-moved")
	quick(t, env)

	if d.exists("src/file.txt") || d.exists("src/sub") {
		t.Errorf("old locations still on the server")
	}
	if got := d.fileID(t, "dst/file.txt"); got != fileID {
		t.Errorf("file fileid changed on move: %q -> %q", fileID, got)
	}
	if got := d.fileID(t, "dst/sub-moved"); got != subID {
		t.Errorf("folder fileid changed on move: %q -> %q", subID, got)
	}
	if got := string(d.get(t, "dst/sub-moved/inner.txt")); got != "inner" {
		t.Errorf("moved folder content: %q", got)
	}

	// Move back up to the root and rename at the same time.
	mv("dst/file.txt", "root-file.txt")
	quick(t, env)
	if got := d.fileID(t, "root-file.txt"); got != fileID {
		t.Errorf("file fileid changed on move+rename: %q -> %q", fileID, got)
	}
	assertSameNamesAndSizes(t, env)
	noConflictCopies(t, env)
}

// Plan 3.4: a case-only rename.
func TestSpec_3_4_CaseOnlyRename(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	env.writeLocal("report.txt", "case test")
	env.writeLocal("folder/x.txt", "x")
	quick(t, env)
	fid := d.fileID(t, "report.txt")
	dirID := d.fileID(t, "folder")

	if err := os.Rename(filepath.Join(env.localDir, "report.txt"), filepath.Join(env.localDir, "REPORT.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(env.localDir, "folder"), filepath.Join(env.localDir, "Folder")); err != nil {
		t.Fatal(err)
	}
	quick(t, env)

	names := map[string]bool{}
	for _, e := range d.list(t, "") {
		names[e.Path] = true
	}
	if !names["REPORT.txt"] || names["report.txt"] {
		t.Errorf("file case rename not applied on the server: %v", names)
	}
	if !names["Folder"] || names["folder"] {
		t.Errorf("folder case rename not applied on the server: %v", names)
	}
	if got := d.fileID(t, "REPORT.txt"); got != fid {
		t.Errorf("fileid changed on case-only rename: %q -> %q", fid, got)
	}
	if got := d.fileID(t, "Folder"); got != dirID {
		t.Errorf("folder fileid changed on case-only rename: %q -> %q", dirID, got)
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

// Plan 3.5: a rename done on the server (WebDAV MOVE) is replayed locally
// with os.Rename: the local inode is preserved (no re-download).
func TestSpec_3_5_RemoteRenameAppliedLocally(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	env.writeLocal("old-name.txt", "rename me remotely")
	env.writeLocal("olddir/inner.txt", "inner content")
	quick(t, env)

	fileIno := inode(t, filepath.Join(env.localDir, "old-name.txt"))
	dirIno := inode(t, filepath.Join(env.localDir, "olddir"))
	innerIno := inode(t, filepath.Join(env.localDir, "olddir/inner.txt"))

	d.move(t, "old-name.txt", "new-name.txt")
	d.move(t, "olddir", "newdir")
	quick(t, env)

	if env.localExists("old-name.txt") || env.localExists("olddir") {
		t.Errorf("old names still present locally")
	}
	if got := env.readLocal("new-name.txt"); got != "rename me remotely" {
		t.Errorf("new-name.txt = %q", got)
	}
	if got := env.readLocal("newdir/inner.txt"); got != "inner content" {
		t.Errorf("newdir/inner.txt = %q", got)
	}
	if got := inode(t, filepath.Join(env.localDir, "new-name.txt")); got != fileIno {
		t.Errorf("file inode changed (%d -> %d): the file was re-downloaded instead of renamed", fileIno, got)
	}
	if got := inode(t, filepath.Join(env.localDir, "newdir")); got != dirIno {
		t.Errorf("dir inode changed (%d -> %d): the dir was recreated instead of renamed", dirIno, got)
	}
	if got := inode(t, filepath.Join(env.localDir, "newdir/inner.txt")); got != innerIno {
		t.Errorf("inner file inode changed (%d -> %d)", innerIno, got)
	}
	noConflictCopies(t, env)
	assertNoOpSecondSync(t, env)
}

// ── 4.x file names ───────────────────────────────────────────────────────────

func specNames() []string {
	name255 := strings.Repeat("n", 251) + ".txt"
	return []string{
		"unicode-éàüñß-Ωλ.txt",
		"emoji-😀🎉🚀.txt",
		"cjk-日本語-中文-한국어.txt",
		"special # % ? & + ; ' \" chars.txt",
		"with space.txt",
		" leading-space.txt",
		"trailing-space.txt ",
		"trailing-dot.",
		"percent-%41-%2F-%25.txt",
		"plus+plus+.txt",
		"hash#only",
		"question?mark",
		"semi;colon",
		name255,
	}
}

// acceptedByServer probes which of the names reva/EOS accepts.
func acceptedByServer(t *testing.T, d rawDAV, names []string) (ok, rejected []string) {
	t.Helper()
	d.mkcol(t, "__probe")
	for _, n := range names {
		c, err := d.status("PUT", "__probe/"+n, []byte("p"), nil)
		if err == nil && okStatus(c) {
			ok = append(ok, n)
		} else {
			rejected = append(rejected, n)
		}
	}
	d.del(t, "__probe")
	return
}

// Plan 4.1 / 4.4: unusual file names created locally reach the server intact,
// and the second sync is a no-op.
func TestSpec_4_1_Names_LocalToRemote(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	names, rejected := acceptedByServer(t, d, specNames())
	for _, n := range rejected {
		t.Logf("server rejects %q: excluded", n)
	}
	for _, n := range names {
		env.writeLocal(n, "content of "+n)
	}
	env.writeLocal("dir # % ? & +/inner & file#1.txt", "inside weird dir")
	quick(t, env)

	for _, n := range names {
		t.Run(fmt.Sprintf("%.30q", n), func(t *testing.T) {
			if !d.exists(n) {
				t.Fatalf("%q missing on the server", n)
			}
			if got := string(d.get(t, n)); got != "content of "+n {
				t.Errorf("content = %q", got)
			}
		})
	}
	if got := string(d.get(t, "dir # % ? & +/inner & file#1.txt")); got != "inside weird dir" {
		t.Errorf("weird dir file: %q", got)
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

// Plan 4.2 / 4.4: unusual file names created on the server are downloaded
// intact, and the second sync is a no-op.
func TestSpec_4_2_Names_RemoteToLocal(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	names, rejected := acceptedByServer(t, d, specNames())
	for _, n := range rejected {
		t.Logf("server rejects %q: excluded", n)
	}
	for _, n := range names {
		d.put(t, n, []byte("content of "+n))
	}
	d.mkcol(t, "dir # % ? & +")
	d.put(t, "dir # % ? & +/inner & file#1.txt", []byte("inside weird dir"))
	quick(t, env)

	for _, n := range names {
		t.Run(fmt.Sprintf("%.30q", n), func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(env.localDir, n))
			if err != nil {
				t.Fatalf("%q not downloaded: %v", n, err)
			}
			if string(b) != "content of "+n {
				t.Errorf("content = %q", b)
			}
		})
	}
	if got := env.readLocal("dir # % ? & +/inner & file#1.txt"); got != "inside weird dir" {
		t.Errorf("weird dir file: %q", got)
	}
	assertSameNamesAndSizes(t, env)
	assertNoOpSecondSync(t, env)
}

// Plan 4.3: names that Windows forbids (: \ * < > |) are valid on Linux. When
// created on the server they must sync; if the server refuses them the client
// must report an error and keep working.
func TestSpec_4_3_WindowsForbiddenNames(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	names := []string{"colon:name.txt", `back\slash.txt`, "star*name.txt", "less<than.txt", "greater>than.txt", "pipe|name.txt"}
	ok, rejected := acceptedByServer(t, d, names)
	for _, n := range ok {
		d.put(t, n, []byte("content of "+n))
	}
	if len(rejected) > 0 {
		events := subscribeEvents(t, env)
		for _, n := range rejected {
			env.writeLocal(n, "rejected "+n)
		}
		env.writeLocal("fine.txt", "fine")
		quick(t, env)
		if !daemonAlive(env) {
			t.Fatal("daemon died while the server rejected file names")
		}
		if len(events.failures()) == 0 {
			t.Errorf("server rejected %v but no clear error was reported", rejected)
		}
		if !d.exists("fine.txt") {
			t.Errorf("fine.txt blocked by rejected names")
		}
	} else {
		quick(t, env)
	}
	for _, n := range ok {
		b, err := os.ReadFile(filepath.Join(env.localDir, n))
		if err != nil {
			t.Errorf("%q not downloaded: %v", n, err)
			continue
		}
		if string(b) != "content of "+n {
			t.Errorf("%q content = %q", n, b)
		}
	}
	noConflictCopies(t, env)
}

// ── 5.5 ──────────────────────────────────────────────────────────────────────

// Plan 5.5: a folder is deleted on the server while a new file is added to it
// locally: the new file must end up on the server (and stay locally).
func TestSpec_5_5_RemoteFolderDeletedWhileLocalFileAdded(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	env.writeLocal("proj/old.txt", "old file")
	quick(t, env)

	d.del(t, "proj")
	env.writeLocal("proj/new.txt", "added while the folder was deleted")
	quick(t, env)

	if got := env.readLocal("proj/new.txt"); got != "added while the folder was deleted" {
		t.Errorf("local new.txt lost or changed: %q", got)
	}
	if !d.exists("proj/new.txt") {
		t.Fatalf("new.txt did not reach the server")
	}
	if got := string(d.get(t, "proj/new.txt")); got != "added while the folder was deleted" {
		t.Errorf("server new.txt = %q", got)
	}
}

// ── 8.x sharing ──────────────────────────────────────────────────────────────

// sharedFolder creates /<id>/shared on einstein's side, shares it with marie
// and returns marie's env syncing the received folder, or skips.
func sharedFolder(t *testing.T, perms int) (einstein, marie *testEnv, marieDav rawDAV) {
	t.Helper()
	einstein = setup(t)
	d := einstein.rdav()
	d.mkcol(t, "shared")
	d.put(t, "shared/seed.txt", []byte("seed"))
	ok, msg := createShare(webdavUser, webdavPass, einsteinAbs(einstein, "shared"), "marie", perms)
	if !ok {
		t.Skipf("sharing not available in the dev reva: %s", strings.TrimSpace(msg))
	}
	// Find the received folder in marie's namespace.
	mar := rawDAV{base: "http://localhost/remote.php/webdav", user: "marie", pass: "radioactivity"}
	for _, prefix := range []string{"", "Shares", "shares"} {
		es, code, _ := mar.propfindURL(mar.base+"/"+prefix, 1, "/remote.php/webdav")
		if code != 207 {
			continue
		}
		for _, e := range es {
			if path.Base(e.Path) == "shared" && e.IsDir {
				marieDav = rawDAV{base: mar.base + "/" + e.Path, user: "marie", pass: "radioactivity"}
				marie = setupAs(t, "marie", "radioactivity", marieDav.base)
				return
			}
		}
	}
	t.Skip("share created but not visible in marie's WebDAV namespace")
	return
}

// Plan 8.1: einstein and marie in a read-write share: each change reaches
// the other within one cycle.
func TestSpec_8_1_ReadWriteShareBothWays(t *testing.T) {
	ein, mar, _ := sharedFolder(t, 31)
	quick(t, mar)
	if mar.readLocal("seed.txt") != "seed" {
		t.Fatalf("marie did not receive seed.txt")
	}
	mar.writeLocal("from-marie.txt", "hello einstein")
	quick(t, mar)
	if !ein.rdav().exists("shared/from-marie.txt") {
		t.Fatalf("marie's file did not reach the server")
	}
	ein.writeRemote("shared/from-einstein.txt", "hello marie")
	quick(t, mar)
	if got := mar.readLocal("from-einstein.txt"); got != "hello marie" {
		t.Errorf("marie did not receive einstein's file: %q", got)
	}
}

// Plan 8.2: a read-only share: marie's local edits and deletes are not
// applied on the server, and the server state is restored locally.
func TestSpec_8_2_ReadOnlyShare(t *testing.T) {
	ein, mar, md := sharedFolder(t, 1)
	quick(t, mar)
	if mar.readLocal("seed.txt") != "seed" {
		t.Fatalf("marie did not receive seed.txt")
	}
	mar.writeLocalAt("seed.txt", "edited by marie", time.Now().Add(5*time.Second))
	mar.writeLocal("new-by-marie.txt", "nope")
	quick(t, mar)
	if got := string(md.get(t, "seed.txt")); got != "seed" {
		t.Errorf("read-only edit reached the server: %q", got)
	}
	if md.exists("new-by-marie.txt") {
		t.Errorf("read-only upload reached the server")
	}
	mar.deleteLocal("seed.txt")
	quick(t, mar)
	if !ein.rdav().exists("shared/seed.txt") {
		t.Errorf("read-only delete reached the server")
	}
	if got := mar.readLocal("seed.txt"); got != "seed" {
		t.Errorf("server version not restored locally: %q", got)
	}
}

// Plan 8.4: the share is revoked while marie syncs: marie keeps her local
// data and the cycle reports an error.
func TestSpec_8_4_ShareRevoked(t *testing.T) {
	_, mar, _ := sharedFolder(t, 31)
	quick(t, mar)
	events := subscribeEvents(t, mar)
	// Revoke: delete the shared folder's share via OCS (all shares of owner).
	req, _ := http.NewRequest("GET", "http://localhost/ocs/v1.php/apps/files_sharing/api/v1/shares?format=json", nil)
	req.SetBasicAuth(webdavUser, webdavPass)
	req.Header.Set("OCS-APIREQUEST", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out struct {
		OCS struct {
			Data []struct {
				ID   json.RawMessage `json:"id"`
				Path string          `json:"path"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Skipf("cannot list shares: %v", err)
	}
	for _, s := range out.OCS.Data {
		id := strings.Trim(string(s.ID), `"`)
		r, _ := http.NewRequest("DELETE", "http://localhost/ocs/v1.php/apps/files_sharing/api/v1/shares/"+id, nil)
		r.SetBasicAuth(webdavUser, webdavPass)
		r.Header.Set("OCS-APIREQUEST", "true")
		if rr, err := http.DefaultClient.Do(r); err == nil {
			rr.Body.Close()
		}
	}
	quick(t, mar)
	if got := mar.readLocal("seed.txt"); got != "seed" {
		t.Errorf("marie's local data lost after revoke: %q", got)
	}
	if len(events.failures()) == 0 {
		t.Errorf("no error reported after the share was revoked")
	}
}

// Plan 8.5: marie's local sync root is deleted: nothing is deleted for
// einstein on the server.
func TestSpec_8_5_DeleteLocalRootOfSharedFolder(t *testing.T) {
	ein, mar, _ := sharedFolder(t, 31)
	quick(t, mar)
	if err := os.RemoveAll(mar.localDir); err != nil {
		t.Fatal(err)
	}
	events := subscribeEvents(t, mar)
	quick(t, mar)
	if !ein.rdav().exists("shared/seed.txt") {
		t.Fatalf("einstein's data was deleted after marie removed her local root")
	}
	if len(events.failures()) == 0 {
		t.Errorf("no error reported for the missing local root")
	}
}

// ── 9.x ──────────────────────────────────────────────────────────────────────

// Plan 9.1: a change 10 levels deep on the server is picked up by the next
// sync (etag propagation through all parents).
func TestSpec_9_1_DeepServerChange(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	deep := deepPath(10)
	env.writeLocal(deep+"/deep.txt", "v1")
	env.writeLocal("sibling/other.txt", "unchanged")
	quick(t, env)

	d.put(t, deep+"/deep.txt", []byte("v2 from the server"))
	quick(t, env)

	if got := env.readLocal(deep + "/deep.txt"); got != "v2 from the server" {
		t.Errorf("deep change not picked up: %q", got)
	}
	noConflictCopies(t, env)
}

// Plan 9.4: only the modification time changes on the server (same content):
// no conflict copy, content intact.
func TestSpec_9_4_ServerMtimeOnlyChange(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	env.writeLocal("stable.txt", "same content")
	env.writeLocal("dir/stable2.txt", "same content 2")
	quick(t, env)

	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	for _, p := range []string{"stable.txt", "dir/stable2.txt"} {
		body := []byte("same content")
		if p != "stable.txt" {
			body = []byte("same content 2")
		}
		c, err := d.status("PUT", p, body, map[string]string{"X-OC-Mtime": future})
		if err != nil || !okStatus(c) {
			t.Fatalf("PUT %s: %d %v", p, c, err)
		}
	}
	quick(t, env)

	noConflictCopies(t, env)
	if got := env.readLocal("stable.txt"); got != "same content" {
		t.Errorf("stable.txt = %q", got)
	}
	if got := string(d.get(t, "stable.txt")); got != "same content" {
		t.Errorf("server stable.txt = %q", got)
	}
	assertNoOpSecondSync(t, env)
}

// ── 10.x trash / versions ────────────────────────────────────────────────────

func trashEntries(d rawDAV) []davEntry {
	es, code, _ := d.propfindURL(davRoot+"/trash-bin/"+d.user+"/", 1, "/remote.php/dav/trash-bin/"+d.user)
	if code != 207 {
		return nil
	}
	var out []davEntry
	for _, e := range es {
		if e.Path != "" {
			out = append(out, e)
		}
	}
	return out
}

// Plan 10.1: a file deleted locally (and therefore on the server) is
// restored from the trash and comes back locally.
func TestSpec_10_1_RestoreFromTrash(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	env.writeLocal("trashme.txt", "restore me")
	quick(t, env)
	env.deleteLocal("trashme.txt")
	quick(t, env)
	if d.exists("trashme.txt") {
		t.Fatalf("trashme.txt still on the server after local delete")
	}
	var item *davEntry
	for _, e := range trashEntries(d) {
		if strings.HasSuffix(e.Path, "trashme.txt") || strings.Contains(e.Path, "trashme") {
			e := e
			item = &e
		}
	}
	if item == nil {
		t.Skip("dev reva trash-bin lists no entries (PROPFIND /remote.php/dav/trash-bin/<user>/ is empty)")
	}
	dst := d.url("trashme.txt")
	resp, err := d.do("MOVE", davRoot+"/trash-bin/"+d.user+"/"+escPath(item.Path), nil, 0, map[string]string{"Destination": dst, "Overwrite": "F"})
	if err != nil || !okStatus(resp.StatusCode) {
		t.Skipf("restore from trash not supported by the dev reva (err %v)", err)
	}
	resp.Body.Close()
	quick(t, env)
	if got := env.readLocal("trashme.txt"); got != "restore me" {
		t.Errorf("restored file content: %q", got)
	}
	noConflictCopies(t, env)
}

// Plan 10.2: an old version is restored on the server; the new content is
// downloaded without a conflict copy.
func TestSpec_10_2_RestoreOldVersion(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	now := time.Now()
	env.writeLocalAt("versioned.txt", "first", now)
	quick(t, env)
	env.writeLocalAt("versioned.txt", "second and longer", now.Add(5*time.Second))
	quick(t, env)
	fid := d.fileID(t, "versioned.txt")
	vs := versionHrefs(d, fid)
	if len(vs) == 0 {
		t.Skip("dev reva exposes no versions under /remote.php/dav/meta/<fileid>/v")
	}
	resp, err := d.do("COPY", davRoot+"/meta/"+url.PathEscape(fid)+"/v/"+escPath(vs[len(vs)-1]), nil, 0,
		map[string]string{"Destination": d.url("versioned.txt"), "Overwrite": "T"})
	if err != nil || !okStatus(resp.StatusCode) {
		t.Skipf("restore of versions not supported by the dev reva (err %v)", err)
	}
	resp.Body.Close()
	quick(t, env)
	if got := env.readLocal("versioned.txt"); got != string(d.get(t, "versioned.txt")) {
		t.Errorf("local content differs from the restored server version")
	}
	noConflictCopies(t, env)
}

// ── 12.4 ─────────────────────────────────────────────────────────────────────

// Plan 12.4: a file the server refuses (EOS answers 500 for names with a
// newline) does not block the other files, and the failure is reported.
func TestSpec_12_4_RejectedFileDoesNotBlockOthers(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	if c, _ := d.status("PUT", "__probe\nname", []byte("x"), nil); okStatus(c) {
		d.del(t, "__probe\nname")
		t.Skip("this server accepts names with newlines; no refused name known")
	}
	events := subscribeEvents(t, env)
	env.writeLocal("a-ok.txt", "a")
	env.writeLocal("bad\nname.txt", "refused")
	env.writeLocal("dir/b-ok.txt", "b")
	env.writeLocal("z-ok.txt", "z")
	quick(t, env)

	if !daemonAlive(env) {
		t.Fatal("daemon died")
	}
	for _, p := range []string{"a-ok.txt", "dir/b-ok.txt", "z-ok.txt"} {
		if !d.exists(p) {
			t.Errorf("%s did not reach the server because another file failed", p)
		}
	}
	if len(events.failures()) == 0 {
		t.Errorf("the refused file was not reported (no sync-failed event)")
	}
	if !env.localExists("bad\nname.txt") {
		t.Errorf("the local file with the refused name was removed")
	}
}

// ── 13.x ─────────────────────────────────────────────────────────────────────

// Plan 13.3: transfer_streams 1 and 16 both produce a correct result.
func TestSpec_13_3_TransferStreams(t *testing.T) {
	for _, streams := range []int{1, 16} {
		t.Run(fmt.Sprintf("streams=%d", streams), func(t *testing.T) {
			env := setup(t)
			d := env.rdav()
			setSettings(t, env, -1, -1, streams, streams)
			for i := range 40 {
				env.writeLocal(fmt.Sprintf("up/d%d/f%02d.txt", i%5, i), fmt.Sprintf("up %d", i))
			}
			for i := range 5 {
				d.mkcolAll(t, fmt.Sprintf("down/d%d", i))
			}
			for i := range 40 {
				d.put(t, fmt.Sprintf("down/d%d/f%02d.txt", i%5, i), []byte(fmt.Sprintf("down %d", i)))
			}
			quick(t, env)
			for i := range 40 {
				if got := string(d.get(t, fmt.Sprintf("up/d%d/f%02d.txt", i%5, i))); got != fmt.Sprintf("up %d", i) {
					t.Errorf("up %d = %q", i, got)
				}
				if got := env.readLocal(fmt.Sprintf("down/d%d/f%02d.txt", i%5, i)); got != fmt.Sprintf("down %d", i) {
					t.Errorf("down %d = %q", i, got)
				}
			}
			assertSameNamesAndSizes(t, env)
			assertNoOpSecondSync(t, env)
		})
	}
}

// Plan 13.5: the daemon is restarted in the middle of a sync: the next cycle
// completes everything; `remove` keeps the local files.
func TestSpec_13_5_RestartDuringSync(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	bigHash := bigFile(t, filepath.Join(env.localDir, "big.bin"), 12<<20, 7)
	for i := range 20 {
		env.writeLocal(fmt.Sprintf("small/f%02d.txt", i), fmt.Sprintf("small %d", i))
	}
	setSettings(t, env, 400<<10, -1, -1, -1) // ~30 s for big.bin

	if resp, err := ipc.Send(env.sockPath, ipc.Request{Cmd: ipc.CmdSync, Name: folderName}); err != nil || !resp.OK {
		t.Fatalf("sync: %v %+v", err, resp)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := ipc.Send(env.sockPath, ipc.Request{Cmd: ipc.CmdStatus}); err == nil && sliceContains(resp.Status.Syncing, folderName) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(3 * time.Second) // let the transfer start

	env.stopDaemon()
	cmd, logFile := startDaemonAt(t, env)
	t.Cleanup(func() {
		ipc.Send(env.sockPath, ipc.Request{Cmd: ipc.CmdStop})
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
		logFile.Close()
	})
	env.waitDaemonReady()

	setSettings(t, env, 0, 0, -1, -1)
	// A startup sync may already be running; wait it out, then sync again.
	syncWait(t, env, 5*time.Minute)
	syncWait(t, env, 5*time.Minute)

	if h, n := d.hashOf(t, "big.bin"); n != 12<<20 || h != bigHash {
		t.Errorf("big.bin after restart: size %d hash ok=%v", n, h == bigHash)
	}
	for i := range 20 {
		if got := string(d.get(t, fmt.Sprintf("small/f%02d.txt", i))); got != fmt.Sprintf("small %d", i) {
			t.Errorf("small %d = %q", i, got)
		}
	}
	noConflictCopies(t, env)

	// `remove` unregisters the pair but must keep the local files.
	before := localTree(t, env)
	env.cli("remove", "-name", folderName)
	if got := fileHash(t, filepath.Join(env.localDir, "big.bin")); got != bigHash {
		t.Errorf("big.bin changed or removed by `remove`")
	}
	after := localTree(t, env)
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Errorf("`remove` deleted local %q", p)
		}
	}
	if !d.exists("big.bin") {
		t.Errorf("`remove` deleted data on the server")
	}
}

// ── 1.3 ──────────────────────────────────────────────────────────────────────

// Plan 1.3: after the password changes on the server (here: the stored
// password is replaced by a wrong one) the cycle fails with an auth error and
// deletes nothing, locally or remotely.
func TestSpec_1_3_WrongPassword(t *testing.T) {
	env := setup(t)
	d := env.rdav()
	env.writeLocal("keep.txt", "keep me")
	env.writeLocal("dir/also.txt", "also keep")
	quick(t, env)

	events := subscribeEvents(t, env)
	if resp, err := ipc.Send(env.sockPath, ipc.Request{Cmd: ipc.CmdSetAccount, Account: &ipc.AccountPayload{Username: webdavUser, Password: "definitely-wrong"}}); err != nil || !resp.OK {
		t.Fatalf("set-account: %v %+v", err, resp)
	}
	// Create a local change and a remote deletion that would normally be applied.
	env.writeLocal("new-while-locked-out.txt", "pending")
	quick(t, env)

	fails := events.failures()
	if len(fails) == 0 {
		t.Fatalf("no sync-failed event after the password became invalid")
	}
	joined := strings.ToLower(strings.Join(fails, " | "))
	if !strings.Contains(joined, "401") && !strings.Contains(joined, "unauthori") && !strings.Contains(joined, "auth") {
		t.Errorf("error does not identify an authentication failure: %v", fails)
	}
	for _, p := range []string{"keep.txt", "dir/also.txt", "new-while-locked-out.txt"} {
		if !env.localExists(p) {
			t.Errorf("local %s was deleted after an auth failure", p)
		}
	}
	if !d.exists("keep.txt") || !d.exists("dir/also.txt") {
		t.Errorf("remote data deleted after an auth failure")
	}
	if d.exists("new-while-locked-out.txt") {
		t.Errorf("upload happened with a wrong password?")
	}

	// With the right password again everything recovers.
	if resp, err := ipc.Send(env.sockPath, ipc.Request{Cmd: ipc.CmdSetAccount, Account: &ipc.AccountPayload{Username: webdavUser, Password: webdavPass}}); err != nil || !resp.OK {
		t.Fatalf("set-account: %v %+v", err, resp)
	}
	quick(t, env)
	if !d.exists("new-while-locked-out.txt") {
		t.Errorf("sync did not recover after restoring the password")
	}
}
