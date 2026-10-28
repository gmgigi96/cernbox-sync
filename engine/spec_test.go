package engine_test

// Black-box tests for the "Sync client tests against reva" plan
// (see docs/reva-test-coverage.md). Each test names its plan ID.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/engine"
	"github.com/gmgigi96/cernbox-sync/webdav"
	"golang.org/x/time/rate"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func (e *testEnv) runErr() error { return engine.Run(e.cfg) }

// mustSyncError runs the engine and requires a *engine.SyncError.
func (e *testEnv) mustSyncError() *engine.SyncError {
	e.t.Helper()
	err := e.runErr()
	if err == nil {
		e.t.Fatal("engine.Run: expected an error, got nil")
	}
	var se *engine.SyncError
	if !errors.As(err, &se) {
		e.t.Fatalf("engine.Run: expected *engine.SyncError, got %T: %v", err, err)
	}
	return se
}

// assertSettled runs one more cycle and requires that nothing is transferred,
// moved, created or deleted on either side.
func (e *testEnv) assertSettled(label string) {
	e.t.Helper()
	e.dav.resetSideEffects()
	e.run()
	for _, m := range []string{"GET", "PUT", "DELETE", "MKCOL", "MOVE"} {
		if n := e.dav.count(m); n != 0 {
			e.t.Errorf("%s: second run issued %d %s request(s)", label, n, m)
		}
	}
}

// localTree returns relative path -> content for every regular file and
// relative path -> "<dir>" for every directory under the local root.
func (e *testEnv) localTree() map[string]string {
	e.t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(e.localDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.localDir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel] = "<dir>"
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// tmpLeftovers lists .tmp-sync-* files anywhere under the local root.
func (e *testEnv) tmpLeftovers() []string {
	var out []string
	_ = filepath.WalkDir(e.localDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".tmp-sync-") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// anyConflict reports whether a ".conflict-" file exists anywhere locally.
func (e *testEnv) anyConflict() []string {
	var out []string
	for p := range e.localTree() {
		if strings.Contains(p, ".conflict-") {
			out = append(out, p)
		}
	}
	return out
}

// contentSomewhere reports whether content is still present in any local file
// or any server file (used for "no data loss" assertions).
func (e *testEnv) contentSomewhere(content string) bool {
	for _, c := range e.localTree() {
		if c == content {
			return true
		}
	}
	for _, p := range e.dav.paths() {
		if c, _ := e.dav.contentOf(p); c == content {
			return true
		}
	}
	return false
}

func (e *testEnv) removeLocal(name string) {
	e.t.Helper()
	if err := os.RemoveAll(filepath.Join(e.localDir, filepath.FromSlash(name))); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) renameLocal(from, to string) {
	e.t.Helper()
	if err := os.Rename(filepath.Join(e.localDir, filepath.FromSlash(from)),
		filepath.Join(e.localDir, filepath.FromSlash(to))); err != nil {
		e.t.Fatal(err)
	}
}

// editLocal rewrites a file with new content and a clearly newer mtime.
func (e *testEnv) editLocal(name, content string) {
	e.t.Helper()
	e.writeLocalAt(name, content, time.Now().Add(2*time.Minute))
}

func (e *testEnv) dbHas(path string) bool { return e.dbGet(path) != nil }

// newSharedEnvs creates two independent clients on one fake server.
func newSharedEnvs(t *testing.T) (*fakeWebDAV, *testEnv, *testEnv) {
	t.Helper()
	dav := newFakeWebDAV()
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)
	return dav, setupWithSharedDAV(t, dav, srv.URL), setupWithSharedDAV(t, dav, srv.URL)
}

// addBytes adds a remote file with binary content.
func (f *fakeWebDAV) addBytes(path string, content []byte, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = &fakeFile{
		content: content,
		etag:    etag,
		modTime: time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC),
		fileID:  "fid-" + strings.ReplaceAll(path, "/", "-"),
	}
}

// failGetTruncated makes GETs of path abort mid-body (n<0: always).
func (f *fakeWebDAV) failGetTruncated(path string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, &fakeFault{method: "GET", path: path, truncate: true, remaining: n})
}

// dropRoot makes the remote root collection disappear.
func (f *fakeWebDAV) dropRoot() {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, "")
}

