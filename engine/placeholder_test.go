package engine_test

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/engine"
	"github.com/gmgigi96/cernbox-sync/webdav"
)

// fakePlaceholderFS records Create/Update calls without touching the disk.
// Tests assert on the recorded calls and on the absence of local files
// (a real download would leave bytes on disk).
type fakePlaceholderFS struct {
	mu      sync.Mutex
	creates []placeholderCall
	updates []placeholderCall
}

type placeholderCall struct {
	absPath string
	r       webdav.Resource
}

// Create records the call and lays down a file on disk that mirrors what
// a real placeholder looks like: the right size and modification time, but
// no content (we use a sparse file with truncate). This lets subsequent
// sync cycles see the placeholder during local scan.
func (p *fakePlaceholderFS) Create(absPath string, r webdav.Resource) error {
	p.mu.Lock()
	p.creates = append(p.creates, placeholderCall{absPath, r})
	p.mu.Unlock()

	f, err := os.Create(absPath)
	if err != nil {
		return err
	}
	if err := f.Truncate(r.Size); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if !r.LastModified.IsZero() {
		_ = os.Chtimes(absPath, r.LastModified, r.LastModified)
	}
	return nil
}

// Update records the call and refreshes the placeholder file's metadata
// (size + mtime) so the local scan continues to view it as in-sync.
func (p *fakePlaceholderFS) Update(absPath string, r webdav.Resource) error {
	p.mu.Lock()
	p.updates = append(p.updates, placeholderCall{absPath, r})
	p.mu.Unlock()

	if err := os.Truncate(absPath, r.Size); err != nil {
		return err
	}
	if !r.LastModified.IsZero() {
		_ = os.Chtimes(absPath, r.LastModified, r.LastModified)
	}
	return nil
}

// TestPlaceholder_RemoteNew verifies that in on-demand mode a remote-new
// file produces a Create() call (no Update), the file content is never
// written to disk, and the DB is updated as if the file were synced.
func TestPlaceholder_RemoteNew(t *testing.T) {
	env := setup(t)
	env.dav.addFile("hello.txt", "hello world", "etag-1", time.Unix(1700000000, 0))

	ph := &fakePlaceholderFS{}
	env.cfg.Placeholders = ph
	env.run()

	if len(ph.creates) != 1 {
		t.Fatalf("expected 1 Create call, got %d", len(ph.creates))
	}
	if len(ph.updates) != 0 {
		t.Fatalf("expected 0 Update calls, got %d", len(ph.updates))
	}
	got := ph.creates[0]
	wantAbs := filepath.Join(env.localDir, "hello.txt")
	if got.absPath != wantAbs {
		t.Errorf("Create absPath = %q, want %q", got.absPath, wantAbs)
	}
	if got.r.ETag != "etag-1" {
		t.Errorf("Create resource etag = %q, want etag-1", got.r.ETag)
	}
	if got.r.Size != int64(len("hello world")) {
		t.Errorf("Create resource size = %d, want %d", got.r.Size, len("hello world"))
	}

	// Engine must NOT have downloaded the actual content in placeholder mode.
	if len(env.dav.gets) != 0 {
		t.Errorf("expected no GETs in placeholder mode, got %v", env.dav.gets)
	}

	// Placeholder file should exist on disk with the correct size and mtime
	// (mimicking what the real Cloud Files API does).
	st, err := os.Stat(filepath.Join(env.localDir, "hello.txt"))
	if err != nil {
		t.Fatalf("placeholder file should exist on disk: %v", err)
	}
	if st.Size() != int64(len("hello world")) {
		t.Errorf("placeholder size = %d, want %d", st.Size(), len("hello world"))
	}

	// Second cycle should be a no-op: the placeholder is in-sync.
	env.run()
	if len(ph.creates) != 1 || len(ph.updates) != 0 {
		t.Errorf("second cycle should be a no-op; creates=%d updates=%d",
			len(ph.creates), len(ph.updates))
	}
	if len(env.dav.puts) != 0 || len(env.dav.deletes) != 0 || len(env.dav.gets) != 0 {
		t.Errorf("second cycle should not touch remote; gets=%v puts=%v deletes=%v",
			env.dav.gets, env.dav.puts, env.dav.deletes)
	}
}

// TestPlaceholder_RemoteUpdated verifies that when a placeholder already
// exists on disk, a remote-side change triggers Update() (not Create()).
func TestPlaceholder_RemoteUpdated(t *testing.T) {
	env := setup(t)
	env.dav.addFile("doc.md", "v1 content", "v1", time.Unix(1700000000, 0))

	ph := &fakePlaceholderFS{}
	env.cfg.Placeholders = ph

	// First cycle creates the placeholder.
	env.run()
	if len(ph.creates) != 1 || len(ph.updates) != 0 {
		t.Fatalf("after first cycle: creates=%d updates=%d", len(ph.creates), len(ph.updates))
	}

	// Bump the remote: etag changes, content grows.
	env.dav.addFile("doc.md", "v2 content here", "v2", time.Unix(1700001000, 0))

	// Second cycle should call Update() since the placeholder file already exists.
	env.run()
	if len(ph.creates) != 1 {
		t.Errorf("expected creates to stay at 1, got %d", len(ph.creates))
	}
	if len(ph.updates) != 1 {
		t.Fatalf("expected 1 Update call, got %d", len(ph.updates))
	}
	if ph.updates[0].r.ETag != "v2" {
		t.Errorf("Update etag = %q, want v2", ph.updates[0].r.ETag)
	}

	// Engine must never have GET'd the actual bytes.
	if len(env.dav.gets) != 0 {
		t.Errorf("placeholder mode must not GET file content, got %v", env.dav.gets)
	}
}

// TestFetchFile verifies that FetchFile streams remote content correctly.
// This is the path used by the Cloud Files API callback to hydrate a
// placeholder on user access.
func TestFetchFile(t *testing.T) {
	env := setup(t)
	env.dav.addFile("big.bin", "the quick brown fox", "etag-x", time.Unix(1700000000, 0))

	rc, err := engine.FetchFile(env.cfg, "big.bin")
	if err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "the quick brown fox" {
		t.Errorf("FetchFile content = %q, want %q", got, "the quick brown fox")
	}
}
