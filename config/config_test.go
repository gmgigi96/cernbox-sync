package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
)

func openDB(t *testing.T) *config.DB {
	t.Helper()
	d, err := config.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// ── Add / Get ────────────────────────────────────────────────────────────────

func TestAdd_Get(t *testing.T) {
	d := openDB(t)
	f := config.Folder{
		Name:       "myspace",
		LocalRoot:  "/home/user/myspace",
		RemoteBase: "https://dav.example.com/dav/myspace",
		Folders:    []string{"docs", "images"},
		Settings: config.FolderSettings{
			SyncHiddenFiles:  true,
			AutoSyncOnChange: false,
		},
	}
	if err := d.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := d.Get("myspace")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Name != f.Name {
		t.Errorf("Name: got %q, want %q", got.Name, f.Name)
	}
	if got.LocalRoot != f.LocalRoot {
		t.Errorf("LocalRoot: got %q, want %q", got.LocalRoot, f.LocalRoot)
	}
	if got.RemoteBase != f.RemoteBase {
		t.Errorf("RemoteBase: got %q, want %q", got.RemoteBase, f.RemoteBase)
	}
	if len(got.Folders) != 2 || got.Folders[0] != "docs" || got.Folders[1] != "images" {
		t.Errorf("Folders: got %v", got.Folders)
	}
	if !got.Settings.SyncHiddenFiles {
		t.Error("SyncHiddenFiles should be true")
	}
}

func TestGet_NotFound(t *testing.T) {
	d := openDB(t)
	got, err := d.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for nonexistent folder")
	}
}

func TestAdd_NilFolders(t *testing.T) {
	d := openDB(t)
	f := config.Folder{
		Name:       "nilfolders",
		LocalRoot:  "/tmp/nilfolders",
		RemoteBase: "https://dav.example.com/dav/nilfolders",
		Folders:    nil,
	}
	if err := d.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := d.Get("nilfolders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Folders == nil {
		// nil is fine — marshalled as []
	}
}

func TestAdd_DuplicateName_ReturnsError(t *testing.T) {
	d := openDB(t)
	f := config.Folder{Name: "dup", LocalRoot: "/tmp/a", RemoteBase: "https://example.com/a"}
	if err := d.Add(f); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := d.Add(f); err == nil {
		t.Fatal("expected error on duplicate Add, got nil")
	}
}

// ── All ──────────────────────────────────────────────────────────────────────

func TestAll_Empty(t *testing.T) {
	d := openDB(t)
	folders, err := d.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(folders) != 0 {
		t.Fatalf("expected 0 folders, got %d", len(folders))
	}
}

func TestAll_Multiple(t *testing.T) {
	d := openDB(t)
	for _, name := range []string{"b-space", "a-space", "c-space"} {
		if err := d.Add(config.Folder{
			Name:       name,
			LocalRoot:  "/tmp/" + name,
			RemoteBase: "https://example.com/" + name,
		}); err != nil {
			t.Fatalf("Add %q: %v", name, err)
		}
	}
	folders, err := d.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(folders) != 3 {
		t.Fatalf("expected 3, got %d", len(folders))
	}
	// Results should be sorted by name.
	if folders[0].Name != "a-space" {
		t.Errorf("first name: got %q, want %q", folders[0].Name, "a-space")
	}
}

// ── GetByRemoteBase ───────────────────────────────────────────────────────────

func TestGetByRemoteBase_Found(t *testing.T) {
	d := openDB(t)
	f := config.Folder{
		Name:       "rb-test",
		LocalRoot:  "/tmp/rb",
		RemoteBase: "https://dav.example.com/dav/rb",
	}
	if err := d.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := d.GetByRemoteBase("https://dav.example.com/dav/rb")
	if err != nil {
		t.Fatalf("GetByRemoteBase: %v", err)
	}
	if got == nil {
		t.Fatal("expected folder, got nil")
	}
	if got.Name != "rb-test" {
		t.Errorf("Name: got %q", got.Name)
	}
}