// pseudoRandom is a deterministic reader of n bytes.
func pseudoRandom(seed uint64, n int64) io.Reader {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	return io.LimitReader(readerFunc(func(p []byte) (int, error) {
		for i := range p {
			p[i] = byte(r.Uint32())
		}
		return len(p), nil
	}), n)
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func sumBytes(b []byte) [32]byte { return sha256.Sum256(b) }

// ─── 2.5 – 2.8: transfers ─────────────────────────────────────────────────────

// Plan 2.5: zero-byte file and empty folder, remote -> local.
func TestSpec_2_5_ZeroByteAndEmptyFolder_Download(t *testing.T) {
	env := setup(t)
	env.dav.addFile("empty.txt", "", "e1", time.Now())
	env.dav.addDir("emptydir", "e2")

	env.run()

	info, err := os.Stat(filepath.Join(env.localDir, "empty.txt"))
	if err != nil || info.Size() != 0 {
		t.Fatalf("empty.txt: err=%v info=%v", err, info)
	}
	if !env.localIsDir("emptydir") {
		t.Fatal("emptydir should exist locally")
	}
	env.assertSettled("2.5 download")
}

// Plan 2.5: zero-byte file and empty folder, local -> remote.
func TestSpec_2_5_ZeroByteAndEmptyFolder_Upload(t *testing.T) {
	env := setup(t)
	env.writeLocal("empty.txt", "")
	env.mkdirLocal("emptydir")

	env.run()

	if c, ok := env.dav.contentOf("empty.txt"); !ok || c != "" {
		t.Fatalf("empty.txt on server: ok=%v content=%q", ok, c)
	}
	if !contains(env.dav.mkcols, "emptydir") {
		t.Fatalf("emptydir should be MKCOL'd; mkcols=%v", env.dav.mkcols)
	}
	env.assertSettled("2.5 upload")
}

// Plan 2.6: 50 MB streamed file, remote -> local, content hash checked.
func TestSpec_2_6_LargeFile_Download(t *testing.T) {
	if testing.Short() {
		t.Skip("large file")
	}
	env := setup(t)
	data, err := io.ReadAll(pseudoRandom(1, 50<<20))
	if err != nil {
		t.Fatal(err)
	}
	want := sumBytes(data)
	env.dav.addBytes("big.bin", data, "big1")
	data = nil //nolint:ineffassign

	env.run()

	f, err := os.Open(filepath.Join(env.localDir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, _ := io.Copy(h, f)
	if n != 50<<20 || !bytes.Equal(h.Sum(nil), want[:]) {
		t.Fatalf("downloaded big.bin differs (size %d)", n)
	}
	env.assertSettled("2.6 download")
}

// Plan 2.6: 50 MB streamed file, local -> remote, content hash checked.
func TestSpec_2_6_LargeFile_Upload(t *testing.T) {
	if testing.Short() {
		t.Skip("large file")
	}
	env := setup(t)
	abs := filepath.Join(env.localDir, "big.bin")
	f, err := os.Create(abs)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), pseudoRandom(2, 50<<20)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	env.run()

	env.dav.mu.Lock()
	got := sha256.Sum256(env.dav.files["big.bin"].content)
	size := len(env.dav.files["big.bin"].content)
	env.dav.mu.Unlock()
	if size != 50<<20 || !bytes.Equal(got[:], h.Sum(nil)) {
		t.Fatalf("uploaded big.bin differs (size %d)", size)
	}
	env.assertSettled("2.6 upload")
}

// Plan 2.7: 10,000 small files in one folder, both directions.
func TestSpec_2_7_TenThousandFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("10k files")
	}
	const n = 10000
	t.Run("download", func(t *testing.T) {
		env := setup(t)
		env.cfg.TransferStreams = 8
		env.cfg.MetadataStreams = 8
		env.dav.addDir("many", "d1")
		for i := range n {
			env.dav.addFile(fmt.Sprintf("many/f%05d.txt", i), fmt.Sprintf("c%d", i), "e", time.Now())
		}
		env.run()
		entries, err := os.ReadDir(filepath.Join(env.localDir, "many"))
		if err != nil || len(entries) != n {
			t.Fatalf("local entries: %d err=%v", len(entries), err)
		}
		if got := env.readLocal("many/f04242.txt"); got != "c4242" {
			t.Fatalf("content: %q", got)
		}
		env.assertSettled("2.7 download")
	})
	t.Run("upload", func(t *testing.T) {
		env := setup(t)
		env.cfg.TransferStreams = 8
		env.cfg.MetadataStreams = 8
		for i := range n {
			env.writeLocal(fmt.Sprintf("many/f%05d.txt", i), fmt.Sprintf("c%d", i))
		}
		env.run()
		if len(env.dav.puts) != n {
			t.Fatalf("PUTs: got %d want %d", len(env.dav.puts), n)
		}
		if c, _ := env.dav.contentOf("many/f04242.txt"); c != "c4242" {
			t.Fatalf("server content: %q", c)
		}
		env.assertSettled("2.7 upload")
	})
}

func deepPath(levels int) string {
	segs := make([]string, levels)
	for i := range segs {
		segs[i] = fmt.Sprintf("l%02d", i)
	}
	return strings.Join(segs, "/")
}

// Plan 2.7: 50-level-deep tree, both directions.
func TestSpec_2_7_DeepTree(t *testing.T) {
	deep := deepPath(50)
	t.Run("download", func(t *testing.T) {
		env := setup(t)
		env.cfg.TransferStreams = 8
		for i := 1; i <= 50; i++ {
			env.dav.addDir(deepPath(i), fmt.Sprintf("d%d", i))
		}
		env.dav.addFile(deep+"/leaf.txt", "leaf", "leaf1", time.Now())
		env.run()
		if got := env.readLocal(deep + "/leaf.txt"); got != "leaf" {
			t.Fatalf("leaf: %q", got)
		}
		env.assertSettled("2.7 deep download")
	})
	t.Run("upload", func(t *testing.T) {
		env := setup(t)
		env.cfg.TransferStreams = 8
		env.writeLocal(deep+"/leaf.txt", "leaf")
		env.run()
		if c, ok := env.dav.contentOf(deep + "/leaf.txt"); !ok || c != "leaf" {
			t.Fatalf("server leaf: ok=%v %q", ok, c)
		}
		env.assertSettled("2.7 deep upload")
	})
}

// Plan 2.8: a second Run over an already-synced tree transfers nothing.
func TestSpec_2_8_SecondRunTransfersNothing(t *testing.T) {
	env := setup(t)
	env.dav.addDir("remote-dir", "rd1")
	env.dav.addFile("remote-dir/a.txt", "a", "a1", time.Now())
	env.dav.addFile("top.txt", "top", "t1", time.Now())
	env.writeLocal("local-dir/b.txt", "b")
	env.writeLocal("local.txt", "local")

	env.run()
	env.assertSettled("2.8")
	env.assertSettled("2.8 (third)")
}

// ─── 3.x: renames and moves ───────────────────────────────────────────────────

// Plan 3.1: local file rename = exactly one MOVE, no PUT/DELETE/GET, fileid kept.
func TestSpec_3_1_LocalRenameFile(t *testing.T) {
	for _, origin := range []string{"downloaded", "uploaded"} {
		t.Run(origin, func(t *testing.T) {
			env := setup(t)
			if origin == "downloaded" {
				env.dav.addFile("a.txt", "some content", "a1", time.Now().Add(-time.Hour))
				env.run()
			} else {
				env.writeLocal("a.txt", "some content")
				env.run()
			}
			fid := env.dav.fileIDOf("a.txt")
			env.dav.resetSideEffects()

			env.renameLocal("a.txt", "b.txt")
			env.run()

			if !slices.Equal(env.dav.moves, []string{"a.txt -> b.txt"}) {
				t.Fatalf("moves = %v", env.dav.moves)
			}
			for _, m := range []string{"PUT", "DELETE", "GET"} {
				if n := env.dav.count(m); n != 0 {
					t.Errorf("unexpected %d %s", n, m)
				}
			}
			if env.dav.exists("a.txt") {
				t.Error("a.txt still on server")
			}
			if got := env.dav.fileIDOf("b.txt"); got != fid {
				t.Errorf("fileid changed: %q -> %q", fid, got)
			}
			if env.dbHas("a.txt") || !env.dbHas("b.txt") {
				t.Error("DB should have b.txt and not a.txt")
			}
			env.assertSettled("3.1")
		})
	}
}

