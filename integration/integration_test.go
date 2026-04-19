//go:build integration

package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestIntegration_PauseFolderBlocksSync: a paused folder must be rejected when
// a manual sync is requested and must not receive any changes.
func TestIntegration_PauseFolderBlocksSync(t *testing.T) {
	env := setup(t)

	// Establish a baseline so there is something to sync later.
	env.writeRemote("before.txt", "before pause")
	env.triggerSync()
	env.assertInSync()

	// Pause the folder.
	env.pause(folderName)

	if !env.isPaused(folderName) {
		t.Fatal("folder should be reported as paused after pause command")
	}

	// Add a remote file while the folder is paused.
	env.writeRemote("during.txt", "added while paused")

	// Manual sync must be rejected.
	resp := env.trySync(folderName)
	if resp.OK {
		t.Fatal("sync command should have been rejected while folder is paused")
	}

	// The new remote file must not have been downloaded.
	if env.localExists("during.txt") {
		t.Fatal("during.txt should not be present locally while folder is paused")
	}

	// Resume and sync — change must now propagate.
	env.resume(folderName)

	if env.isPaused(folderName) {
		t.Fatal("folder should no longer be paused after resume command")
	}

	env.triggerSync()

	if !env.localExists("during.txt") {
		t.Fatal("during.txt should have been downloaded after resume + sync")
	}
	if got := env.readLocal("during.txt"); got != "added while paused" {
		t.Fatalf("during.txt content: got %q, want %q", got, "added while paused")
	}
	env.assertInSync()
}

// TestIntegration_GlobalPauseBlocksAllSyncs: globally pausing the daemon must
// prevent syncing for all folders and be lifted by a global resume.
func TestIntegration_GlobalPauseBlocksAllSyncs(t *testing.T) {
	env := setup(t)

	// Baseline.
	env.writeRemote("base.txt", "base")
	env.triggerSync()
	env.assertInSync()

	// Global pause.
	env.pause("") // empty name → global pause

	if !env.isGloballyPaused() {
		t.Fatal("daemon should report globally paused")
	}

	// Add a remote file while globally paused.
	env.writeRemote("blocked.txt", "should not arrive")

	// Manual sync must be rejected.
	resp := env.trySync(folderName)
	if resp.OK {
		t.Fatal("sync command should have been rejected while globally paused")
	}

	if env.localExists("blocked.txt") {
		t.Fatal("blocked.txt should not be present while globally paused")
	}

	// Global resume — sync must succeed now.
	env.resume("") // empty name → global resume

	if env.isGloballyPaused() {
		t.Fatal("daemon should no longer be globally paused after resume")
	}

	env.triggerSync()

	if !env.localExists("blocked.txt") {
		t.Fatal("blocked.txt should have been downloaded after global resume + sync")
	}
	env.assertInSync()
}

// TestIntegration_PausePersistsAcrossRestart: a paused folder must still be
// paused after the daemon is restarted (state stored in the config DB).
func TestIntegration_PausePersistsAcrossRestart(t *testing.T) {
	env := setup(t)

	// Baseline.
	env.writeRemote("base.txt", "base")
	env.triggerSync()

	env.pause(folderName)
	if !env.isPaused(folderName) {
		t.Fatal("folder should be paused")
	}

	// Stop and restart the daemon.
	env.stopDaemon()

	configDir := filepath.Join(env.tmpDir, "config")
	runDir := filepath.Join(env.tmpDir, "run")
	logFile, err := os.CreateTemp(env.tmpDir, "daemon2-*.log")
	if err != nil {
		t.Fatalf("create log: %v", err)
	}

	env.daemonCmd = exec.Command(daemonPath,
		"-interval", syncInterval,
		"-socket", env.sockPath,
	)
	env.daemonCmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+runDir,
		"XDG_CONFIG_HOME="+configDir,
	)
	env.daemonCmd.Stdout = logFile
	env.daemonCmd.Stderr = logFile
	if err := env.daemonCmd.Start(); err != nil {
		t.Fatalf("restart daemon: %v", err)
	}
	env.waitDaemonReady()

	// The folder must still be paused after restart.
	if !env.isPaused(folderName) {
		t.Fatal("folder should still be paused after daemon restart")
	}

	// Add a remote file — sync must still be blocked.
	env.writeRemote("after_restart.txt", "after restart")
	resp := env.trySync(folderName)
	if resp.OK {
		t.Fatal("sync should still be rejected after restart while folder is paused")
	}
	if env.localExists("after_restart.txt") {
		t.Fatal("after_restart.txt should not be downloaded while still paused")
	}
}

// TestIntegration_RepeatedSyncsAreNoOp: after a full tree is synced, running
// the sync client three more times with no changes must leave both sides
// identical each time.
func TestIntegration_RepeatedSyncsAreNoOp(t *testing.T) {
	env := setup(t)

	// Build a tree: top-level file, a directory with two files, and a nested sub-dir.
	env.writeRemote("readme.txt", "project readme")
	env.mkdirRemote("docs")
	env.writeRemote("docs/intro.txt", "introduction")
	env.writeRemote("docs/guide.txt", "user guide")
	env.mkdirRemote("docs/api")
	env.writeRemote("docs/api/ref.txt", "api reference")

	// First sync establishes the baseline.
	env.triggerSync()
	env.assertInSync()

	// Three subsequent syncs must all be no-ops.
	for i := range 3 {
		env.triggerSync()
		env.assertInSync()

		// Spot-check a few files to confirm content is unchanged.
		if got := env.readLocal("readme.txt"); got != "project readme" {
			t.Fatalf("run %d: readme.txt changed unexpectedly: %q", i+2, got)
		}
		if got := env.readLocal("docs/api/ref.txt"); got != "api reference" {
			t.Fatalf("run %d: docs/api/ref.txt changed unexpectedly: %q", i+2, got)
		}
	}
}
