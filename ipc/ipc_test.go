package ipc_test

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/ipc"
)

// ── SocketPath ────────────────────────────────────────────────────────────────

func TestSocketPath_WithXDGRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	got, err := ipc.SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	want := filepath.Join(dir, "cernbox-sync.sock")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSocketPath_WithoutXDGRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got, err := ipc.SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if !strings.HasSuffix(got, "sync.sock") {
		t.Errorf("fallback path should end with sync.sock, got %q", got)
	}
	if !strings.Contains(got, "cernbox-sync") {
		t.Errorf("fallback path should contain cernbox-sync, got %q", got)
	}
}

// ── Send ──────────────────────────────────────────────────────────────────────

// startFakeServer binds a Unix socket at sockPath, accepts one connection,
// decodes the Request, calls handle(req) to build a Response, and encodes it
// back. It is safe to call before ipc.Send.
func startFakeServer(t *testing.T, sockPath string, handle func(ipc.Request) ipc.Response) {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req ipc.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		resp := handle(req)
		json.NewEncoder(conn).Encode(resp) //nolint:errcheck
	}()
}

func TestSend_Success(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	startFakeServer(t, sockPath, func(req ipc.Request) ipc.Response {
		return ipc.Response{OK: true}
	})

	resp, err := ipc.Send(sockPath, ipc.Request{Cmd: ipc.CmdList})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !resp.OK {
		t.Error("expected OK=true")
	}
}