// Plan 3.2: renaming a folder containing 1,000 files = one MOVE.
func TestSpec_3_2_LocalRenameFolderWith1000Files(t *testing.T) {
	env := setup(t)
	env.cfg.TransferStreams = 8
	for i := range 1000 {
		env.writeLocal(fmt.Sprintf("proj/f%04d.txt", i), fmt.Sprintf("c%d", i))
	}
	env.run()
	fid := env.dav.fileIDOf("proj/f0500.txt")
	env.dav.resetSideEffects()

	env.renameLocal("proj", "proj2")
	env.run()

	if !slices.Equal(env.dav.moves, []string{"proj -> proj2"}) {
		t.Fatalf("moves = %v", env.dav.moves)
	}
	if n := env.dav.count("PUT") + env.dav.count("DELETE"); n != 0 {
		t.Errorf("unexpected PUT/DELETE: %d", n)
	}
	if got := env.dav.fileIDOf("proj2/f0500.txt"); got != fid {
		t.Errorf("fileid of child changed: %q -> %q", fid, got)
	}
	if env.dav.exists("proj") {
		t.Error("old folder still on server")
	}
	env.assertSettled("3.2")
}

// Plan 3.3: move a file to another folder, and a folder into another folder.
func TestSpec_3_3_LocalMove(t *testing.T) {
	setupTree := func(t *testing.T) *testEnv {
		env := setup(t)
		env.writeLocal("a/f.txt", "file in a")
		env.writeLocal("b/keep.txt", "keep")
		env.writeLocal("x/y.txt", "file in x")
		env.run()
		env.dav.resetSideEffects()
		return env
	}
	t.Run("file", func(t *testing.T) {
		env := setupTree(t)
		fid := env.dav.fileIDOf("a/f.txt")
		env.renameLocal("a/f.txt", "b/f.txt")
		env.run()
		if !slices.Equal(env.dav.moves, []string{"a/f.txt -> b/f.txt"}) {
			t.Fatalf("moves = %v", env.dav.moves)
		}
		if env.dav.count("PUT") != 0 || env.dav.count("DELETE") != 0 {
			t.Errorf("unexpected PUT/DELETE")
		}
		if env.dav.fileIDOf("b/f.txt") != fid {
			t.Error("fileid changed")
		}
		env.assertSettled("3.3 file")
	})
	t.Run("folder", func(t *testing.T) {
		env := setupTree(t)
		fid := env.dav.fileIDOf("x/y.txt")
		env.renameLocal("x", "b/x")
		env.run()
		if !slices.Equal(env.dav.moves, []string{"x -> b/x"}) {
			t.Fatalf("moves = %v", env.dav.moves)
		}
		if env.dav.count("PUT") != 0 || env.dav.count("DELETE") != 0 {
			t.Errorf("unexpected PUT/DELETE")
		}
		if env.dav.fileIDOf("b/x/y.txt") != fid {
			t.Error("fileid changed")
		}
		env.assertSettled("3.3 folder")
	})
}

// Plan 3.4: case-only rename Foo.txt -> foo.txt = MOVE, no duplicate on server.
func TestSpec_3_4_CaseOnlyRename(t *testing.T) {
	env := setup(t)
	env.writeLocal("Foo.txt", "case")
	env.run()
	fid := env.dav.fileIDOf("Foo.txt")
	env.dav.resetSideEffects()

	env.renameLocal("Foo.txt", "foo.txt")
	env.run()

	if !slices.Equal(env.dav.moves, []string{"Foo.txt -> foo.txt"}) {
		t.Fatalf("moves = %v", env.dav.moves)
	}
	if got := env.dav.paths(); !slices.Equal(got, []string{"foo.txt"}) {
		t.Fatalf("server paths = %v", got)
	}
	if env.dav.fileIDOf("foo.txt") != fid || env.dav.count("PUT") != 0 {
		t.Error("expected same fileid and no PUT")
	}
	env.assertSettled("3.4")
}

// Plan 3.5: remote rename (same fileid, new path) -> local rename, zero GETs.
func TestSpec_3_5_RemoteRename(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		env := setup(t)
		env.dav.addFile("a.txt", "content", "a1", time.Now().Add(-time.Hour))
		env.run()
		env.dav.rename("a.txt", "b.txt")
		env.dav.resetSideEffects()

		env.run()

		if env.localExists("a.txt") || env.readLocal("b.txt") != "content" {
			t.Fatalf("local tree = %v", env.localTree())
		}
		if n := env.dav.count("GET"); n != 0 {
			t.Errorf("expected 0 GETs, got %d", n)
		}
		if conf := env.anyConflict(); len(conf) != 0 {
			t.Errorf("conflicts: %v", conf)
		}
		env.assertSettled("3.5 file")
	})
	t.Run("folder", func(t *testing.T) {
		env := setup(t)
		env.dav.addDir("dir", "d1")
		env.dav.addFile("dir/one.txt", "one", "o1", time.Now().Add(-time.Hour))
		env.dav.addFile("dir/two.txt", "two", "t1", time.Now().Add(-time.Hour))
		env.run()
		env.dav.rename("dir", "dir2")
		env.dav.resetSideEffects()

		env.run()

		if env.localExists("dir") || env.readLocal("dir2/one.txt") != "one" || env.readLocal("dir2/two.txt") != "two" {
			t.Fatalf("local tree = %v", env.localTree())
		}
		if n := env.dav.count("GET"); n != 0 {
			t.Errorf("expected 0 GETs, got %d", n)
		}
		env.assertSettled("3.5 folder")
	})
}

// ─── 4.x: special names ───────────────────────────────────────────────────────

func specialNames() []string {
	return []string{
		"héllo wörld.txt",
		"emoji-😀-🎉.txt",
		"日本語のファイル.txt",
		"hash#tag.txt",
		"percent%.txt",
		"%41-looks-encoded.txt",
		"question?.txt",
		"amp&amp.txt",
		"plus+sign.txt",
		"semi;colon.txt",
		"it's.txt",
		`say "hi".txt`,
		"with  double  spaces.txt",
		" leading-space.txt",
		"trailing-space.txt ",
		"trailing-dot.",
		"colon:name.txt",
		`back\slash.txt`,
		"star*.txt",
		"less<than.txt",
		"greater>than.txt",
		"pipe|name.txt",
		strings.Repeat("a", 251) + ".txt", // 255 bytes
		"weird dir #1 %/inner file #2.txt",
	}
}

