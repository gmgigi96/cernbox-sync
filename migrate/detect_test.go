package migrate

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/db"
)

type journalRow struct {
	path   string
	typ    int
	etag   string
	mtime  int64
	size   int64
	fileID string
}

// writeJournal creates a journal with the tables (and the columns read here)
// of the desktop client's SyncJournalDb.
func writeJournal(t *testing.T, path string, rows []journalRow, excluded ...string) {
	t.Helper()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	mustExec(t, conn, `CREATE TABLE metadata(phash INTEGER(8), pathlen INTEGER, path VARCHAR(4096), inode INTEGER, uid INTEGER, gid INTEGER, mode INTEGER, modtime INTEGER(8), type INTEGER, md5 VARCHAR(32), fileid VARCHAR(128), remotePerm VARCHAR(128), filesize BIGINT, PRIMARY KEY(phash));
		CREATE TABLE selectivesync(path VARCHAR(4096), type INTEGER);
		INSERT INTO selectivesync(path, type) VALUES ('undecided/', 3), ('approved/', 2);`)
	for i, r := range rows {
		mustExec(t, conn, `INSERT INTO metadata(phash, path, type, md5, modtime, filesize, fileid) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			i, r.path, r.typ, r.etag, r.mtime, r.size, r.fileID)
	}
	for _, e := range excluded {
		mustExec(t, conn, `INSERT INTO selectivesync(path, type) VALUES (?, 1)`, e)
	}
}

func mustExec(t *testing.T, conn *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// writeConfig writes a configuration shaped like the one of the CERNBox
// desktop client 5.3 (credentials omitted) with two folders, in
// <base>/cernbox/cernbox.cfg.
func writeConfig(t *testing.T, base, home, project, extra string) string {
	t.Helper()
	cfg := fmt.Sprintf(`[General]
clientVersion=5.3.2.15486

[Accounts]
0\Folders\33b4e33d\davUrl=@Variant(\0\0\0\x11\0\0\0\x46https://cernbox.cern.ch/cernbox/desktop/remote.php/dav/files/gdelmont/)
0\Folders\33b4e33d\displayString=2026 Heterogeneous Spaces
0\Folders\33b4e33d\ignoreHiddenFiles=false
0\Folders\33b4e33d\journalPath=.sync_journal.db
0\Folders\33b4e33d\localPath=%[1]s/
0\Folders\33b4e33d\paused=true
0\Folders\33b4e33d\targetPath=/eos/project/c/cernbox/Projects/2026 Heterogeneous Spaces
0\Folders\33b4e33d\virtualFilesMode=off
0\Folders\783f3376\davUrl=@Variant(\0\0\0\x11\0\0\0\x46https://cernbox.cern.ch/cernbox/desktop/remote.php/dav/files/gdelmont/)
0\Folders\783f3376\displayString=home
0\Folders\783f3376\ignoreHiddenFiles=true
0\Folders\783f3376\journalPath=.sync_journal.db
0\Folders\783f3376\localPath=%[2]s/
0\Folders\783f3376\paused=false
0\Folders\783f3376\targetPath=/home
0\Folders\783f3376\virtualFilesMode=off
0\dav_user=gdelmont
0\display-name=Gianmaria
0\http_oauth=true
0\http_user=gdelmont
0\url=https://cernbox.cern.ch/cernbox/desktop/
0\uuid=@Variant(\0\0\0\x7f\0\0\0\x6QUuid\0\x1f\xafS\xf3)
version=13
%[3]s`, filepath.ToSlash(project), filepath.ToSlash(home), extra)
	dir := filepath.Join(base, "cernbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cernbox.cfg")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, cfgPath string) Client {
	t.Helper()
	text, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return parseConfig("CERNBox", cfgPath, parseSettings(string(text)))
}

func folderByID(t *testing.T, c Client, id string) Folder {
	t.Helper()
	for _, a := range c.Accounts {
		for _, f := range a.Folders {
			if f.ID == id {
				return f
			}
		}
	}
	t.Fatalf("folder %s not found", id)
	return Folder{}
}

// fixture creates <tmp>/cernbox (with a journal) and returns the paths of the
// home folder, the project folder (absent) and the configuration directory.
func fixture(t *testing.T, rows []journalRow, excluded ...string) (home, project, base string) {
	t.Helper()
	tmp := t.TempDir()
	home, project, base = filepath.Join(tmp, "cernbox"), filepath.Join(tmp, "designs"), filepath.Join(tmp, "config")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, filepath.Join(home, ".sync_journal.db"), rows, excluded...)
	return home, project, base
}

func TestDetect_ParsesAccountsAndFolders(t *testing.T) {
	home, project, base := fixture(t, []journalRow{{"docs", 2, "e-docs", 0, 0, "id-docs"}}, "big/", "a/b/")
	cfg := writeConfig(t, base, home, project, "")

	clients := detect([]string{base}, nil)
	if len(clients) != 1 || clients[0].ConfigPath != cfg || clients[0].AppName != "CERNBox" || clients[0].Running {
		t.Fatalf("unexpected clients: %+v", clients)
	}
	a := clients[0].Accounts[0]
	if a.ServerURL != "https://cernbox.cern.ch/cernbox/desktop/" || a.Username != "gdelmont" || a.DisplayName != "Gianmaria" {
		t.Errorf("unexpected account: %+v", a)
	}

	h := folderByID(t, clients[0], "783f3376")
	if h.DisplayName != "home" || h.LocalPath != filepath.ToSlash(home)+"/" || h.TargetPath != "/home" ||
		h.DavURL != "https://cernbox.cern.ch/cernbox/desktop/remote.php/dav/files/gdelmont/" {
		t.Errorf("unexpected home folder: %+v", h)
	}
	if !h.IgnoreHiddenFiles || h.Paused || h.VirtualFiles || h.InUse || h.JournalError != "" || h.BaselineEntries != 1 {
		t.Errorf("unexpected home folder state: %+v", h)
	}
	if want := []string{"a/b", "big", "undecided"}; !slices.Equal(h.Excluded, want) {
		t.Errorf("excluded = %q, want %q", h.Excluded, want)
	}

	p := folderByID(t, clients[0], "33b4e33d")
	if p.TargetPath != "/eos/project/c/cernbox/Projects/2026 Heterogeneous Spaces" || p.IgnoreHiddenFiles || !p.Paused {
		t.Errorf("unexpected project folder: %+v", p)
	}
	// The local folder does not exist: nothing would be uploaded.
	if p.JournalPath != "" || !p.LocalIsEmpty {
		t.Errorf("unexpected project folder state: %+v", p)
	}
}

func TestDetect_ReadsEachConfigurationOnce(t *testing.T) {
	home, project, base := fixture(t, nil)
	writeConfig(t, base, home, project, "")
	// Releases 2.9-4.2 leave a symlink at the location they migrated from.
	old := filepath.Join(t.TempDir(), "old")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "cernbox"), filepath.Join(old, "cernbox")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if n := len(detect([]string{base, old, filepath.Join(old, "missing")}, nil)); n != 1 {
		t.Errorf("expected 1 client, got %d", n)
	}
}

func TestDetect_RunningClient(t *testing.T) {
	home, project, base := fixture(t, nil)
	writeConfig(t, base, home, project, "")
	clients := detect([]string{base}, []string{"bash", "/opt/cernbox-client.AppDir/usr/bin/cernbox"})
	if !clients[0].Running {
		t.Error("the running client should be detected from its executable")
	}
}

func TestBaseline_FilesAndDirectoriesOnly(t *testing.T) {
	home, project, base := fixture(t, []journalRow{
		{"docs", 2, "e-docs", 1700000000, 0, "id-docs"},
		{"docs/a.txt", 0, "e-a", 1700000001, 12, "id-a"},
		{"link", 1, "e-l", 0, 0, ""},
		{"docs/lazy.txt", 4, "e-v", 0, 99, "id-v"},
	})
	f := folderByID(t, load(t, writeConfig(t, base, home, project, "")), "783f3376")

	entries, err := Baseline(f)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(entries, func(a, b db.Entry) int { return strings.Compare(a.Path, b.Path) })
	want := []db.Entry{
		{Path: "docs", ETag: "e-docs", IsDir: true, LastModified: time.Unix(1700000000, 0), FileID: "id-docs"},
		{Path: "docs/a.txt", ETag: "e-a", Size: 12, LastModified: time.Unix(1700000001, 0), FileID: "id-a"},
	}
	same := func(a, b db.Entry) bool {
		return a.Path == b.Path && a.ETag == b.ETag && a.IsDir == b.IsDir && a.Size == b.Size && a.LastModified.Equal(b.LastModified) && a.FileID == b.FileID
	}
	if !slices.EqualFunc(entries, want, same) {
		t.Errorf("baseline = %+v", entries)
	}
}

func TestBaseline_RefusesFoldersThatCannotBeTakenOver(t *testing.T) {
	home, project, base := fixture(t, nil)
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "report.pdf"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := "0\\Folders\\vfs1\\localPath=/tmp/vfs/\n0\\Folders\\vfs1\\virtualFilesMode=suffix\n0\\FoldersWithPlaceholders\\vfs2\\localPath=/tmp/vfs2/\n"
	cfg := writeConfig(t, base, home, project, extra)
	c := load(t, cfg)

	for _, id := range []string{"vfs1", "vfs2"} {
		if f := folderByID(t, c, id); !f.VirtualFiles {
			t.Errorf("%s should use virtual files", id)
		} else if _, err := Baseline(f); err == nil || !strings.Contains(err.Error(), "virtual files") {
			t.Errorf("%s: err = %v", id, err)
		}
	}
	// Local files but no journal: every file would be uploaded again.
	if _, err := Baseline(folderByID(t, c, "33b4e33d")); err == nil || !strings.Contains(err.Error(), "no sync database") {
		t.Errorf("err = %v", err)
	}
	// An empty local folder without journal is a plain fresh sync.
	if err := os.Remove(filepath.Join(project, "report.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".owncloudsync.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if entries, err := Baseline(folderByID(t, load(t, cfg), "33b4e33d")); err != nil || len(entries) != 0 {
		t.Errorf("entries = %v, err = %v", entries, err)
	}
}

func TestBaseline_JournalLockedByRunningClient(t *testing.T) {
	home, project, base := fixture(t, []journalRow{{"a.txt", 0, "e-a", 1, 1, "id"}})
	cfg := writeConfig(t, base, home, project, "")

	// The desktop client keeps its journal open in exclusive locking mode.
	client, err := sql.Open("sqlite", filepath.Join(home, ".sync_journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	client.SetMaxOpenConns(1)
	mustExec(t, client, `PRAGMA journal_mode=WAL; PRAGMA locking_mode=EXCLUSIVE; UPDATE metadata SET md5 = md5;`)

	f := folderByID(t, load(t, cfg), "783f3376")
	if !f.InUse || f.JournalError != "" {
		t.Errorf("folder should be in use: %+v", f)
	}
	if _, err := Baseline(f); err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Errorf("err = %v", err)
	}
	if !detect([]string{base}, nil)[0].Running {
		t.Error("a locked journal means the client is running")
	}

	_ = client.Close()
	if entries, err := Baseline(folderByID(t, load(t, cfg), "783f3376")); err != nil || len(entries) != 1 {
		t.Errorf("entries = %v, err = %v", entries, err)
	}
}

func TestReadJournal_CleanlyClosedWALCreatesNoFiles(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.db")
	writeJournal(t, plain, []journalRow{{"a.txt", 0, "e-a", 1, 1, "id"}}, "x/")
	conn, err := sql.Open("sqlite", plain)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `PRAGMA journal_mode=WAL;`)
	_ = conn.Close()
	// Characters that need escaping in SQLite URIs.
	name := ".sync journal%#?.db"
	if runtime.GOOS == "windows" {
		name = ".sync journal%#.db"
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(plain, path); err != nil {
		t.Fatal(err)
	}
	if !isWALMode(path) {
		t.Fatal("journal should be in WAL mode")
	}
	before, _ := os.ReadDir(dir)

	j, err := readJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.entries) != 1 || !slices.Equal(j.excluded, []string{"undecided", "x"}) {
		t.Errorf("unexpected journal: %+v", j)
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Errorf("files were created next to the journal: %v", after)
	}
}

func TestJournalFile_FindsHashNamedJournalOfOlderReleases(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, filepath.Join(dir, "._sync_0a1b2c3d4e5f.db"), nil)
	if err := os.WriteFile(filepath.Join(dir, "._sync_0a1b2c3d4e5f.db-wal"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := journalFile(dir, ""); filepath.Base(got) != "._sync_0a1b2c3d4e5f.db" {
		t.Errorf("journal = %q", got)
	}
	if got := journalFile(dir, ".sync_journal.db"); got != "" {
		t.Errorf("a configured but missing journal must not be guessed, got %q", got)
	}
}

func TestBandwidthLimits(t *testing.T) {
	for _, tc := range []struct {
		extra    string
		up, down int64
	}{
		{"\n[BWLimit]\nuseUploadLimit=1\nuploadLimit=250\nuseDownloadLimit=-1\ndownloadLimit=80\n", 250, 0},
		{"\n[BWLimit]\nuseUploadLimit=0\nuploadLimit=250\nuseDownloadLimit=2\ndownloadLimit=80\n", 0, 80},
		{"", 0, 0},
	} {
		dir := t.TempDir()
		c := load(t, writeConfig(t, dir, filepath.Join(dir, "h"), filepath.Join(dir, "p"), tc.extra))
		if c.UploadLimitKBps != tc.up || c.DownloadLimitKBps != tc.down {
			t.Errorf("%q: limits = %d/%d, want %d/%d", tc.extra, c.UploadLimitKBps, c.DownloadLimitKBps, tc.up, tc.down)
		}
	}
}

func TestForServerAndFind(t *testing.T) {
	folder := Folder{ID: "f"}
	clients := []Client{
		{ConfigPath: "/a.cfg", Accounts: []Account{
			{ID: "0", ServerURL: "https://cernbox.cern.ch/cernbox/desktop/", Folders: []Folder{folder}},
			{ID: "1", ServerURL: "https://owncloud.example/", Folders: []Folder{folder}},
			{ID: "2", ServerURL: "https://cernbox.cern.ch/"},
		}},
		{ConfigPath: "/b.cfg", Accounts: []Account{{ID: "0", ServerURL: "https://owncloud.example/", Folders: []Folder{folder}}}},
	}
	kept := ForServer(clients, "https://cernbox.cern.ch")
	if len(kept) != 1 || len(kept[0].Accounts) != 1 || kept[0].Accounts[0].ID != "0" {
		t.Fatalf("unexpected result: %+v", kept)
	}
	if len(clients[0].Accounts) != 3 {
		t.Error("the input must not be modified")
	}
	if _, f, ok := Find(kept, FolderRef{ConfigPath: "/a.cfg", AccountID: "0", FolderID: "f"}); !ok || f.ID != "f" {
		t.Error("folder should be found")
	}
	if _, _, ok := Find(kept, FolderRef{ConfigPath: "/a.cfg", AccountID: "1", FolderID: "f"}); ok {
		t.Error("filtered account should not be found")
	}
}

func TestIsClientProcess(t *testing.T) {
	for p, want := range map[string]bool{
		"cernbox":            true,
		"CERNBox.exe":        true,
		`C:\x\cernbox.EXE`:   true,
		"/opt/x/bin/cernbox": true,
		"cernbox-syncd":      false,
		"cernbox-sync-gu":    false,
	} {
		if got := isClientProcess(p, "cernbox"); got != want {
			t.Errorf("isClientProcess(%q) = %v", p, got)
		}
	}
}
