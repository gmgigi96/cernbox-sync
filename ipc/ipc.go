// Package ipc defines the IPC protocol between the cernbox-sync CLI and the
// cernbox-syncd daemon. Communication happens over a Unix domain socket using
// newline-delimited JSON.
//
// Unix sockets are supported on Linux, macOS, and Windows 10+.
package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/gmgigi96/cernbox-sync/config"
)

// Command names sent in Request.Cmd.
const (
	CmdAdd         = "add"
	CmdList        = "list"
	CmdRemove      = "remove"
	CmdUpdate      = "update"
	CmdSync        = "sync"
	CmdStatus      = "status"
	CmdStop        = "stop"
	CmdSetSettings = "set-settings"
	CmdGetSettings = "get-settings"
	CmdGetAccount  = "get-account"
	CmdSetAccount  = "set-account"
	// CmdSubscribe keeps the connection open and streams push events.
	CmdSubscribe = "subscribe"
	// CmdPause pauses syncing globally (Name=="") or for a specific folder.
	CmdPause = "pause"
	// CmdResume resumes syncing globally (Name=="") or for a specific folder.
	CmdResume = "resume"
	// CmdListConflicts returns all unresolved conflicts, optionally filtered by folder name.
	CmdListConflicts = "list-conflicts"
	// CmdPin marks a path inside an on-demand folder as always-local.
	CmdPin = "pin"
	// CmdUnpin reverts a path back to the default unpinned (lazy) state.
	CmdUnpin = "unpin"
)

// Event type names pushed by the daemon to subscribed clients.
const (
	EventSyncStarted   = "sync_started"
	EventSyncProgress  = "sync_progress"
	EventSyncCompleted = "sync_completed"
	EventSyncFailed    = "sync_failed"
	EventFolderAdded   = "folder_added"
	EventFolderRemoved = "folder_removed"
	EventFolderUpdated = "folder_updated"
	EventSyncPaused    = "sync_paused"
	EventSyncResumed   = "sync_resumed"
)

// SyncProgressPayload describes how far a running sync has progressed.
type SyncProgressPayload struct {
	Done        int    `json:"done"`
	Total       int    `json:"total"`
	Current     string `json:"current,omitempty"`       // relative path of the item being processed
	UploadBps   int64  `json:"upload_bps,omitempty"`    // current upload bytes/sec
	DownloadBps int64  `json:"download_bps,omitempty"`  // current download bytes/sec
	BytesDone   int64  `json:"bytes_done,omitempty"`    // bytes transferred for the current file
	BytesTotal  int64  `json:"bytes_total,omitempty"`   // total bytes for the current file (0 when not a file transfer)
}

// Event is pushed by the daemon to subscribed clients as newline-delimited JSON.
type Event struct {
	Type       string               `json:"type"`
	Folder     string               `json:"folder,omitempty"`
	FolderData *config.Folder       `json:"folder_data,omitempty"`
	Progress   *SyncProgressPayload `json:"progress,omitempty"`
	LastSync   string               `json:"last_sync,omitempty"`
	Counts     *FileCounts          `json:"counts,omitempty"`
	Error      string               `json:"error,omitempty"`
}

// SubscribeResponse is the first message sent by the daemon after a subscribe
// command. It carries a full state snapshot so the client needs no separate
// list/status call.
type SubscribeResponse struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Folders []config.Folder `json:"folders,omitempty"`
	Status  *Status         `json:"status,omitempty"`
}

// SettingsPayload carries daemon settings over IPC.
// Duration values are represented as Go duration strings (e.g. "168h", "720h").
type SettingsPayload struct {
	// LogRotateMaxAge is the maximum age of per-folder log entries.
	// Empty string means no rotation.
	LogRotateMaxAge string `json:"log_rotate_max_age,omitempty"`
	// SyncInterval is the interval between automatic sync cycles.
	// Empty string means use the daemon default (5 minutes).
	SyncInterval string `json:"sync_interval,omitempty"`
	// UploadBandwidth is the maximum upload bandwidth in bytes/sec.
	// 0 means unlimited.
	UploadBandwidth int64 `json:"upload_bandwidth"`
	// DownloadBandwidth is the maximum download bandwidth in bytes/sec.
	// 0 means unlimited.
	DownloadBandwidth int64 `json:"download_bandwidth"`
	// TransferStreams is the number of concurrent upload/download operations
	// per sync cycle. 0 or 1 means sequential.
	TransferStreams int `json:"transfer_streams"`
	// MetadataStreams is the number of concurrent metadata operations
	// (directory creates and deletes) per depth tier. 0 or 1 means sequential.
	MetadataStreams int `json:"metadata_streams"`
}