// Plan 4.1, 4.2, 4.3, 4.4: special names sync both ways, no duplicates, second run no-op.
func TestSpec_4_SpecialNames(t *testing.T) {
	names := specialNames()
	wantServer := slices.Clone(names)
	wantServer = append(wantServer, "weird dir #1 %")
	slices.Sort(wantServer)

	t.Run("download", func(t *testing.T) {
		env := setup(t)
		env.dav.addDir("weird dir #1 %", "wd")
		for i, n := range names {
			env.dav.addFile(n, "content of "+n, fmt.Sprintf("e%d", i), time.Now().Add(-time.Hour))
		}
		env.run()
		for _, n := range names {
			if !env.localExists(n) {
				t.Errorf("missing locally: %q", n)
			} else if got := env.readLocal(n); got != "content of "+n {
				t.Errorf("%q: content %q", n, got)
			}
		}
		env.assertSettled("4 download")
	})
	t.Run("upload", func(t *testing.T) {
		env := setup(t)
		for _, n := range names {
			env.writeLocal(n, "content of "+n)
		}
		env.run()
		if got := env.dav.paths(); !slices.Equal(got, wantServer) {
			t.Fatalf("server paths:\n got  %q\n want %q", got, wantServer)
		}
		for _, n := range names {
			if c, _ := env.dav.contentOf(n); c != "content of "+n {
				t.Errorf("%q: server content %q", n, c)
			}
		}
		env.assertSettled("4 upload")
		if got := env.dav.paths(); !slices.Equal(got, wantServer) {
			t.Fatalf("server paths after 2nd run (duplicates?): %q", got)
		}
	})
}

// ─── 5.5: folder deleted remotely while local content was added ───────────────

// Plan 5.5: a new local file inside a remotely-deleted folder must survive.
func TestSpec_5_5_RemoteFolderDeleted_NewLocalFileKept(t *testing.T) {
	env := setup(t)
	env.dav.addDir("proj", "p1")
	env.dav.addFile("proj/old.txt", "old", "o1", time.Now().Add(-time.Hour))
	env.run()

	env.dav.remove("proj")
	env.writeLocal("proj/new.txt", "brand new local data")

	env.run()

	if got := env.readLocal("proj/new.txt"); got != "brand new local data" {
		t.Fatalf("local new.txt lost or changed: %q", got)
	}
	if c, ok := env.dav.contentOf("proj/new.txt"); !ok || c != "brand new local data" {
		t.Fatalf("new.txt should be uploaded (folder recreated): ok=%v %q; server=%v", ok, c, env.dav.paths())
	}
	env.assertSettled("5.5")
}

// Plan 5.5: a locally modified known file inside a remotely-deleted folder must not be lost.
func TestSpec_5_5_RemoteFolderDeleted_ModifiedLocalFileKept(t *testing.T) {
	env := setup(t)
	env.dav.addDir("proj", "p1")
	env.dav.addFile("proj/old.txt", "old", "o1", time.Now().Add(-time.Hour))
	env.run()

	env.dav.remove("proj")
	env.editLocal("proj/old.txt", "precious local edit")

	env.run()

	if !env.contentSomewhere("precious local edit") {
		t.Fatalf("local edit lost; local=%v server=%v", env.localTree(), env.dav.paths())
	}
}

// ─── 8.x: permissions, roots, multi client ────────────────────────────────────

func setupReadOnly(t *testing.T, filePerms, dirPerms string) *testEnv {
	env := setup(t)
	env.dav.addFile("doc.txt", "server v1", "d1", time.Now().Add(-time.Hour))
	env.dav.addDir("ro", "r1")
	env.dav.addFile("ro/inner.txt", "inner v1", "i1", time.Now().Add(-time.Hour))
	env.dav.mu.Lock()
	for _, e := range env.dav.files {
		if e.isDir {
			e.perms = dirPerms
		} else {
			e.perms = filePerms
		}
	}
	env.dav.mu.Unlock()
	env.run()
	env.dav.resetSideEffects()
	return env
}

func (e *testEnv) assertServerUntouched() {
	e.t.Helper()
	for _, m := range []string{"PUT", "DELETE", "MKCOL", "MOVE"} {
		if n := e.dav.count(m); n != 0 {
			e.t.Errorf("read-only: %d %s request(s) reached the server", n, m)
		}
	}
	if c, _ := e.dav.contentOf("doc.txt"); c != "server v1" {
		e.t.Errorf("doc.txt on server changed: %q", c)
	}
	if c, ok := e.dav.contentOf("ro/inner.txt"); !ok || c != "inner v1" {
		e.t.Errorf("ro/inner.txt on server: ok=%v %q", ok, c)
	}
}

// Plan 8.2 / 8.11: read-only (no W): local modification is not uploaded, server version restored.
func TestSpec_8_2_ReadOnly_ModifyNotUploaded(t *testing.T) {
	env := setupReadOnly(t, "RD", "RDCK")
	env.editLocal("doc.txt", "local edit")

	se := env.mustSyncError()

	env.assertServerUntouched()
	if len(se.Errors) == 0 {
		t.Error("SyncError should list the refused file")
	}
	if got := env.readLocal("doc.txt"); got != "server v1" {
		t.Errorf("server version should be restored locally, got %q", got)
	}
}

// Plan 8.2 / 8.11: no D permission: local delete is not propagated, file restored.
func TestSpec_8_2_ReadOnly_DeleteNotPropagated(t *testing.T) {
	env := setupReadOnly(t, "RWNV", "RCK")
	env.removeLocal("doc.txt")
	env.removeLocal("ro/inner.txt")

	se := env.mustSyncError()

	env.assertServerUntouched()
	if len(se.Errors) == 0 {
		t.Error("SyncError should list refused deletions")
	}
	if got := env.readLocal("doc.txt"); got != "server v1" {
		t.Errorf("doc.txt should be restored locally, got %q", got)
	}
	if got := env.readLocal("ro/inner.txt"); got != "inner v1" {
		t.Errorf("ro/inner.txt should be restored locally, got %q", got)
	}
}