func TestGetByRemoteBase_NotFound(t *testing.T) {
	d := openDB(t)
	got, err := d.GetByRemoteBase("https://nonexistent.example.com/")
	if err != nil {
		t.Fatalf("GetByRemoteBase: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for unknown remote base")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestUpdate(t *testing.T) {
	d := openDB(t)
	f := config.Folder{
		Name:       "upd",
		LocalRoot:  "/tmp/upd-old",
		RemoteBase: "https://example.com/upd-old",
		Folders:    []string{"a"},
	}
	if err := d.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}
	f.LocalRoot = "/tmp/upd-new"
	f.RemoteBase = "https://example.com/upd-new"
	f.Folders = []string{"a", "b"}
	f.Settings = config.FolderSettings{AutoSyncOnChange: true}
	if err := d.Update(f); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := d.Get("upd")
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.LocalRoot != "/tmp/upd-new" {
		t.Errorf("LocalRoot: got %q", got.LocalRoot)
	}
	if len(got.Folders) != 2 {
		t.Errorf("Folders len: got %d", len(got.Folders))
	}
	if !got.Settings.AutoSyncOnChange {
		t.Error("AutoSyncOnChange should be true")
	}
}

func TestUpdate_NotFound_ReturnsError(t *testing.T) {
	d := openDB(t)
	err := d.Update(config.Folder{
		Name:       "ghost",
		LocalRoot:  "/tmp/ghost",
		RemoteBase: "https://example.com/ghost",
	})
	if err == nil {
		t.Fatal("expected error updating non-existent folder")
	}
}

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRemove(t *testing.T) {
	d := openDB(t)
	if err := d.Add(config.Folder{
		Name:       "todelete",
		LocalRoot:  "/tmp/todelete",
		RemoteBase: "https://example.com/todelete",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := d.Remove("todelete"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, err := d.Get("todelete")
	if err != nil {
		t.Fatalf("Get after Remove: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after Remove")
	}
}

func TestRemove_NotFound_ReturnsError(t *testing.T) {
	d := openDB(t)
	if err := d.Remove("nonexistent"); err == nil {
		t.Fatal("expected error removing non-existent folder")
	}
}

// ── Settings ──────────────────────────────────────────────────────────────────

func TestSettings_DefaultsAreZero(t *testing.T) {
	d := openDB(t)
	s, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.AccountUsername != "" || s.AccountPassword != "" {
		t.Error("expected empty credentials by default")
	}
	if s.SyncInterval != 0 {
		t.Error("expected zero SyncInterval by default")
	}
}

func TestSettings_SetAndGet(t *testing.T) {
	d := openDB(t)
	in := config.Settings{
		AccountUsername:   "alice",
		AccountPassword:   "secret",
		SyncInterval:      5 * time.Minute,
		LogRotateMaxAge:   24 * time.Hour,
		UploadBandwidth:   1024 * 1024,
		DownloadBandwidth: 2 * 1024 * 1024,
		TransferStreams:   4,
		MetadataStreams:   2,
		GlobalPaused:      true,
	}
	if err := d.SetSettings(in); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	out, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if out.AccountUsername != in.AccountUsername {
		t.Errorf("AccountUsername: got %q", out.AccountUsername)
	}
	if out.AccountPassword != in.AccountPassword {
		t.Errorf("AccountPassword: got %q", out.AccountPassword)
	}
	if out.SyncInterval != in.SyncInterval {
		t.Errorf("SyncInterval: got %v", out.SyncInterval)
	}
	if out.LogRotateMaxAge != in.LogRotateMaxAge {
		t.Errorf("LogRotateMaxAge: got %v", out.LogRotateMaxAge)
	}
	if out.UploadBandwidth != in.UploadBandwidth {
		t.Errorf("UploadBandwidth: got %d", out.UploadBandwidth)
	}
	if out.DownloadBandwidth != in.DownloadBandwidth {
		t.Errorf("DownloadBandwidth: got %d", out.DownloadBandwidth)
	}
	if out.TransferStreams != in.TransferStreams {
		t.Errorf("TransferStreams: got %d", out.TransferStreams)
	}
	if out.MetadataStreams != in.MetadataStreams {
		t.Errorf("MetadataStreams: got %d", out.MetadataStreams)
	}
	if !out.GlobalPaused {
		t.Error("GlobalPaused should be true")
	}
}

func TestSettings_Overwrite(t *testing.T) {
	d := openDB(t)
	if err := d.SetSettings(config.Settings{AccountUsername: "alice"}); err != nil {
		t.Fatalf("first SetSettings: %v", err)
	}
	if err := d.SetSettings(config.Settings{AccountUsername: "bob"}); err != nil {
		t.Fatalf("second SetSettings: %v", err)
	}
	s, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.AccountUsername != "bob" {
		t.Errorf("expected bob, got %q", s.AccountUsername)
	}
}

func TestSettings_ZeroValues(t *testing.T) {
	d := openDB(t)
	// Set non-zero then reset to zero to confirm zero values survive round-trip.
	if err := d.SetSettings(config.Settings{
		UploadBandwidth: 1024,
		GlobalPaused:    true,
	}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	if err := d.SetSettings(config.Settings{}); err != nil {
		t.Fatalf("SetSettings zero: %v", err)
	}
	s, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.UploadBandwidth != 0 {
		t.Errorf("UploadBandwidth should be 0, got %d", s.UploadBandwidth)
	}
	if s.GlobalPaused {
		t.Error("GlobalPaused should be false after reset")
	}
}
