//go:build windows

package cloudfiles_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/webdav"
)

// fakeFetch satisfies cloudfiles.FetchFunc for tests that don't actually
// hydrate. Returns a zero-byte stream.
func fakeFetch(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// TestSyncRoot_RegisterConnect exercises the full register → connect →
// disconnect → unregister lifecycle.
func TestSyncRoot_RegisterConnect(t *testing.T) {
	root := cloudfiles.SyncRootTempDir(t)

	p, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot:  root,
		FolderName: "test-folder",
		Fetch:      fakeFetch,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Idempotent: second Start should be a no-op.
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start (second): %v", err)
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Idempotent: second Stop should be a no-op.
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop (second): %v", err)
	}

	// Cleanup: unregister so the temp dir doesn't leave a sync-root entry
	// in the user's registry. winProvider exposes Unregister even though
	// it isn't part of the Provider interface (yet).
	type unregisterer interface {
		Unregister() error
	}
	if u, ok := p.(unregisterer); ok {
		if err := u.Unregister(); err != nil {
			t.Errorf("Unregister: %v", err)
		}
	}

	// Sanity: temp dir should still exist (CfUnregisterSyncRoot doesn't
	// delete the directory, just removes the cloud-aware status).
	if _, err := os.Stat(root); err != nil {
		t.Errorf("temp dir disappeared: %v", err)
	}
}

// TestCreatePlaceholder verifies that Create lays down a file with the right
// size and mtime.
func TestCreatePlaceholder(t *testing.T) {
	root := cloudfiles.SyncRootTempDir(t)

	p, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot:  root,
		FolderName: "test-folder",
		Fetch:      fakeFetch,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Stop()
		if u, ok := p.(interface{ Unregister() error }); ok {
			_ = u.Unregister()
		}
	})

	abs := filepath.Join(root, "hello.txt")
	res := webdav.Resource{
		Href:         "/dav/hello.txt",
		Size:         1024,
		ETag:         "etag-1",
		LastModified: time.Now().Add(-time.Hour).Truncate(time.Second),
	}

	if err := p.Create(abs, res); err != nil {
		t.Fatalf("Create: %v", err)
	}

	st, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("Stat after Create: %v", err)
	}
	if st.Size() != res.Size {
		t.Errorf("placeholder size = %d, want %d", st.Size(), res.Size)
	}
	if !st.ModTime().Equal(res.LastModified) {
		t.Errorf("placeholder mtime = %v, want %v", st.ModTime(), res.LastModified)
	}
}

// TestUpdatePlaceholder verifies metadata refresh on an existing placeholder.
func TestUpdatePlaceholder(t *testing.T) {
	root := cloudfiles.SyncRootTempDir(t)
	p, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot: root, FolderName: "test", Fetch: fakeFetch,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Stop()
		if u, ok := p.(interface{ Unregister() error }); ok {
			_ = u.Unregister()
		}
	})

	abs := filepath.Join(root, "doc.txt")
	first := webdav.Resource{Size: 100, ETag: "v1", LastModified: time.Now().Add(-time.Hour).Truncate(time.Second)}
	if err := p.Create(abs, first); err != nil {
		t.Fatalf("Create: %v", err)
	}

	second := webdav.Resource{Size: 250, ETag: "v2", LastModified: time.Now().Truncate(time.Second)}
	if err := p.Update(abs, second); err != nil {
		t.Fatalf("Update: %v", err)
	}

	st, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size() != second.Size {
		t.Errorf("size = %d, want %d", st.Size(), second.Size)
	}
}

// TestHydrate verifies the full FETCH_DATA → cfg.Fetch → CfExecute path.
// Reading from a placeholder file should trigger our callback, which
// streams the configured Fetch output back to the OS so the read returns
// those bytes.
func TestHydrate(t *testing.T) {
	const content = "hello on-demand world!"
	root := cloudfiles.SyncRootTempDir(t)

	var (
		fetchedRel string
		fetchCount int
	)
	fetch := func(_ context.Context, relPath string) (io.ReadCloser, error) {
		fetchedRel = relPath
		fetchCount++
		return io.NopCloser(strings.NewReader(content)), nil
	}

	p, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot: root, FolderName: "test", Fetch: fetch,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Stop()
		if u, ok := p.(interface{ Unregister() error }); ok {
			_ = u.Unregister()
		}
	})

	abs := filepath.Join(root, "greeting.txt")
	res := webdav.Resource{
		Size:         int64(len(content)),
		ETag:         "etag-1",
		LastModified: time.Now().Truncate(time.Second),
	}
	if err := p.Create(abs, res); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Reading the file triggers FETCH_DATA → cfg.Fetch.
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile (hydration): %v", err)
	}
	if fetchCount == 0 {
		t.Errorf("Fetch was never invoked")
	}
	if fetchedRel != "greeting.txt" {
		t.Errorf("Fetch got relPath=%q, want %q", fetchedRel, "greeting.txt")
	}
	if string(data) != content {
		t.Errorf("hydrated content = %q, want %q", string(data), content)
	}
}