// Plan 8.2 / 8.11: no CK on a folder: new files and folders are not created on the server.
func TestSpec_8_2_ReadOnly_AddNotUploaded(t *testing.T) {
	env := setupReadOnly(t, "RWDNV", "RD")
	env.writeLocal("ro/new.txt", "new local")
	env.writeLocal("ro/subdir/x.txt", "nested")

	se := env.mustSyncError()

	env.assertServerUntouched()
	if env.dav.exists("ro/new.txt") || env.dav.exists("ro/subdir") {
		t.Errorf("new local items reached the server: %v", env.dav.paths())
	}
	if len(se.Errors) == 0 {
		t.Error("SyncError should list refused additions")
	}
	if got := env.readLocal("ro/new.txt"); got != "new local" {
		t.Errorf("local data must not be lost, got %q", got)
	}
}

// Plan 8.3: permissions change between cycles -> no data loss.
func TestSpec_8_3_PermissionsChangeBetweenCycles(t *testing.T) {
	env := setup(t)
	env.dav.addFile("doc.txt", "server v1", "d1", time.Now().Add(-time.Hour))
	env.dav.setPermsAll("RDNVWCK")
	env.run()

	env.editLocal("doc.txt", "edit while rw")
	env.dav.setPerms("doc.txt", "R") // server flips to read-only before the upload
	_ = env.runErr()
	if !env.contentSomewhere("edit while rw") {
		t.Fatalf("data lost after permission drop: local=%v", env.localTree())
	}

	env.dav.setPerms("doc.txt", "RDNVWCK")
	env.editLocal("doc.txt", "second edit")
	_ = env.runErr()
	if !env.contentSomewhere("second edit") {
		t.Fatalf("data lost after permission restore: local=%v", env.localTree())
	}
}

// Plan 8.3: 403 on PUT (permissions not advertised) -> SyncError, nothing lost, DB not advanced.
func TestSpec_8_3_Forbidden_PUT(t *testing.T) {
	env := setup(t)
	env.writeLocal("a.txt", "mine")
	env.dav.failWith("PUT", "a.txt", 403, -1)

	_ = env.mustSyncError()

	if got := env.readLocal("a.txt"); got != "mine" {
		t.Fatalf("local file lost: %q", got)
	}
	if env.dbHas("a.txt") {
		t.Error("DB must not record a.txt after a refused upload")
	}
}

// Plan 8.3: 403 on DELETE -> SyncError, DB not advanced.
func TestSpec_8_3_Forbidden_DELETE(t *testing.T) {
	env := setup(t)
	env.writeLocal("a.txt", "mine")
	env.run()
	env.removeLocal("a.txt")
	env.dav.failWith("DELETE", "a.txt", 403, -1)

	_ = env.mustSyncError()

	if !env.dav.exists("a.txt") {
		t.Error("server file must remain")
	}
	if !env.dbHas("a.txt") {
		t.Error("DB entry must be kept after a refused delete")
	}
}

// Plan 8.4: remote root returns 404/403 -> error, no local file deleted.
func TestSpec_8_4_RemoteRootGone(t *testing.T) {
	for _, mode := range []string{"404", "403"} {
		t.Run(mode, func(t *testing.T) {
			env := setup(t)
			env.dav.addFile("a.txt", "a", "a1", time.Now().Add(-time.Hour))
			env.dav.addDir("d", "d1")
			env.dav.addFile("d/b.txt", "b", "b1", time.Now().Add(-time.Hour))
			env.run()

			if mode == "404" {
				env.dav.dropRoot()
			} else {
				env.dav.failWith("PROPFIND", "*", 403, -1)
			}
			env.dav.resetSideEffects()

			if err := env.runErr(); err == nil {
				t.Fatal("expected an error when the remote root is gone")
			}
			if env.readLocal("a.txt") != "a" || env.readLocal("d/b.txt") != "b" {
				t.Fatalf("local data deleted: %v", env.localTree())
			}
			if n := env.dav.count("DELETE"); n != 0 {
				t.Errorf("%d DELETE(s) sent", n)
			}
		})
	}
}

// Plan 8.5: local root deleted -> error, zero DELETE requests.
func TestSpec_8_5_LocalRootGone(t *testing.T) {
	env := setup(t)
	env.writeLocal("a.txt", "a")
	env.writeLocal("d/b.txt", "b")
	env.run()
	before := env.dav.paths()

	if err := os.RemoveAll(env.localDir); err != nil {
		t.Fatal(err)
	}
	env.dav.resetSideEffects()

	if err := env.runErr(); err == nil {
		t.Fatal("expected an error when the local root does not exist")
	}
	if n := env.dav.count("DELETE"); n != 0 {
		t.Errorf("%d DELETE(s) sent", n)
	}
	if got := env.dav.paths(); !slices.Equal(got, before) {
		t.Errorf("server changed: %v -> %v", before, got)
	}
}

// Plan 8.1: two read-write clients; every change reaches the other in one cycle.
func TestSpec_8_1_TwoClients_OneCyclePropagation(t *testing.T) {
	dav, a, b := newSharedEnvs(t)

	a.writeLocal("proj/sub/file.txt", "from A")
	a.run()
	b.run()
	if got := b.readLocal("proj/sub/file.txt"); got != "from A" {
		t.Fatalf("B: %q", got)
	}

	b.editLocal("proj/sub/file.txt", "edited by B, longer")
	b.writeLocal("proj/fromb.txt", "B new")
	b.run()
	a.run()
	if got := a.readLocal("proj/sub/file.txt"); got != "edited by B, longer" {
		t.Fatalf("A edit: %q", got)
	}
	if got := a.readLocal("proj/fromb.txt"); got != "B new" {
		t.Fatalf("A new: %q", got)
	}

	// Rename on A reaches B in one cycle without re-downloading.
	a.renameLocal("proj/sub", "proj/sub2")
	a.run()
	dav.resetSideEffects()
	b.run()
	if got := b.readLocal("proj/sub2/file.txt"); got != "edited by B, longer" || b.localExists("proj/sub") {
		t.Fatalf("B after rename: %v", b.localTree())
	}
	if n := dav.count("GET"); n != 0 {
		t.Errorf("rename should not re-download: %d GETs", n)
	}

	// Delete on B reaches A in one cycle.
	b.removeLocal("proj/fromb.txt")
	b.run()
	a.run()
	if a.localExists("proj/fromb.txt") {
		t.Error("delete did not propagate to A")
	}
	a.assertSettled("8.1 A")
	b.assertSettled("8.1 B")
}