func TestSend_ErrorResponse(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	startFakeServer(t, sockPath, func(req ipc.Request) ipc.Response {
		return ipc.Response{OK: false, Error: "something went wrong"}
	})

	resp, err := ipc.Send(sockPath, ipc.Request{Cmd: ipc.CmdSync, Name: "myspace"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.OK {
		t.Error("expected OK=false")
	}
	if resp.Error != "something went wrong" {
		t.Errorf("Error: got %q", resp.Error)
	}
}

func TestSend_ServerReceivesCorrectRequest(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	var received ipc.Request
	startFakeServer(t, sockPath, func(req ipc.Request) ipc.Response {
		received = req
		return ipc.Response{OK: true}
	})

	req := ipc.Request{
		Cmd:  ipc.CmdAdd,
		Name: "docs",
		Folder: config.Folder{
			Name:       "docs",
			LocalRoot:  "/home/user/docs",
			RemoteBase: "https://dav.example.com/dav/docs",
		},
	}
	if _, err := ipc.Send(sockPath, req); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if received.Cmd != ipc.CmdAdd {
		t.Errorf("Cmd: got %q, want %q", received.Cmd, ipc.CmdAdd)
	}
	if received.Folder.LocalRoot != "/home/user/docs" {
		t.Errorf("Folder.LocalRoot: got %q", received.Folder.LocalRoot)
	}
}

func TestSend_ResponseWithFolders(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	startFakeServer(t, sockPath, func(req ipc.Request) ipc.Response {
		return ipc.Response{
			OK: true,
			Folders: []config.Folder{
				{Name: "a", LocalRoot: "/a", RemoteBase: "https://dav.example.com/a"},
				{Name: "b", LocalRoot: "/b", RemoteBase: "https://dav.example.com/b"},
			},
		}
	})

	resp, err := ipc.Send(sockPath, ipc.Request{Cmd: ipc.CmdList})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(resp.Folders) != 2 {
		t.Fatalf("expected 2 folders, got %d", len(resp.Folders))
	}
	if resp.Folders[0].Name != "a" || resp.Folders[1].Name != "b" {
		t.Errorf("unexpected folder names: %v", resp.Folders)
	}
}

func TestSend_NoServer_ReturnsError(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	_, err := ipc.Send(sockPath, ipc.Request{Cmd: ipc.CmdList})
	if err == nil {
		t.Fatal("expected error when no server is listening")
	}
}

func TestSend_WithSettingsPayload(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	var received ipc.Request
	startFakeServer(t, sockPath, func(req ipc.Request) ipc.Response {
		received = req
		return ipc.Response{OK: true}
	})

	req := ipc.Request{
		Cmd: ipc.CmdSetSettings,
		Settings: ipc.SettingsPayload{
			SyncInterval:    "10m",
			UploadBandwidth: 1024 * 1024,
			TransferStreams: 4,
		},
	}
	if _, err := ipc.Send(sockPath, req); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.Settings.SyncInterval != "10m" {
		t.Errorf("SyncInterval: got %q", received.Settings.SyncInterval)
	}
	if received.Settings.UploadBandwidth != 1024*1024 {
		t.Errorf("UploadBandwidth: got %d", received.Settings.UploadBandwidth)
	}
}

func TestSend_WithAccountPayload(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	var received ipc.Request
	startFakeServer(t, sockPath, func(req ipc.Request) ipc.Response {
		received = req
		return ipc.Response{OK: true}
	})

	req := ipc.Request{
		Cmd:     ipc.CmdSetAccount,
		Account: &ipc.AccountPayload{Username: "alice", Password: "secret"},
	}
	if _, err := ipc.Send(sockPath, req); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.Account == nil {
		t.Fatal("Account should not be nil")
	}
	if received.Account.Username != "alice" {
		t.Errorf("Username: got %q", received.Account.Username)
	}
}

// ── JSON serialisation of public types ───────────────────────────────────────

func TestEvent_JSONRoundtrip(t *testing.T) {
	evt := ipc.Event{
		Type:   ipc.EventSyncProgress,
		Folder: "myspace",
		Progress: &ipc.SyncProgressPayload{
			Done:        5,
			Total:       10,
			Current:     "docs/file.txt",
			UploadBps:   512,
			DownloadBps: 1024,
			BytesDone:   2048,
			BytesTotal:  4096,
		},
	}
	b, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ipc.Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Type != ipc.EventSyncProgress {
		t.Errorf("Type: got %q", got.Type)
	}
	if got.Progress == nil {
		t.Fatal("Progress should not be nil")
	}
	if got.Progress.Done != 5 || got.Progress.Total != 10 {
		t.Errorf("Progress: got %+v", got.Progress)
	}
	if got.Progress.Current != "docs/file.txt" {
		t.Errorf("Current: got %q", got.Progress.Current)
	}
}

func TestStatus_JSONRoundtrip(t *testing.T) {
	s := ipc.Status{
		Syncing:       []string{"folder-a"},
		LastSync:      map[string]string{"folder-a": "2024-01-01T00:00:00Z"},
		Counts:        map[string]ipc.FileCounts{"folder-a": {Files: 10, Dirs: 2, Size: 4096}},
		GlobalPaused:  true,
		PausedFolders: []string{"folder-b"},
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ipc.Status
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Syncing) != 1 || got.Syncing[0] != "folder-a" {
		t.Errorf("Syncing: got %v", got.Syncing)
	}
	if !got.GlobalPaused {
		t.Error("GlobalPaused should be true")
	}
	if got.Counts["folder-a"].Files != 10 {
		t.Errorf("Counts: got %+v", got.Counts)
	}
}

func TestSubscribeResponse_JSONRoundtrip(t *testing.T) {
	sr := ipc.SubscribeResponse{
		OK: true,
		Folders: []config.Folder{
			{Name: "f1", LocalRoot: "/local/f1", RemoteBase: "https://dav.example.com/f1"},
		},
		Status: &ipc.Status{
			Syncing: []string{},
		},
	}
	b, err := json.Marshal(sr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ipc.SubscribeResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.OK {
		t.Error("OK should be true")
	}
	if len(got.Folders) != 1 || got.Folders[0].Name != "f1" {
		t.Errorf("Folders: got %v", got.Folders)
	}
}

// ── Constants ─────────────────────────────────────────────────────────────────

func TestCommandConstants_AreNonEmpty(t *testing.T) {
	cmds := []string{
		ipc.CmdAdd, ipc.CmdList, ipc.CmdRemove, ipc.CmdUpdate,
		ipc.CmdSync, ipc.CmdStatus, ipc.CmdStop,
		ipc.CmdSetSettings, ipc.CmdGetSettings,
		ipc.CmdGetAccount, ipc.CmdSetAccount,
		ipc.CmdSubscribe, ipc.CmdPause, ipc.CmdResume, ipc.CmdListConflicts,
	}
	seen := map[string]bool{}
	for _, c := range cmds {
		if c == "" {
			t.Error("found empty command constant")
		}
		if seen[c] {
			t.Errorf("duplicate command constant: %q", c)
		}
		seen[c] = true
	}
}

func TestEventConstants_AreNonEmpty(t *testing.T) {
	events := []string{
		ipc.EventSyncStarted, ipc.EventSyncProgress, ipc.EventSyncCompleted,
		ipc.EventSyncFailed, ipc.EventFolderAdded, ipc.EventFolderRemoved,
		ipc.EventFolderUpdated, ipc.EventSyncPaused, ipc.EventSyncResumed,
	}
	seen := map[string]bool{}
	for _, e := range events {
		if e == "" {
			t.Error("found empty event constant")
		}
		if seen[e] {
			t.Errorf("duplicate event constant: %q", e)
		}
		seen[e] = true
	}
}
