//go:build windows

package cloudfiles_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/webdav"
)

// TestSubdirPlaceholder_FetchUsesForwardSlashes guards against the bug where
// a placeholder created in a nested directory hydrated via a WebDAV URL with
// backslashes percent-encoded as %5C — e.g. a request like
// `<base>/pictures%5Cvacation%5Citaly%5Ccaptions.txt` instead of
// `<base>/pictures/vacation/italy/captions.txt`.
//
// The FileIdentity blob is opaque to the OS but round-trips through the
// FETCH_DATA callback unchanged. We must store the path with forward
// slashes (normalized via filepath.ToSlash) so the relPath that comes back
// goes straight into a valid HTTP path.
func TestSubdirPlaceholder_FetchUsesForwardSlashes(t *testing.T) {
	const (
		relPath = "pictures/vacation/italy/captions.txt"
		content = "On the sixth day we drove to Siena."
	)

	root := cloudfiles.SyncRootTempDir(t)

	// Engine-side mkdirLocal creates parent directories as plain dirs;
	// only the leaf file is a CF placeholder. Mirror that here.
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash("pictures/vacation/italy")), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var (
		mu          sync.Mutex
		fetchedRel  string
		fetchCount  int
	)
	provider, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot:  root,
		FolderName: "subdir-relpath",
		Fetch: func(_ context.Context, rel string) (io.ReadCloser, error) {
			mu.Lock()
			fetchedRel = rel
			fetchCount++
			mu.Unlock()
			return io.NopCloser(strings.NewReader(content)), nil
		},
	})
	if err != nil {
		t.Fatalf("cloudfiles.New: %v", err)
	}
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Provider.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Stop()
		if u, ok := provider.(interface{ Unregister() error }); ok {
			_ = u.Unregister()
		}
	})

	abs := filepath.Join(root, filepath.FromSlash(relPath))
	if err := provider.Create(abs, webdav.Resource{
		Path:         relPath,
		Size:         int64(len(content)),
		LastModified: time.Now().UTC().Add(-time.Hour).Truncate(time.Second),
		ETag:         "etag-captions",
	}); err != nil {
		t.Fatalf("Create placeholder: %v", err)
	}

	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile (hydration): %v", err)
	}
	if string(got) != content {
		t.Errorf("hydrated content = %q, want %q", string(got), content)
	}

	mu.Lock()
	defer mu.Unlock()
	if fetchCount == 0 {
		t.Fatal("Fetch callback was never invoked")
	}
	if strings.ContainsRune(fetchedRel, '\\') {
		t.Errorf("Fetch was called with a backslash-separated path %q — would percent-encode to %%5C in the WebDAV URL", fetchedRel)
	}
	if fetchedRel != relPath {
		t.Errorf("Fetch rel = %q, want %q", fetchedRel, relPath)
	}
}