// ─── 9.x: change detection ────────────────────────────────────────────────────

// Plan 9.1 / 9.2: change 10 levels deep (etags propagated to ancestors) is picked up next cycle.
func TestSpec_9_1_DeepRemoteChange(t *testing.T) {
	env := setup(t)
	deep := deepPath(10)
	for i := 1; i <= 10; i++ {
		env.dav.addDir(deepPath(i), fmt.Sprintf("d%d", i))
	}
	env.dav.addFile(deep+"/f.txt", "v1", "f1", time.Now().Add(-time.Hour))
	env.dav.addFile("l00/other.txt", "other", "o1", time.Now().Add(-time.Hour))
	env.run()

	env.dav.setContent(deep+"/f.txt", []byte("v2 changed deep"))
	env.dav.resetSideEffects()
	env.run()

	if got := env.readLocal(deep + "/f.txt"); got != "v2 changed deep" {
		t.Fatalf("deep change not picked up: %q", got)
	}
	if n := env.dav.count("GET"); n != 1 {
		t.Errorf("expected exactly 1 GET, got %d", n)
	}
	env.assertSettled("9.1")
}

// Plan 9.3: server file changes between PROPFIND and GET -> latest content, no conflict copy.
func TestSpec_9_3_ChangeDuringSync(t *testing.T) {
	env := setup(t)
	env.dav.addFile("f.txt", "v1", "f1", time.Now().Add(-time.Hour))
	var once sync.Once
	env.dav.mu.Lock()
	env.dav.onGet = func(p string) {
		once.Do(func() { env.dav.setContent("f.txt", []byte("version two, longer")) })
	}
	env.dav.mu.Unlock()

	_ = env.runErr()
	if conf := env.anyConflict(); len(conf) != 0 {
		t.Fatalf("conflict copy after first cycle: %v", conf)
	}
	env.run() // next cycle
	if got := env.readLocal("f.txt"); got != "version two, longer" {
		t.Fatalf("latest content not downloaded: %q", got)
	}
	if conf := env.anyConflict(); len(conf) != 0 {
		t.Fatalf("conflict copy after next cycle: %v", conf)
	}
	env.assertSettled("9.3")
}

// Plan 9.4: remote mtime-only change (new etag, same content), unchanged local -> no conflict copy.
func TestSpec_9_4_RemoteMtimeOnlyChange(t *testing.T) {
	env := setup(t)
	env.dav.addFile("f.txt", "same content", "f1", time.Now().Add(-2*time.Hour))
	env.run()

	env.dav.touch("f.txt", time.Now().Add(-time.Hour))
	env.run()

	if conf := env.anyConflict(); len(conf) != 0 {
		t.Fatalf("conflict copy created: %v", conf)
	}
	if got := env.readLocal("f.txt"); got != "same content" {
		t.Fatalf("content: %q", got)
	}
	env.assertSettled("9.4")
}

// ─── 10.x: trash and versions ─────────────────────────────────────────────────

// Plan 10.1: delete locally (propagated), restore from trash on server -> downloaded.
func TestSpec_10_1_RestoreFromTrash(t *testing.T) {
	env := setup(t)
	env.writeLocal("doc.txt", "precious")
	env.run()
	env.removeLocal("doc.txt")
	env.run()
	if env.dav.exists("doc.txt") {
		t.Fatal("deletion should have propagated")
	}

	env.dav.restoreFromTrash("doc.txt", "precious")
	env.run()

	if got := env.readLocal("doc.txt"); got != "precious" {
		t.Fatalf("restored file not downloaded: %q", got)
	}
	env.assertSettled("10.1")
}

// Plan 10.2: overwrite locally (uploaded), server restores the old version -> downloaded, no conflict.
func TestSpec_10_2_RestoreVersion(t *testing.T) {
	env := setup(t)
	env.writeLocalAt("doc.txt", "v1", time.Now().Add(-time.Hour))
	env.run()
	env.editLocal("doc.txt", "v2 overwritten locally")
	env.run()
	if c, _ := env.dav.contentOf("doc.txt"); c != "v2 overwritten locally" {
		t.Fatalf("upload did not happen: %q", c)
	}
	fid := env.dav.fileIDOf("doc.txt")

	env.dav.restoreVersion("doc.txt", "v1")
	env.run()

	if got := env.readLocal("doc.txt"); got != "v1" {
		t.Fatalf("old version not downloaded: %q", got)
	}
	if conf := env.anyConflict(); len(conf) != 0 {
		t.Fatalf("conflict copy: %v", conf)
	}
	if env.dav.fileIDOf("doc.txt") != fid {
		t.Error("fileid should be unchanged")
	}
	env.assertSettled("10.2")
}

// ─── 11.x: locks ──────────────────────────────────────────────────────────────

// Plan 11.1: PUT returns 423 -> SyncError, local file intact, uploaded next cycle.
func TestSpec_11_1_LockedFile(t *testing.T) {
	env := setup(t)
	env.writeLocal("locked.docx", "work in progress")
	env.writeLocal("free.txt", "free")
	env.dav.failWith("PUT", "locked.docx", 423, -1)

	se := env.mustSyncError()

	if len(se.Errors) != 1 || se.Errors[0].Path != "locked.docx" {
		t.Errorf("errors = %v", se.Errors)
	}
	if got := env.readLocal("locked.docx"); got != "work in progress" {
		t.Fatalf("local file damaged: %q", got)
	}
	if c, _ := env.dav.contentOf("free.txt"); c != "free" {
		t.Error("other file should still be uploaded")
	}

	env.dav.clearFaults() // lock released
	env.run()
	if c, _ := env.dav.contentOf("locked.docx"); c != "work in progress" {
		t.Fatalf("retry did not upload: %q", c)
	}
}