// AccountPayload carries account credentials over IPC.
type AccountPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Request is sent by the CLI to the daemon.
type Request struct {
	Cmd      string          `json:"cmd"`
	Folder   config.Folder   `json:"folder"`            // used by CmdAdd
	Name     string          `json:"name,omitempty"`    // used by CmdRemove and CmdSync
	Path     string          `json:"path,omitempty"`    // used by CmdPin and CmdUnpin (relative to the folder root)
	Settings SettingsPayload `json:"settings"`          // used by CmdSetSettings
	Account  *AccountPayload `json:"account,omitempty"` // used by CmdSetAccount
}

// ConflictEntry describes a single unresolved conflict.
type ConflictEntry struct {
	Folder       string `json:"folder"`        // registered folder name
	Path         string `json:"path"`          // relative path of the original file
	ConflictPath string `json:"conflict_path"` // absolute path of the .conflict-* copy
	CreatedAt    string `json:"created_at"`    // RFC 3339 timestamp
}

// Response is sent by the daemon back to the CLI.
type Response struct {
	OK        bool             `json:"ok"`
	Error     string           `json:"error,omitempty"`
	Folders   []config.Folder  `json:"folders,omitempty"`   // CmdList
	Status    *Status          `json:"status,omitempty"`    // CmdStatus
	Settings  *SettingsPayload `json:"settings,omitempty"`  // CmdGetSettings
	Account   *AccountPayload  `json:"account,omitempty"`   // CmdGetAccount
	Conflicts []ConflictEntry  `json:"conflicts,omitempty"` // CmdListConflicts
}

// FileCounts holds the number of files and directories synced for a folder.
type FileCounts struct {
	Files int   `json:"files"`
	Dirs  int   `json:"dirs"`
	Size  int64 `json:"size"` // total size in bytes of all local files
}

// Status holds a snapshot of the daemon's sync state.
type Status struct {
	// Syncing is the list of folder names currently being synced.
	Syncing []string `json:"syncing"`
	// LastSync maps folder name → RFC 3339 timestamp of the last completed sync.
	LastSync map[string]string `json:"last_sync"`
	// Counts maps folder name → local file/dir counts after the last sync.
	Counts map[string]FileCounts `json:"counts,omitempty"`
	// GlobalPaused is true when all syncing has been paused globally.
	GlobalPaused bool `json:"global_paused,omitempty"`
	// PausedFolders is the set of individually paused folder names.
	PausedFolders []string `json:"paused_folders,omitempty"`
}

// SocketPath returns the platform-appropriate Unix socket path for the daemon.
//
//   - Linux/other: $XDG_RUNTIME_DIR/cernbox-sync.sock, falling back to
//     $XDG_CACHE_HOME/cernbox-sync/sync.sock
//   - macOS / Windows: <UserCacheDir>/cernbox-sync/sync.sock
func SocketPath() (string, error) {
	// On Linux, prefer XDG_RUNTIME_DIR (RAM-backed, cleaned on logout).
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "cernbox-sync.sock"), nil
	}
	// Fall back to the user cache directory — available on Linux, macOS, Windows.
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine cache directory: %w", err)
	}
	dir := filepath.Join(base, "cernbox-sync")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create cache dir %s: %w", dir, err)
	}
	return filepath.Join(dir, "sync.sock"), nil
}

// Send dials sockPath, sends req, and returns the decoded Response.
func Send(sockPath string, req Request) (*Response, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to daemon at %s (is cernbox-syncd running?): %w", sockPath, err)
	}
	defer func() { _ = conn.Close() }()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("receive response: %w", err)
	}
	return &resp, nil
}
