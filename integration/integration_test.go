//go:build integration

package integration_test

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestIntegration_RemoteNewFile: a new file appears on the remote side.
// After sync the file must be present locally with the same content.
func TestIntegration_RemoteNewFile(t *testing.T) {
	env := setup(t)

	env.writeRemote("hello.txt", "hello world")
	env.triggerSync()

	if !env.localExists("hello.txt") {
		t.Fatal("hello.txt should have been downloaded")
	}
	if got := env.readLocal("hello.txt"); got != "hello world" {
		t.Fatalf("hello.txt content: got %q, want %q", got, "hello world")
	}
	env.assertInSync()
}

// TestIntegration_RemoteNewDir: a new directory (with files) appears on the remote.
// After sync the full tree must be mirrored locally.
func TestIntegration_RemoteNewDir(t *testing.T) {
	env := setup(t)

	env.mkdirRemote("docs")
	env.writeRemote("docs/readme.txt", "readme content")
	env.writeRemote("docs/guide.txt", "guide content")

	env.triggerSync()

	if !env.localExists("docs") {
		t.Fatal("docs/ should have been created locally")
	}
	if got := env.readLocal("docs/readme.txt"); got != "readme content" {
		t.Fatalf("docs/readme.txt: got %q", got)
	}
	if got := env.readLocal("docs/guide.txt"); got != "guide content" {
		t.Fatalf("docs/guide.txt: got %q", got)
	}
	env.assertInSync()
}

// TestIntegration_LocalNewFile: a new file is created in the local sync root.
// After sync the file must be uploaded to the remote.
func TestIntegration_LocalNewFile(t *testing.T) {
	env := setup(t)

	env.writeLocal("notes.txt", "my notes")
	env.triggerSync()

	if got := env.readRemote("notes.txt"); got != "my notes" {
		t.Fatalf("remote notes.txt: got %q, want %q", got, "my notes")
	}
	env.assertInSync()
}

// TestIntegration_LocalNewDir: a new directory (with files) is created locally.
// After sync the full tree must be present on the remote.
func TestIntegration_LocalNewDir(t *testing.T) {
	env := setup(t)

	env.mkdirLocal("projects")
	env.writeLocal("projects/plan.txt", "project plan")
	env.writeLocal("projects/notes.txt", "project notes")

	env.triggerSync()

	if got := env.readRemote("projects/plan.txt"); got != "project plan" {
		t.Fatalf("remote projects/plan.txt: got %q", got)
	}
	if got := env.readRemote("projects/notes.txt"); got != "project notes" {
		t.Fatalf("remote projects/notes.txt: got %q", got)
	}
	env.assertInSync()
}

// TestIntegration_RemoteFileDeleted: a previously synced file is deleted on the remote.
// After sync the local copy must be removed as well.
func TestIntegration_RemoteFileDeleted(t *testing.T) {
	env := setup(t)

	// Establish a common baseline.
	env.writeRemote("old.txt", "old content")
	env.triggerSync()
	if !env.localExists("old.txt") {
		t.Fatal("setup: old.txt should exist locally after first sync")
	}

	env.deleteRemote("old.txt")
	env.triggerSync()

	if env.localExists("old.txt") {
		t.Fatal("old.txt should have been deleted locally after remote removal")
	}
	env.assertInSync()
}

// TestIntegration_LocalFileDeleted: a previously synced file is deleted locally.
// After sync the remote copy must be removed as well.
func TestIntegration_LocalFileDeleted(t *testing.T) {
	env := setup(t)

	// Establish a common baseline.
	env.writeRemote("old.txt", "old content")
	env.triggerSync()

	env.deleteLocal("old.txt")
	env.triggerSync()

	// Verify that the file no longer appears in the remote listing.
	resources, err := env.testClient.Propfind("", 1)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	for _, r := range resources {
		if r.Path == "old.txt" {
			t.Fatal("old.txt should have been deleted from remote")
		}
	}
	env.assertInSync()
}

// TestIntegration_RemoteFileUpdated: a previously synced file is modified on the remote.
// After sync the local copy must reflect the new content.
func TestIntegration_RemoteFileUpdated(t *testing.T) {
	env := setup(t)

	env.writeRemote("doc.txt", "version 1")
	env.triggerSync()
	if got := env.readLocal("doc.txt"); got != "version 1" {
		t.Fatalf("setup: expected %q after first sync, got %q", "version 1", got)
	}

	env.writeRemote("doc.txt", "version 2")
	env.triggerSync()

	if got := env.readLocal("doc.txt"); got != "version 2" {
		t.Fatalf("doc.txt: got %q, want %q", got, "version 2")
	}
	env.assertInSync()
}

