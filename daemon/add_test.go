package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/db"
)

func readState(t *testing.T, localRoot string) map[string]*db.Entry {
	t.Helper()
	state, err := db.Open(filepath.Join(localRoot, ".sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	all, err := state.All()
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestAddFolder_WithBaseline_SeedsSyncState(t *testing.T) {
	d := newTestDaemon(t, time.Second)
	local := t.TempDir()

	f, err := d.addFolder(config.Folder{Name: "home", LocalRoot: local + "/", RemoteBase: "http://example.invalid/dav/spaces/x"}, []db.Entry{
		{Path: "docs", ETag: "e-docs", IsDir: true},
		{Path: "docs/a.txt", ETag: "e-a", Size: 3, LastModified: time.Unix(1700000000, 0), FileID: "id-a"},
	})
	if err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if f.LocalRoot != local {
		t.Errorf("LocalRoot = %q, want the cleaned absolute path %q", f.LocalRoot, local)
	}

	state := readState(t, local)
	if len(state) != 2 {
		t.Fatalf("expected 2 seeded entries, got %d", len(state))
	}
	a := state["docs/a.txt"]
	if a == nil || a.ETag != "e-a" || a.Size != 3 || a.LastModified.Unix() != 1700000000 || a.FileID != "id-a" {
		t.Errorf("unexpected seeded entry: %+v", a)
	}
	if got, _ := d.cfgDB.Get("home"); got == nil {
		t.Error("folder should be registered")
	}
}

func TestAddFolder_WithBaseline_DuplicateNameLeavesStateUntouched(t *testing.T) {
	d := newTestDaemon(t, time.Second)
	registerFolder(t, d, "home", t.TempDir(), "http://example.invalid/dav/spaces/a", false)
	local := t.TempDir()

	_, err := d.addFolder(config.Folder{Name: "home", LocalRoot: local, RemoteBase: "http://example.invalid/dav/spaces/b"},
		[]db.Entry{{Path: "a.txt", ETag: "e-a"}})
	if err == nil {
		t.Fatal("add with a duplicate name must fail")
	}
	if state := readState(t, local); len(state) != 0 {
		t.Errorf("baseline must not be written when the add is rejected, got %d entries", len(state))
	}
}

func TestAddFolder_WithBaseline_ExistingStateWins(t *testing.T) {
	d := newTestDaemon(t, time.Second)
	local := t.TempDir()
	state, err := db.Open(filepath.Join(local, ".sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Upsert(db.Entry{Path: "mine.txt", ETag: "e-mine"}); err != nil {
		t.Fatal(err)
	}
	_ = state.Close()

	_, err = d.addFolder(config.Folder{Name: "home", LocalRoot: local, RemoteBase: "http://example.invalid/dav/spaces/x"},
		[]db.Entry{{Path: "theirs.txt", ETag: "e-theirs"}})
	if err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if got := readState(t, local); len(got) != 1 || got["mine.txt"] == nil {
		t.Errorf("existing sync state must be kept, got %v", got)
	}
}
