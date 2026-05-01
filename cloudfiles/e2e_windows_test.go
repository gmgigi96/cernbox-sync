//go:build windows && integration

package cloudfiles_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/engine"
)

// E2E_WEBDAV, E2E_USER, E2E_PASS override the default cernbox dev server
// coordinates. Defaults point at the host machine via the QEMU user-networking
// gateway (10.0.2.2) so make test-windows-integration works out of the box.
func e2eConfig() (base, user, pass string) {
	base = os.Getenv("E2E_WEBDAV")
	if base == "" {
		base = "http://10.0.2.2/remote.php/webdav/eos/user/e/einstein"
	}
	user = os.Getenv("E2E_USER")
	if user == "" {
		user = "einstein"
	}
	pass = os.Getenv("E2E_PASS")
	if pass == "" {
		pass = "relativity"
	}
	return
}

// TestOnDemand_E2E tests the complete on-demand sync flow against a real
// cernbox backend. It:
//  1. Uploads a file to an isolated remote subdirectory.
//  2. Runs an engine sync in placeholder mode — hello.txt appears locally as a
//     zero-byte placeholder.
//  3. Reads the placeholder file, which triggers FETCH_DATA → our Fetch
//     callback → a real HTTP GET to the cernbox backend.
//  4. Verifies the hydrated content matches what was uploaded.
func TestOnDemand_E2E(t *testing.T) {
	base, user, pass := e2eConfig()

	if !e2eReachable(base, user, pass) {
		t.Skipf("cernbox backend not reachable at %s — start with: make dev-up", base)
	}

	// Isolate this test run in its own remote subdirectory.
	testDir := "e2e-ondemand-" + randHex(6)
	remoteRoot := base + "/" + testDir
	e2eMkdir(t, remoteRoot, user, pass)
	t.Cleanup(func() { e2eDelete(remoteRoot, user, pass) })

	const content = "hello from the real cernbox backend!"
	e2ePut(t, remoteRoot+"/hello.txt", user, pass, content)

	// Register a local Cloud Files sync root and start the provider.
	root := cloudfiles.SyncRootTempDir(t)

	fetchCount := 0
	provider, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot:  root,
		FolderName: "e2e-ondemand",
		Fetch: func(_ context.Context, relPath string) (io.ReadCloser, error) {
			fetchCount++
			return e2eGet(remoteRoot+"/"+relPath, user, pass)
		},
	})
	if err != nil {
		t.Fatalf("cloudfiles.New: %v", err)
	}
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("provider.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Stop()
		if u, ok := provider.(interface{ Unregister() error }); ok {
			_ = u.Unregister()
		}
	})

	// Sync: engine should create hello.txt as a placeholder (no download yet).
	dbPath := filepath.Join(t.TempDir(), "sync.db")
	if err := engine.Run(engine.Config{
		LocalRoot:    root,
		RemoteBase:   remoteRoot,
		Username:     user,
		Password:     pass,
		DBPath:       dbPath,
		Placeholders: provider,
	}); err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	st, err := os.Stat(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("placeholder not created: %v", err)
	}
	if st.Size() != int64(len(content)) {
		t.Errorf("placeholder size = %d, want %d", st.Size(), len(content))
	}

	// Read the placeholder — triggers FETCH_DATA → Fetch → real HTTP GET.
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("ReadFile (hydration): %v", err)
	}
	if fetchCount == 0 {
		t.Error("Fetch was never invoked — FETCH_DATA callback not triggered")
	}
	if string(data) != content {
		t.Errorf("hydrated content = %q, want %q", string(data), content)
	}
}

// e2eReachable returns true when the WebDAV root responds with 207.
func e2eReachable(base, user, pass string) bool {
	req, err := http.NewRequest("PROPFIND", base, nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusMultiStatus
}

func e2eMkdir(t *testing.T, url, user, pass string) {
	t.Helper()
	req, _ := http.NewRequest("MKCOL", url, nil)
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCOL %s: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("MKCOL %s: status %d", url, resp.StatusCode)
	}
}

func e2ePut(t *testing.T, url, user, pass, body string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", url, strings.NewReader(body))
	req.SetBasicAuth(user, pass)
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("PUT %s: status %d", url, resp.StatusCode)
	}
}

func e2eDelete(url, user, pass string) {
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return
	}
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func e2eGet(url, user, pass string) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