// TestIntegration_LocalFileUpdated: a previously synced file is modified locally.
// After sync the remote copy must reflect the new content.
func TestIntegration_LocalFileUpdated(t *testing.T) {
	env := setup(t)

	env.writeRemote("doc.txt", "version 1")
	env.triggerSync()

	// Use writeLocalAt with a mtime 2 s in the future so the engine's
	// isLocalChanged check (modTime > DB.LastModified + 1s) fires reliably
	// even when both sync cycles complete within the same second.
	env.writeLocalAt("doc.txt", "version 2", time.Now().Add(2*time.Second))
	env.triggerSync()

	if got := env.readRemote("doc.txt"); got != "version 2" {
		t.Fatalf("remote doc.txt: got %q, want %q", got, "version 2")
	}
	env.assertInSync()
}

// TestIntegration_Conflict: both the remote and the local copy are modified
// after a common sync baseline. Server wins: the local copy is renamed with a
// ".conflict-" suffix and the server version is written locally.
func TestIntegration_Conflict(t *testing.T) {
	env := setup(t)

	// Establish a common baseline.
	env.writeRemote("shared.txt", "original")
	env.triggerSync()

	// Modify both sides after the baseline.
	env.writeRemote("shared.txt", "server version")
	env.writeLocal("shared.txt", "local version")

	env.triggerSync()

	if got := env.readLocal("shared.txt"); got != "server version" {
		t.Fatalf("shared.txt should contain the server version; got %q", got)
	}

	// A conflict-renamed copy of the local file must exist.
	entries, err := os.ReadDir(env.localDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	hasConflict := false
	for _, e := range entries {
		if strings.Contains(e.Name(), ".conflict-") {
			hasConflict = true
			break
		}
	}
	if !hasConflict {
		t.Fatal("expected a .conflict- copy of the local file to be created")
	}

	env.assertInSync()
}

// TestIntegration_MixedChanges: multiple simultaneous changes on both sides.
// Baseline: files a, b, c on remote.
// Changes:  delete a from remote, update b on remote, add d locally, delete c locally.
// After sync both sides must converge to the same state (b updated, d present).
func TestIntegration_MixedChanges(t *testing.T) {
	env := setup(t)

	// Establish baseline.
	env.writeRemote("a.txt", "file a")
	env.writeRemote("b.txt", "file b")
	env.writeRemote("c.txt", "file c")
	env.triggerSync()

	// Apply changes on both sides.
	env.deleteRemote("a.txt")
	env.writeRemote("b.txt", "file b updated")
	env.writeLocal("d.txt", "file d")
	env.deleteLocal("c.txt")

	env.triggerSync()

	env.assertInSync()
}

// TestIntegration_DeepNestedTree: a multi-level directory tree on the remote.
// After sync the entire tree must be mirrored in the local directory.
func TestIntegration_DeepNestedTree(t *testing.T) {
	env := setup(t)

	env.mkdirRemote("a")
	env.mkdirRemote("a/b")
	env.mkdirRemote("a/b/c")
	env.writeRemote("a/file1.txt", "level 1")
	env.writeRemote("a/b/file2.txt", "level 2")
	env.writeRemote("a/b/c/file3.txt", "level 3")

	env.triggerSync()

	if got := env.readLocal("a/b/c/file3.txt"); got != "level 3" {
		t.Fatalf("a/b/c/file3.txt: got %q, want %q", got, "level 3")
	}
	env.assertInSync()
}

// TestIntegration_SecondSyncIsNoOp: after a clean first sync, a second sync
// cycle must leave both sides unchanged.
func TestIntegration_SecondSyncIsNoOp(t *testing.T) {
	env := setup(t)

	env.writeRemote("stable.txt", "stable content")
	env.triggerSync()
	env.assertInSync()

	// Second sync — nothing should change.
	env.triggerSync()
	env.assertInSync()

	if got := env.readLocal("stable.txt"); got != "stable content" {
		t.Fatalf("stable.txt should be unchanged after second sync; got %q", got)
	}
}