// Plan 11.2: editor/office lock files are never synced, with or without hidden files.
func TestSpec_11_2_LockFilesNeverSynced(t *testing.T) {
	lockNames := []string{"~$doc.docx", ".~lock.doc.odt#", "~WRL0001.tmp", "sub/~$inner.xlsx", "sub/.~lock.x.odt#"}
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidden=%v/upload", hidden), func(t *testing.T) {
			env := setup(t)
			env.cfg.SyncHiddenFiles = hidden
			env.writeLocal("doc.docx", "real")
			env.writeLocal("sub/inner.xlsx", "real")
			for _, n := range lockNames {
				env.writeLocal(n, "lock")
			}
			env.run()
			for _, n := range lockNames {
				if env.dav.exists(n) {
					t.Errorf("lock file %q was uploaded", n)
				}
			}
			if !env.dav.exists("doc.docx") || !env.dav.exists("sub/inner.xlsx") {
				t.Errorf("real files missing: %v", env.dav.paths())
			}
		})
		t.Run(fmt.Sprintf("hidden=%v/download", hidden), func(t *testing.T) {
			env := setup(t)
			env.cfg.SyncHiddenFiles = hidden
			env.dav.addDir("sub", "s1")
			env.dav.addFile("doc.docx", "real", "r1", time.Now())
			for i, n := range lockNames {
				env.dav.addFile(n, "lock", fmt.Sprintf("l%d", i), time.Now())
			}
			env.run()
			for _, n := range lockNames {
				if env.localExists(n) {
					t.Errorf("lock file %q was downloaded", n)
				}
			}
			if !env.localExists("doc.docx") {
				t.Error("real file missing")
			}
		})
	}
}

// ─── 12.x: errors ─────────────────────────────────────────────────────────────

// Plan 12.1: PUT 507 -> SyncError, file kept locally, other files still uploaded.
func TestSpec_12_1_InsufficientStorage(t *testing.T) {
	env := setup(t)
	env.writeLocal("big.bin", "too big for quota")
	env.writeLocal("small1.txt", "s1")
	env.writeLocal("small2.txt", "s2")
	env.dav.failWith("PUT", "big.bin", 507, -1)

	se := env.mustSyncError()

	if len(se.Errors) != 1 || se.Errors[0].Path != "big.bin" {
		t.Errorf("errors = %v", se.Errors)
	}
	if got := env.readLocal("big.bin"); got != "too big for quota" {
		t.Fatalf("local file lost: %q", got)
	}
	if !env.dav.exists("small1.txt") || !env.dav.exists("small2.txt") {
		t.Errorf("other files should be uploaded: %v", env.dav.paths())
	}
	if env.dbHas("big.bin") {
		t.Error("DB must not record the failed upload")
	}
}

// Plan 12.2: GET fails mid-body -> no partial file, no .tmp-sync-*, DB not advanced, next Run succeeds.
func TestSpec_12_2_DownloadFailsMidBody(t *testing.T) {
	env := setup(t)
	env.dav.addFile("f.txt", strings.Repeat("0123456789", 1000), "f1", time.Now().Add(-time.Hour))
	env.dav.addFile("ok.txt", "fine", "o1", time.Now().Add(-time.Hour))
	env.dav.failGetTruncated("f.txt", -1)

	se := env.mustSyncError()

	if len(se.Errors) != 1 || se.Errors[0].Path != "f.txt" {
		t.Errorf("errors = %v", se.Errors)
	}
	if env.localExists("f.txt") {
		t.Error("partial f.txt left at destination")
	}
	if l := env.tmpLeftovers(); len(l) != 0 {
		t.Errorf("leftover temp files: %v", l)
	}
	if env.dbHas("f.txt") {
		t.Error("DB advanced for failed download")
	}
	if got := env.readLocal("ok.txt"); got != "fine" {
		t.Error("other file should be downloaded")
	}

	env.dav.clearFaults()
	env.run()
	if got := env.readLocal("f.txt"); got != strings.Repeat("0123456789", 1000) {
		t.Fatalf("retry produced wrong content (len %d)", len(got))
	}
}

// Plan 12.4: one file failing with 500 on every attempt; the others sync; SyncError lists exactly it.
func TestSpec_12_4_OneFileAlwaysFails(t *testing.T) {
	t.Run("upload", func(t *testing.T) {
		env := setup(t)
		for _, n := range []string{"a.txt", "bad.txt", "c.txt"} {
			env.writeLocal(n, "data "+n)
		}
		env.dav.failWith("PUT", "bad.txt", 500, -1)
		se := env.mustSyncError()
		if len(se.Errors) != 1 || se.Errors[0].Path != "bad.txt" {
			t.Fatalf("errors = %v", se.Errors)
		}
		if !env.dav.exists("a.txt") || !env.dav.exists("c.txt") || env.dav.exists("bad.txt") {
			t.Errorf("server = %v", env.dav.paths())
		}
	})
	t.Run("download", func(t *testing.T) {
		env := setup(t)
		for _, n := range []string{"a.txt", "bad.txt", "c.txt"} {
			env.dav.addFile(n, "data "+n, "e-"+n, time.Now().Add(-time.Hour))
		}
		env.dav.failWith("GET", "bad.txt", 500, -1)
		se := env.mustSyncError()
		if len(se.Errors) != 1 || se.Errors[0].Path != "bad.txt" {
			t.Fatalf("errors = %v", se.Errors)
		}
		if !env.localExists("a.txt") || !env.localExists("c.txt") || env.localExists("bad.txt") {
			t.Errorf("local = %v", env.localTree())
		}
	})
}

// Plan 12.5: 429 with Retry-After: 1 is retried within the same cycle.
func TestSpec_12_5_TooManyRequests(t *testing.T) {
	t.Run("upload", func(t *testing.T) {
		env := setup(t)
		env.writeLocal("a.txt", "payload")
		ft := env.dav.failWith("PUT", "a.txt", 429, 1)
		ft.retryAfter = "1"

		start := time.Now()
		env.run()
		elapsed := time.Since(start)

		if c, _ := env.dav.contentOf("a.txt"); c != "payload" {
			t.Fatalf("upload not retried: %q", c)
		}
		if elapsed < 900*time.Millisecond {
			t.Errorf("Retry-After not honoured: %v", elapsed)
		}
		if n := env.dav.count("PUT"); n != 2 {
			t.Errorf("PUT attempts = %d, want 2", n)
		}
	})
	t.Run("download", func(t *testing.T) {
		env := setup(t)
		env.dav.addFile("a.txt", "payload", "a1", time.Now().Add(-time.Hour))
		ft := env.dav.failWith("GET", "a.txt", 429, 1)
		ft.retryAfter = "1"

		start := time.Now()
		env.run()

		if got := env.readLocal("a.txt"); got != "payload" {
			t.Fatalf("download not retried: %q", got)
		}
		if time.Since(start) < 900*time.Millisecond {
			t.Errorf("Retry-After not honoured")
		}
	})
}

