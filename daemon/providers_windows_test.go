//go:build windows

package daemon

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/ipc"
)

// requirePackageIdentity skips the test when running outside an MSIX
// package context. The on-demand registration path now goes through
// StorageProviderSyncRootManager.Register (cernbox-cf.dll) which throws
// E_NO_PACKAGE_IDENTITY in unpackaged processes, so daemon.ensureProvider
// returns nil and any test that asserts on a cached provider can't
// proceed. Run under dev/windows/run-tests-msix.ps1 to exercise these.
func requirePackageIdentity(t *testing.T) {
	t.Helper()
	id, err := cloudfiles.HasPackageIdentity()
	if err != nil {
		t.Skipf("cloudfiles shim not loadable (%v); run via dev/windows/run-tests-msix.ps1", err)
	}
	if !id {
		t.Skip("requires MSIX package identity; run via dev/windows/run-tests-msix.ps1")
	}
}

// TestDaemon_OnDemandSync verifies that syncFolder wires up a cloudfiles
// provider so the engine creates placeholders instead of downloading files,
// and that reading a placeholder triggers hydration via the daemon's Fetch
// callback.
func TestDaemon_OnDemandSync(t *testing.T) {
	requirePackageIdentity(t)
	const content = "on-demand content from fake server"

	now := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=\"utf-8\"")
		switch r.Method {
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>`)
			ms := &providerTestMultistatus{Xmlns: "DAV:"}
			ms.add("/dav/", true, "root", 0, now)
			ms.add("/dav/hello.txt", false, "etag1", int64(len(content)), now)
			_ = xml.NewEncoder(w).Encode(ms)
		case "GET":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, content)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	d := newTestDaemon(t, time.Hour)
	d.accountUsername = "u"
	d.accountPassword = "p"

	root := cloudfiles.SyncRootTempDir(t)
	f := config.Folder{
		Name:       "ondemand-test",
		LocalRoot:  root,
		RemoteBase: srv.URL + "/dav",
		Settings:   config.FolderSettings{OnDemand: true},
	}
	if err := d.cfgDB.Add(f); err != nil {
		t.Fatalf("cfgDB.Add: %v", err)
	}
	t.Cleanup(func() { d.stopAllProviders() })

	// syncFolder must create hello.txt as a placeholder (not download it).
	d.syncFolder(f)

	st, err := os.Stat(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("placeholder not created after sync: %v", err)
	}
	if st.Size() != int64(len(content)) {
		t.Errorf("placeholder size = %d, want %d", st.Size(), len(content))
	}

	// The provider must be cached so the OS callback survives between cycles.
	d.providerMu.Lock()
	p := d.providers[f.Name]
	d.providerMu.Unlock()
	if p == nil {
		t.Fatal("provider not cached in d.providers after sync")
	}

	// Reading the placeholder triggers FETCH_DATA → Fetch → GET on the fake server.
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("ReadFile (hydration): %v", err)
	}
	if string(data) != content {
		t.Errorf("hydrated content = %q, want %q", string(data), content)
	}
}

// TestDaemon_ProviderStopsOnFolderRemove verifies that the provider is evicted
// when a folder is removed via IPC so no callbacks fire afterwards.
func TestDaemon_ProviderStopsOnFolderRemove(t *testing.T) {
	requirePackageIdentity(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			w.Header().Set("Content-Type", "application/xml; charset=\"utf-8\"")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)
		}
	}))
	t.Cleanup(srv.Close)

	d := newTestDaemon(t, time.Hour)
	root := cloudfiles.SyncRootTempDir(t)
	f := config.Folder{Name: "remove-test", LocalRoot: root, RemoteBase: srv.URL + "/dav", Settings: config.FolderSettings{OnDemand: true}}
	if err := d.cfgDB.Add(f); err != nil {
		t.Fatalf("cfgDB.Add: %v", err)
	}
	t.Cleanup(func() { d.stopAllProviders() })

	d.syncFolder(f)

	d.providerMu.Lock()
	before := d.providers[f.Name]
	d.providerMu.Unlock()
	if before == nil {
		t.Fatal("provider should be cached after first sync")
	}

	resp := d.dispatch(ipc.Request{Cmd: ipc.CmdRemove, Name: f.Name})
	if !resp.OK {
		t.Fatalf("dispatch remove: %s", resp.Error)
	}

	d.providerMu.Lock()
	after := d.providers[f.Name]
	d.providerMu.Unlock()
	if after != nil {
		t.Error("provider should be evicted after folder removal")
	}
}

// TestDaemon_ProviderReusedAcrossSyncs verifies that the same provider
// instance is reused across successive sync cycles (not re-created each time).
func TestDaemon_ProviderReusedAcrossSyncs(t *testing.T) {
	requirePackageIdentity(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			w.Header().Set("Content-Type", "application/xml; charset=\"utf-8\"")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)
		}
	}))
	t.Cleanup(srv.Close)

	d := newTestDaemon(t, time.Hour)
	root := cloudfiles.SyncRootTempDir(t)
	f := config.Folder{Name: "reuse-test", LocalRoot: root, RemoteBase: srv.URL + "/dav", Settings: config.FolderSettings{OnDemand: true}}
	if err := d.cfgDB.Add(f); err != nil {
		t.Fatalf("cfgDB.Add: %v", err)
	}
	t.Cleanup(func() { d.stopAllProviders() })

	d.syncFolder(f)
	d.providerMu.Lock()
	p1 := d.providers[f.Name]
	d.providerMu.Unlock()

	d.syncFolder(f)
	d.providerMu.Lock()
	p2 := d.providers[f.Name]
	d.providerMu.Unlock()

	if p1 != p2 {
		t.Error("provider should be the same instance across sync cycles")
	}
}

// ── PROPFIND response builder ─────────────────────────────────────────────────

type providerTestMultistatus struct {
	XMLName   xml.Name               `xml:"D:multistatus"`
	Xmlns     string                 `xml:"xmlns:D,attr"`
	Responses []providerTestResponse `xml:"D:response"`
	mu        sync.Mutex             `xml:"-"`
}

type providerTestResponse struct {
	Href     string               `xml:"D:href"`
	Propstat providerTestPropstat `xml:"D:propstat"`
}

type providerTestPropstat struct {
	Prop   providerTestProp `xml:"D:prop"`
	Status string           `xml:"D:status"`
}

type providerTestProp struct {
	ResourceType  *providerTestResType `xml:"D:resourcetype,omitempty"`
	GetETag       string               `xml:"D:getetag,omitempty"`
	GetContentLen int64                `xml:"D:getcontentlength,omitempty"`
	GetLastMod    string               `xml:"D:getlastmodified,omitempty"`
}

type providerTestResType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

func (m *providerTestMultistatus) add(href string, isDir bool, etag string, size int64, modTime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := providerTestResponse{
		Href: href,
		Propstat: providerTestPropstat{
			Prop: providerTestProp{
				GetETag:       `"` + etag + `"`,
				GetContentLen: size,
				GetLastMod:    modTime.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"),
			},
			Status: "HTTP/1.1 200 OK",
		},
	}
	if isDir {
		r.Propstat.Prop.ResourceType = &providerTestResType{Collection: &struct{}{}}
	}
	m.Responses = append(m.Responses, r)
}
