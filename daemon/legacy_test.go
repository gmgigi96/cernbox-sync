package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/migrate"
)

// legacyEnv is a CERNBox desktop client configuration with one folder synced
// with the personal space of a fake server, and a daemon signed in to it.
type legacyEnv struct {
	d      *Daemon
	server *httptest.Server
	local  string
	ref    migrate.FolderRef
}

func newLegacyEnv(t *testing.T) *legacyEnv {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the desktop client configuration is looked for in $XDG_CONFIG_HOME on Linux only")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	// The server: one personal space holding docs/, big/ and top.txt.
	space := "/remote.php/dav/spaces/home$1"
	tree := map[string]bool{space: true, space + "/docs": true, space + "/big": true, space + "/top.txt": false}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, _ := r.BasicAuth(); user != "einstein" || pass != "relativity" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/graph/v1beta1/me/drives" {
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{
				"id": "home$1", "name": "einstein", "driveType": "personal", "driveAlias": "eos/user/e/einstein",
				"root": map[string]any{"webDavUrl": "http://" + r.Host + "/remote.php/dav/spaces/eos/user/e/einstein"},
			}}})
			return
		}
		p := strings.TrimSuffix(r.URL.Path, "/")
		isDir, ok := tree[p]
		if r.Method != "PROPFIND" || !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		paths := []string{p}
		if isDir && r.Header.Get("Depth") == "1" {
			for c := range tree {
				if strings.HasPrefix(c, p+"/") {
					paths = append(paths, c)
				}
			}
			slices.Sort(paths[1:])
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
		for _, c := range paths {
			href, resType := c, ""
			if tree[c] {
				href, resType = c+"/", "<d:collection/>"
			}
			_, _ = fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype>%s</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, href, resType)
		}
		_, _ = fmt.Fprint(w, `</d:multistatus>`)
	}))
	t.Cleanup(srv.Close)

	// The local folder, synced by the desktop client except for big/.
	local := filepath.Join(tmp, "cernbox")
	if err := os.MkdirAll(filepath.Join(local, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"docs/a.txt", "top.txt"} {
		if err := os.WriteFile(filepath.Join(local, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := sql.Open("sqlite", filepath.Join(local, ".sync_journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Exec(`CREATE TABLE metadata(phash INTEGER, path TEXT, type INTEGER, md5 TEXT, modtime INTEGER, filesize INTEGER, fileid TEXT);
		CREATE TABLE selectivesync(path TEXT, type INTEGER);
		INSERT INTO metadata VALUES (1, 'docs', 2, 'e1', 1700000000, 0, 'i1'), (2, 'docs/a.txt', 0, 'e2', 1700000000, 1, 'i2'), (3, 'top.txt', 0, 'e3', 1700000000, 1, 'i3');
		INSERT INTO selectivesync VALUES ('big/', 1);`); err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()

	cfgDir := filepath.Join(tmp, "config", "cernbox")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "cernbox.cfg")
	cfg := fmt.Sprintf(`[Accounts]
0\Folders\f1\displayString=home
0\Folders\f1\journalPath=.sync_journal.db
0\Folders\f1\localPath=%s/
0\Folders\f1\targetPath=/home
0\dav_user=einstein
0\url=%s/cernbox/desktop/

[BWLimit]
useUploadLimit=1
uploadLimit=250
`, local, srv.URL)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	d := newTestDaemon(t, time.Second)
	d.accountUsername, d.accountPassword = "einstein", "relativity"
	// Keep the imported folder from syncing against the fake server.
	d.globalPaused = true
	return &legacyEnv{d: d, server: srv, local: local, ref: migrate.FolderRef{ConfigPath: cfgPath, AccountID: "0", FolderID: "f1"}}
}

func TestLegacyDetect(t *testing.T) {
	env := newLegacyEnv(t)

	resp := env.d.dispatch(ipc.Request{Cmd: ipc.CmdLegacyDetect, Legacy: &ipc.LegacyRequest{ServerURL: env.server.URL}})
	if !resp.OK || len(resp.Legacy) != 1 || len(resp.Legacy[0].Accounts) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	f := resp.Legacy[0].Accounts[0].Folders[0]
	if f.Plan != nil || f.BaselineEntries != 3 || !slices.Equal(f.Excluded, []string{"big"}) {
		t.Errorf("unexpected folder: %+v", f)
	}
	// Accounts of other servers are left out.
	if resp := env.d.dispatch(ipc.Request{Cmd: ipc.CmdLegacyDetect, Legacy: &ipc.LegacyRequest{ServerURL: "https://other.example"}}); !resp.OK || len(resp.Legacy) != 0 {
		t.Errorf("unexpected response: %+v", resp)
	}

	resp = env.d.dispatch(ipc.Request{Cmd: ipc.CmdLegacyDetect, Legacy: &ipc.LegacyRequest{ServerURL: env.server.URL, Plan: true}})
	plan := resp.Legacy[0].Accounts[0].Folders[0].Plan
	if plan == nil || !plan.Ready || plan.RemoteBase != env.server.URL+"/remote.php/dav/spaces/home$1" ||
		!slices.Equal(plan.Folders, []string{"docs"}) || !slices.Equal(plan.UnsyncedFiles, []string{"top.txt"}) {
		t.Errorf("unexpected plan: %+v", plan)
	}
}

func TestLegacyImport(t *testing.T) {
	env := newLegacyEnv(t)
	req := ipc.Request{Cmd: ipc.CmdLegacyImport, Legacy: &ipc.LegacyRequest{
		ServerURL: env.server.URL, Folders: []migrate.FolderRef{env.ref}, ImportLimits: true,
	}}

	resp := env.d.dispatch(req)
	if !resp.OK || resp.LegacyImport == nil || len(resp.LegacyImport.Results) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if r := resp.LegacyImport.Results[0]; r.Name != "home" || r.Error != "" || r.FolderRef != env.ref {
		t.Fatalf("unexpected result: %+v", r)
	}

	f, err := env.d.cfgDB.Get("home")
	if err != nil || f == nil {
		t.Fatalf("folder not registered: %v", err)
	}
	if f.LocalRoot != env.local || f.RemoteBase != env.server.URL+"/remote.php/dav/spaces/home$1" || !slices.Equal(f.Folders, []string{"docs"}) {
		t.Errorf("unexpected folder: %+v", f)
	}
	if f.Settings.SyncHiddenFiles || !f.Settings.AutoSyncOnChange || f.Settings.Paused {
		t.Errorf("unexpected settings: %+v", f.Settings)
	}
	if state := readState(t, env.local); len(state) != 3 || state["docs/a.txt"].ETag != "e2" {
		t.Errorf("unexpected seeded state: %v", state)
	}
	if s, _ := env.d.cfgDB.GetSettings(); s.UploadBandwidth != 250_000 || s.DownloadBandwidth != 0 {
		t.Errorf("bandwidth = %d/%d", s.UploadBandwidth, s.DownloadBandwidth)
	}

	// The same folder cannot be imported twice.
	resp = env.d.dispatch(req)
	if r := resp.LegacyImport.Results[0]; r.Error == "" || r.Name != "" {
		t.Errorf("second import should fail: %+v", r)
	}
}

func TestLegacyImport_RequiresAccount(t *testing.T) {
	env := newLegacyEnv(t)
	env.d.accountUsername = ""
	resp := env.d.dispatch(ipc.Request{Cmd: ipc.CmdLegacyImport, Legacy: &ipc.LegacyRequest{ServerURL: env.server.URL, Folders: []migrate.FolderRef{env.ref}}})
	if resp.OK || !strings.Contains(resp.Error, "no account") {
		t.Errorf("unexpected response: %+v", resp)
	}
}