// Plan 12.6: local write failure -> clear error, no partial file, DB unchanged.
func TestSpec_12_6_LocalWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	env := setup(t)
	env.dav.addDir("sub", "s1")
	env.dav.addFile("sub/f.txt", "first", "f1", time.Now().Add(-time.Hour))
	env.run()

	env.dav.mu.Lock()
	env.dav.files["sub/g.txt"] = &fakeFile{content: []byte("second"), etag: "g1", modTime: time.Now().Add(-time.Hour), fileID: "fid-g"}
	env.dav.files["sub"].etag = "s2"
	env.dav.mu.Unlock()

	sub := filepath.Join(env.localDir, "sub")
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	se := env.mustSyncError()

	if len(se.Errors) != 1 || se.Errors[0].Path != "sub/g.txt" {
		t.Errorf("errors = %v", se.Errors)
	}
	if env.localExists("sub/g.txt") {
		t.Error("partial g.txt present")
	}
	if l := env.tmpLeftovers(); len(l) != 0 {
		t.Errorf("leftover temp files: %v", l)
	}
	if env.dbHas("sub/g.txt") {
		t.Error("DB advanced for failed download")
	}
	if e := env.dbGet("sub/f.txt"); e == nil || e.ETag != "f1" {
		t.Errorf("unrelated DB entry changed: %v", e)
	}
}

// ─── 1.3: authentication ──────────────────────────────────────────────────────

// Plan 1.3: 401 on PROPFIND -> errors.Is(err, ErrUnauthorized), no DELETE, no local deletion.
func TestSpec_1_3_Unauthorized_Propfind(t *testing.T) {
	env := setup(t)
	env.writeLocal("a.txt", "a")
	env.writeLocal("b.txt", "b")
	env.run()
	env.removeLocal("b.txt") // a pending remote deletion
	env.dav.failWith("PROPFIND", "*", 401, -1)
	env.dav.resetSideEffects()

	err := env.runErr()

	if !errors.Is(err, webdav.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if n := env.dav.count("DELETE"); n != 0 {
		t.Errorf("%d DELETE(s) sent", n)
	}
	if env.readLocal("a.txt") != "a" {
		t.Error("local file touched")
	}
	if !env.dav.exists("b.txt") {
		t.Error("remote b.txt deleted")
	}
}

// Plan 1.3: 401 in the middle of the cycle aborts it before any deletion.
func TestSpec_1_3_Unauthorized_MidCycle(t *testing.T) {
	env := setup(t)
	env.dav.addFile("gone.txt", "will be deleted remotely", "g1", time.Now().Add(-time.Hour))
	env.dav.addFile("keep.txt", "keep", "k1", time.Now().Add(-time.Hour))
	env.run()
	env.dav.remove("gone.txt")
	env.writeLocal("new.txt", "new")
	env.dav.failWith("PUT", "*", 401, -1)
	env.dav.resetSideEffects()

	err := env.runErr()

	if !errors.Is(err, webdav.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if n := env.dav.count("DELETE"); n != 0 {
		t.Errorf("%d DELETE(s) sent", n)
	}
	if env.readLocal("gone.txt") != "will be deleted remotely" || env.readLocal("keep.txt") != "keep" || env.readLocal("new.txt") != "new" {
		t.Errorf("local tree modified: %v", env.localTree())
	}
}

// ─── 13.x: bandwidth and concurrency ──────────────────────────────────────────

// Plan 13.2: rate limiters throttle uploads and downloads (2 MB/s over 4 MB >= ~1.5 s).
func TestSpec_13_2_BandwidthLimit(t *testing.T) {
	data, _ := io.ReadAll(pseudoRandom(3, 4<<20))
	t.Run("upload", func(t *testing.T) {
		env := setup(t)
		env.cfg.UploadLimiter = rate.NewLimiter(rate.Limit(2<<20), 256<<10)
		if err := os.WriteFile(filepath.Join(env.localDir, "big.bin"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		env.run()
		if el := time.Since(start); el < 1500*time.Millisecond {
			t.Errorf("upload took %v, expected >= 1.5s", el)
		}
		if c, _ := env.dav.contentOf("big.bin"); c != string(data) {
			t.Error("uploaded content differs")
		}
	})
	t.Run("download", func(t *testing.T) {
		env := setup(t)
		env.cfg.DownloadLimiter = rate.NewLimiter(rate.Limit(2<<20), 256<<10)
		env.dav.addBytes("big.bin", data, "b1")
		start := time.Now()
		env.run()
		if el := time.Since(start); el < 1500*time.Millisecond {
			t.Errorf("download took %v, expected >= 1.5s", el)
		}
		if env.readLocal("big.bin") != string(data) {
			t.Error("downloaded content differs")
		}
	})
}

// Plan 13.3: TransferStreams 1 and 16 both sync 200 files correctly, both directions.
func TestSpec_13_3_TransferStreams(t *testing.T) {
	for _, streams := range []int{1, 16} {
		t.Run(fmt.Sprintf("streams=%d/download", streams), func(t *testing.T) {
			env := setup(t)
			env.cfg.TransferStreams = streams
			for i := range 200 {
				env.dav.addFile(fmt.Sprintf("d%d/f%03d.txt", i%5, i), fmt.Sprintf("c%d", i), "e", time.Now().Add(-time.Hour))
			}
			for i := range 5 {
				env.dav.addDir(fmt.Sprintf("d%d", i), "d")
			}
			env.run()
			for i := range 200 {
				if got := env.readLocal(fmt.Sprintf("d%d/f%03d.txt", i%5, i)); got != fmt.Sprintf("c%d", i) {
					t.Fatalf("file %d: %q", i, got)
				}
			}
			env.assertSettled("13.3 download")
		})
		t.Run(fmt.Sprintf("streams=%d/upload", streams), func(t *testing.T) {
			env := setup(t)
			env.cfg.TransferStreams = streams
			for i := range 200 {
				env.writeLocal(fmt.Sprintf("d%d/f%03d.txt", i%5, i), fmt.Sprintf("c%d", i))
			}
			env.run()
			for i := range 200 {
				if c, _ := env.dav.contentOf(fmt.Sprintf("d%d/f%03d.txt", i%5, i)); c != fmt.Sprintf("c%d", i) {
					t.Fatalf("file %d: %q", i, c)
				}
			}
			env.assertSettled("13.3 upload")
		})
	}
}
