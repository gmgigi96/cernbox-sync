package db_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/db"
)

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// ── conflict table ────────────────────────────────────────────────────────────

func TestConflict_AddAndReadBack(t *testing.T) {
	d := openDB(t)
	ts := time.Unix(1700000000, 0)

	if err := d.AddConflict("docs/report.txt", "/local/docs/report.conflict-20240101-120000.txt", ts); err != nil {
		t.Fatalf("AddConflict: %v", err)
	}

	entries, err := d.AllConflicts()
	if err != nil {
		t.Fatalf("AllConflicts: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 conflict entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Path != "docs/report.txt" {
		t.Errorf("Path: got %q, want %q", e.Path, "docs/report.txt")
	}
	if e.ConflictPath != "/local/docs/report.conflict-20240101-120000.txt" {
		t.Errorf("ConflictPath: got %q", e.ConflictPath)
	}
	if e.CreatedAt.Unix() != ts.Unix() {
		t.Errorf("CreatedAt: got %v, want %v", e.CreatedAt.Unix(), ts.Unix())
	}
}

func TestConflict_AllConflicts_Empty(t *testing.T) {
	d := openDB(t)

	entries, err := d.AllConflicts()
	if err != nil {
		t.Fatalf("AllConflicts: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty slice, got %d entries", len(entries))
	}
}

func TestConflict_MultipleEntries(t *testing.T) {
	d := openDB(t)
	ts := time.Now()

	pairs := []struct{ path, cp string }{
		{"a.txt", "/root/a.conflict-20240101-120000.txt"},
		{"b.txt", "/root/b.conflict-20240101-120001.txt"},
		{"c.txt", "/root/c.conflict-20240101-120002.txt"},
	}
	for _, p := range pairs {
		if err := d.AddConflict(p.path, p.cp, ts); err != nil {
			t.Fatalf("AddConflict %q: %v", p.path, err)
		}
	}

	entries, err := d.AllConflicts()
	if err != nil {
		t.Fatalf("AllConflicts: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestConflict_AddDuplicate_Replaces(t *testing.T) {
	d := openDB(t)
	cp := "/root/shared.conflict-20240101-120000.txt"

	if err := d.AddConflict("shared.txt", cp, time.Unix(1000, 0)); err != nil {
		t.Fatalf("first AddConflict: %v", err)
	}
	// Same conflict_path (PRIMARY KEY) with a different path value.
	if err := d.AddConflict("other.txt", cp, time.Unix(2000, 0)); err != nil {
		t.Fatalf("second AddConflict: %v", err)
	}

	entries, err := d.AllConflicts()
	if err != nil {
		t.Fatalf("AllConflicts: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after upsert, got %d", len(entries))
	}
	if entries[0].CreatedAt.Unix() != 2000 {
		t.Errorf("expected updated CreatedAt 2000, got %d", entries[0].CreatedAt.Unix())
	}
}

func TestConflict_Delete(t *testing.T) {
	d := openDB(t)
	cp := "/root/a.conflict-20240101-120000.txt"

	if err := d.AddConflict("a.txt", cp, time.Now()); err != nil {
		t.Fatalf("AddConflict: %v", err)
	}
	if err := d.AddConflict("b.txt", "/root/b.conflict-20240101-120001.txt", time.Now()); err != nil {
		t.Fatalf("AddConflict b: %v", err)
	}

	if err := d.DeleteConflict(cp); err != nil {
		t.Fatalf("DeleteConflict: %v", err)
	}

	entries, err := d.AllConflicts()
	if err != nil {
		t.Fatalf("AllConflicts: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 remaining entry, got %d", len(entries))
	}
	if entries[0].Path != "b.txt" {
		t.Errorf("remaining entry: got %q, want %q", entries[0].Path, "b.txt")
	}
}

func TestConflict_DeleteNonExistent_NoError(t *testing.T) {
	d := openDB(t)

	// Deleting a conflict that was never added must not return an error.
	if err := d.DeleteConflict("/nonexistent/path.conflict-20240101-120000.txt"); err != nil {
		t.Fatalf("DeleteConflict on non-existent: %v", err)
	}
}

// ── sync_state table (smoke tests to confirm existing behaviour unchanged) ────

func TestSyncState_UpsertAndGet(t *testing.T) {
	d := openDB(t)
	entry := db.Entry{
		Path:         "hello.txt",
		ETag:         "etag1",
		IsDir:        false,
		Size:         11,
		LastModified: time.Unix(1700000000, 0),
		FileID:       "fid-1",
	}
	if err := d.Upsert(entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := d.Get("hello.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.ETag != "etag1" {
		t.Errorf("ETag: got %q, want %q", got.ETag, "etag1")
	}
}

func TestSyncState_ConflictTableDoesNotInterfereWithSyncState(t *testing.T) {
	d := openDB(t)

	// Write a conflict record and a sync-state entry; verify both are independent.
	if err := d.AddConflict("x.txt", "/tmp/x.conflict-20240101-120000.txt", time.Now()); err != nil {
		t.Fatalf("AddConflict: %v", err)
	}
	if err := d.Upsert(db.Entry{Path: "x.txt", ETag: "e1", LastModified: time.Now()}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	e, err := d.Get("x.txt")
	if err != nil || e == nil {
		t.Fatalf("sync-state entry missing after conflict insert: %v", err)
	}
	conflicts, err := d.AllConflicts()
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflict entry missing after sync-state insert: %v, count=%d", err, len(conflicts))
	}
}

// ── helper: fake file on disk ─────────────────────────────────────────────────

func writeTempFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("writeTempFile: %v", err)
	}
	return path
}
