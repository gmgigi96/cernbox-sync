// Package daemon implements the cernbox-sync background daemon.
//
// The daemon owns the configuration database, runs sync cycles on a
// configurable interval, and accepts IPC connections from CLI clients on a
// Unix domain socket.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/db"
	"github.com/gmgigi96/cernbox-sync/engine"
	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/logger"
	"github.com/gmgigi96/cernbox-sync/synclog"
	"golang.org/x/time/rate"
)

// newLimiter returns a rate.Limiter for the given bandwidth in bytes/sec,
// or nil when bytesPerSec is 0 (unlimited).
func newLimiter(bytesPerSec int64) *rate.Limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	burst := max(int(bytesPerSec/10), 1)
	return rate.NewLimiter(rate.Limit(bytesPerSec), burst)
}

// Daemon runs sync cycles on a schedule and serves IPC requests.
type Daemon struct {
	cfgDB    *config.DB
	interval time.Duration
	log      *slog.Logger

	// cancel is set by Run; calling it shuts the daemon down.
	cancel context.CancelFunc

	mu           sync.Mutex
	syncing      map[string]bool               // folders currently being synced
	syncCancels  map[string]context.CancelFunc // cancel funcs for in-progress syncs
	lastSync     map[string]time.Time          // time of last successful sync per folder
	counts       map[string]ipc.FileCounts     // local file/dir counts after last sync
	globalPaused bool                          // all syncing paused when true

	bus *eventBus // push-event broadcaster

	logRotateMaxAge time.Duration // 0 means no rotation; loaded from settings at startup
	accountUsername string        // loaded from settings at startup; updated by set-account
	accountPassword string

	// Global bandwidth limiters shared across all concurrent syncs.
	// nil means unlimited.
	uploadLimiter   *rate.Limiter
	downloadLimiter *rate.Limiter

	// transferStreams is the number of concurrent upload/download operations.
	// 0 or 1 means sequential.
	transferStreams int
	// metadataStreams is the number of concurrent metadata operations
	// (directory creates and deletes) per depth tier. 0 or 1 means sequential.
	metadataStreams int

	// syncTicker is the periodic sync ticker; reset when the interval changes.
	syncTicker      *time.Ticker
	syncTickerReset chan time.Duration // send a new duration to reset the ticker

	// filesystem watcher (auto-sync on change)
	watcher          *fsnotify.Watcher
	watchedRoots     map[string]config.Folder // localRoot → Folder
	watchMu          sync.Mutex
	debounceTimers   map[string]*time.Timer // folderName → pending debounce timer
	debounceMu       sync.Mutex
	debounceDuration time.Duration // how long to wait after the last event before syncing

	// On-demand sync: one cloudfiles.Provider per sync folder (Windows only;
	// nil on other platforms). Kept alive between sync cycles so the OS
	// callback path remains connected and can hydrate placeholders at any time.
	providerMu sync.Mutex
	providers  map[string]cloudfiles.Provider
}

// New creates a new Daemon. interval controls how often all registered folders
// are synced automatically. log is the slog logger to use; if nil the
// package-level slog default is used.
func New(cfgDB *config.DB, interval time.Duration, log *slog.Logger) *Daemon {
	if log == nil {
		log = slog.Default()
	}
	return &Daemon{
		cfgDB:            cfgDB,
		interval:         interval,
		log:              log,
		syncing:          make(map[string]bool),
		syncCancels:      make(map[string]context.CancelFunc),
		lastSync:         make(map[string]time.Time),
		counts:           make(map[string]ipc.FileCounts),
		watchedRoots:     make(map[string]config.Folder),
		debounceTimers:   make(map[string]*time.Timer),
		debounceDuration: 2 * time.Second,
		syncTickerReset:  make(chan time.Duration, 1),
		bus:              newEventBus(),
		providers:        make(map[string]cloudfiles.Provider),
	}
}

// Run starts the daemon: removes any stale socket, binds sockPath, runs the
// periodic sync loop, and serves IPC connections until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context, sockPath string) error {
	ctx, d.cancel = context.WithCancel(ctx)

	// Load settings (best-effort; non-fatal if table is missing on old DBs).
	if s, err := d.cfgDB.GetSettings(); err != nil {
		d.log.Error("[daemon] load settings", "err", err)
	} else {
		d.logRotateMaxAge = s.LogRotateMaxAge
		d.accountUsername = s.AccountUsername
		d.accountPassword = s.AccountPassword
		if s.SyncInterval > 0 {
			d.interval = s.SyncInterval
		}
		d.uploadLimiter = newLimiter(s.UploadBandwidth)
		d.downloadLimiter = newLimiter(s.DownloadBandwidth)
		d.transferStreams = s.TransferStreams
		d.metadataStreams = s.MetadataStreams
		d.globalPaused = s.GlobalPaused
	}

	// Remove stale socket from a previous (crashed) run.
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}

	d.log.Info("[daemon] listening", "socket", sockPath)
	d.log.Info("[daemon] sync interval", "interval", d.interval)
	d.log.Debug("[daemon] log level", "level", d.log.Handler())

	// Filesystem watcher for auto-sync on change.
	d.startWatcher(ctx)

	// Periodic sync loop.
	go d.syncLoop(ctx)

	// Accept IPC connections.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					// Listener was closed intentionally — exit silently.
				default:
					d.log.Error("[daemon] accept", "err", err)
				}
				return
			}
			go d.handleConn(ctx, conn)
		}
	}()

	<-ctx.Done()
	_ = ln.Close()
	_ = os.Remove(sockPath)
	d.stopAllProviders()
	d.log.Info("[daemon] stopped")
	return nil
}

// ── sync loop ─────────────────────────────────────────────────────────────────

func (d *Daemon) syncLoop(ctx context.Context) {
	d.syncAll() // run immediately on startup

	t := time.NewTicker(d.interval)
	d.syncTicker = t
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-d.syncTickerReset:
			t.Reset(newInterval)
			d.log.Info("[daemon] sync interval updated", "interval", newInterval)
		case <-t.C:
			d.syncAll()
		}
	}
}

func (d *Daemon) syncAll() {
	d.mu.Lock()
	paused := d.globalPaused
	d.mu.Unlock()
	if paused {
		d.log.Info("[daemon] sync cycle skipped — globally paused")
		return
	}

	folders, err := d.cfgDB.All()
	if err != nil {
		d.log.Error("[daemon] list folders", "err", err)
		return
	}
	d.log.Info("[daemon] starting sync cycle", "folders", len(folders))
	for _, f := range folders {
		if f.Settings.Paused {
			d.log.Debug("[daemon] skipping paused folder", "folder", f.Name)
			continue
		}
		d.syncFolder(f)
	}
	d.log.Info("[daemon] sync cycle complete")
}

func (d *Daemon) syncFolder(f config.Folder) {
	d.mu.Lock()
	if d.syncing[f.Name] {
		d.mu.Unlock()
		d.log.Debug("[daemon] already syncing, skipping", "folder", f.Name)
		return
	}
	ctx, cancelSync := context.WithCancel(context.Background())
	d.syncing[f.Name] = true
	d.syncCancels[f.Name] = cancelSync
	d.mu.Unlock()

	start := time.Now()
	d.log.Info("[daemon] sync start", "folder", f.Name, "local", f.LocalRoot, "remote", f.RemoteBase)
	d.bus.publish(ipc.Event{Type: ipc.EventSyncStarted, Folder: f.Name})

	var syncErr error
	defer func() {
		cancelSync() // always release resources
		c := countLocalEntries(f.LocalRoot)
		now := time.Now()
		d.mu.Lock()
		delete(d.syncing, f.Name)
		delete(d.syncCancels, f.Name)
		d.lastSync[f.Name] = now
		d.counts[f.Name] = c
		d.mu.Unlock()
		d.log.Info("[daemon] sync done", "folder", f.Name, "elapsed", time.Since(start).Round(time.Millisecond), "files", c.Files, "dirs", c.Dirs)
		if syncErr != nil {
			d.bus.publish(ipc.Event{Type: ipc.EventSyncFailed, Folder: f.Name, Error: syncErr.Error()})
		} else {
			d.bus.publish(ipc.Event{
				Type:     ipc.EventSyncCompleted,
				Folder:   f.Name,
				LastSync: now.Format(time.RFC3339),
				Counts:   &c,
			})
		}
	}()

	// Open per-folder log; rotate stale entries before the new cycle.
	fl, err := synclog.Open(f.LocalRoot, d.logRotateMaxAge)
	if err != nil {
		d.log.Error("[daemon] open folder log", "folder", f.Name, "err", err)
	} else {
		if err := fl.Rotate(); err != nil {
			d.log.Error("[daemon] rotate folder log", "folder", f.Name, "err", err)
		}
		defer func() { _ = fl.Close() }()
	}

	d.mu.Lock()
	username := d.accountUsername
	password := d.accountPassword
	uploadLimiter := d.uploadLimiter
	downloadLimiter := d.downloadLimiter
	transferStreams := d.transferStreams
	metadataStreams := d.metadataStreams
	d.mu.Unlock()

	placeholder := d.ensureProvider(f)

	cfg := engine.Config{
		Ctx:             ctx,
		LocalRoot:       f.LocalRoot,
		RemoteBase:      f.RemoteBase,
		Folders:         f.Folders,
		Username:        username,
		Password:        password,
		DBPath:          filepath.Join(f.LocalRoot, ".sync.db"),
		FolderLog:       fl,
		Placeholders:    placeholder,
		SyncHiddenFiles: f.Settings.SyncHiddenFiles,
		UploadLimiter:   uploadLimiter,
		DownloadLimiter: downloadLimiter,
		TransferStreams: transferStreams,
		MetadataStreams: metadataStreams,
		OnProgress: func(done, total int, current string, uploadBps, downloadBps, bytesDone, bytesTotal int64) {
			d.bus.publish(ipc.Event{
				Type:   ipc.EventSyncProgress,
				Folder: f.Name,
				Progress: &ipc.SyncProgressPayload{
					Done:        done,
					Total:       total,
					Current:     current,
					UploadBps:   uploadBps,
					DownloadBps: downloadBps,
					BytesDone:   bytesDone,
					BytesTotal:  bytesTotal,
				},
			})
		},
	}
	d.log.Debug("[daemon] running engine", "folder", f.Name)
	if fl != nil {
		fl.Printf("[sync] started")
	}
	if err := engine.Run(cfg); err != nil {
		syncErr = err
		d.log.Error("[daemon] sync failed", "folder", f.Name, "err", err)
		if fl != nil {
			fl.Printf("[sync] ERROR: %v", err)
			fl.Printf("[sync] completed with errors")
		}
	} else {
		d.log.Info("[daemon] sync OK", "folder", f.Name)
		if fl != nil {
			fl.Printf("[sync] completed")
		}
	}
}

// ── IPC handling ──────────────────────────────────────────────────────────────

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	var req ipc.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		d.log.Error("[daemon] decode request", "err", err)
		return
	}

	// Log raw command at trace; summary at debug.
	d.log.Log(ctx, logger.LevelTrace, "[daemon] received command (trace)", "cmd", req.Cmd, "payload", fmt.Sprintf("%+v", req))
	d.log.Debug("[daemon] received command", "cmd", req.Cmd)

	// Subscribe keeps the connection open and streams events — handle separately.
	if req.Cmd == ipc.CmdSubscribe {
		d.handleSubscribe(ctx, conn)
		return
	}

	resp := d.dispatch(req)

	if resp.OK {
		d.log.Debug("[daemon] command OK", "cmd", req.Cmd)
	} else {
		d.log.Error("[daemon] command failed", "cmd", req.Cmd, "error", resp.Error)
	}

	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		d.log.Error("[daemon] encode response", "err", err)
		return
	}

	// Trigger stop after the response has been flushed.
	if req.Cmd == ipc.CmdStop && resp.OK {
		d.cancel()
	}
}

// handleSubscribe sends a full state snapshot and then streams events until
// the client disconnects or the daemon shuts down.
func (d *Daemon) handleSubscribe(ctx context.Context, conn net.Conn) {
	d.log.Debug("[daemon] subscribe: new subscriber")

	// Build initial snapshot.
	folders, err := d.cfgDB.All()
	if err != nil {
		_ = json.NewEncoder(conn).Encode(ipc.SubscribeResponse{OK: false, Error: err.Error()})
		return
	}

	d.mu.Lock()
	syncing := make([]string, 0, len(d.syncing))
	for name := range d.syncing {
		syncing = append(syncing, name)
	}
	last := make(map[string]string, len(d.lastSync))
	for name, t := range d.lastSync {
		last[name] = t.Format(time.RFC3339)
	}
	counts := make(map[string]ipc.FileCounts, len(d.counts))
	maps.Copy(counts, d.counts)
	globalPaused := d.globalPaused
	d.mu.Unlock()
	var pausedFolders []string
	for _, f := range folders {
		if f.Settings.Paused {
			pausedFolders = append(pausedFolders, f.Name)
		}
	}

	snapshot := ipc.SubscribeResponse{
		OK:      true,
		Folders: folders,
		Status: &ipc.Status{
			Syncing:       syncing,
			LastSync:      last,
			Counts:        counts,
			GlobalPaused:  globalPaused,
			PausedFolders: pausedFolders,
		},
	}
	if err := json.NewEncoder(conn).Encode(snapshot); err != nil {
		return
	}

	// Stream events until connection drops or daemon shuts down.
	ch := d.bus.subscribe()
	defer d.bus.unsubscribe(ch)

	enc := json.NewEncoder(conn)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := enc.Encode(event); err != nil {
				return
			}
		}
	}
}

func (d *Daemon) dispatch(req ipc.Request) ipc.Response {
	switch req.Cmd {

	case ipc.CmdAdd:
		d.log.Debug("[daemon] add", "name", req.Folder.Name, "local", req.Folder.LocalRoot, "remote", req.Folder.RemoteBase)
		abs, err := filepath.Abs(req.Folder.LocalRoot)
		if err != nil {
			return fail("invalid local path: " + err.Error())
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return fail("cannot create local dir: " + err.Error())
		}
		req.Folder.LocalRoot = abs
		existing, err := d.cfgDB.GetByRemoteBase(req.Folder.RemoteBase)
		if err != nil {
			return fail(err.Error())
		}
		if existing != nil {
			return fail(fmt.Sprintf("space %q is already registered as sync folder %q", req.Folder.RemoteBase, existing.Name))
		}
		if err := d.cfgDB.Add(req.Folder); err != nil {
			return fail(err.Error())
		}
		d.log.Info("[daemon] add: registered folder", "folder", req.Folder.Name)
		d.updateFolderWatch(req.Folder)
		d.bus.publish(ipc.Event{Type: ipc.EventFolderAdded, FolderData: &req.Folder})
		return ok()

	case ipc.CmdList:
		folders, err := d.cfgDB.All()
		if err != nil {
			return fail(err.Error())
		}
		d.log.Debug("[daemon] list", "count", len(folders))
		return ipc.Response{OK: true, Folders: folders}

	case ipc.CmdRemove:
		d.log.Debug("[daemon] remove", "name", req.Name)
		f, err := d.cfgDB.Get(req.Name)
		if err != nil {
			return fail(err.Error())
		}
		if err := d.cfgDB.Remove(req.Name); err != nil {
			return fail(err.Error())
		}
		if f != nil {
			d.removeFolderWatch(f.Name, f.LocalRoot)
		}
		d.stopFolderProvider(req.Name)
		d.log.Info("[daemon] remove: removed folder", "folder", req.Name)
		d.bus.publish(ipc.Event{Type: ipc.EventFolderRemoved, Folder: req.Name})
		return ok()

	case ipc.CmdUpdate:
		d.log.Debug("[daemon] update", "name", req.Folder.Name, "local", req.Folder.LocalRoot, "remote", req.Folder.RemoteBase)
		abs, err := filepath.Abs(req.Folder.LocalRoot)
		if err != nil {
			return fail("invalid local path: " + err.Error())
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return fail("cannot create local dir: " + err.Error())
		}
		req.Folder.LocalRoot = abs
		// Fetch current config before updating so we can clean up the old watch
		// (LocalRoot may have changed).
		oldFolder, _ := d.cfgDB.Get(req.Folder.Name)
		if err := d.cfgDB.Update(req.Folder); err != nil {
			return fail(err.Error())
		}
		if oldFolder != nil {
			d.removeFolderWatch(oldFolder.Name, oldFolder.LocalRoot)
		}
		// Stop the existing provider so it is re-created on the next sync
		// with the updated LocalRoot / RemoteBase.
		d.stopFolderProvider(req.Folder.Name)
		d.updateFolderWatch(req.Folder)
		d.log.Info("[daemon] update: updated folder", "folder", req.Folder.Name, "selected", req.Folder.Folders, "settings", fmt.Sprintf("%+v", req.Folder.Settings))
		d.bus.publish(ipc.Event{Type: ipc.EventFolderUpdated, FolderData: &req.Folder})
		return ok()

	case ipc.CmdPause:
		if req.Name != "" {
			// Per-folder pause.
			f, err := d.cfgDB.Get(req.Name)
			if err != nil {
				return fail(err.Error())
			}
			if f == nil {
				return fail(fmt.Sprintf("folder %q not found", req.Name))
			}
			f.Settings.Paused = true
			if err := d.cfgDB.Update(*f); err != nil {
				return fail(err.Error())
			}
			// Cancel any running sync for this folder.
			d.mu.Lock()
			if cancel, ok := d.syncCancels[req.Name]; ok {
				cancel()
			}
			d.mu.Unlock()
			d.log.Info("[daemon] pause: paused folder", "folder", req.Name)
			d.bus.publish(ipc.Event{Type: ipc.EventSyncPaused, Folder: req.Name, FolderData: f})
		} else {
			// Global pause.
			d.mu.Lock()
			d.globalPaused = true
			// Cancel all in-progress syncs.
			for _, cancel := range d.syncCancels {
				cancel()
			}
			d.mu.Unlock()
			// Persist.
			s, err := d.cfgDB.GetSettings()
			if err != nil {
				return fail("read settings: " + err.Error())
			}
			s.GlobalPaused = true
			if err := d.cfgDB.SetSettings(s); err != nil {
				return fail(err.Error())
			}
			d.log.Info("[daemon] pause: globally paused")
			d.bus.publish(ipc.Event{Type: ipc.EventSyncPaused})
		}
		return ok()

	case ipc.CmdResume:
		if req.Name != "" {
			// Per-folder resume.
			f, err := d.cfgDB.Get(req.Name)
			if err != nil {
				return fail(err.Error())
			}
			if f == nil {
				return fail(fmt.Sprintf("folder %q not found", req.Name))
			}
			f.Settings.Paused = false
			if err := d.cfgDB.Update(*f); err != nil {
				return fail(err.Error())
			}
			d.log.Info("[daemon] resume: resumed folder", "folder", req.Name)
			d.bus.publish(ipc.Event{Type: ipc.EventSyncResumed, Folder: req.Name, FolderData: f})
		} else {
			// Global resume.
			d.mu.Lock()
			d.globalPaused = false
			d.mu.Unlock()
			s, err := d.cfgDB.GetSettings()
			if err != nil {
				return fail("read settings: " + err.Error())
			}
			s.GlobalPaused = false
			if err := d.cfgDB.SetSettings(s); err != nil {
				return fail(err.Error())
			}
			d.log.Info("[daemon] resume: globally resumed")
			d.bus.publish(ipc.Event{Type: ipc.EventSyncResumed})
		}
		return ok()

	case ipc.CmdSync:
		d.mu.Lock()
		globalPaused := d.globalPaused
		d.mu.Unlock()
		if globalPaused {
			return fail("syncing is globally paused; resume first")
		}
		var folders []config.Folder
		if req.Name != "" {
			d.log.Debug("[daemon] sync: requested for folder", "folder", req.Name)
			f, err := d.cfgDB.Get(req.Name)
			if err != nil {
				return fail(err.Error())
			}
			if f == nil {
				return fail(fmt.Sprintf("folder %q not found", req.Name))
			}
			if f.Settings.Paused {
				return fail(fmt.Sprintf("folder %q is paused; resume it first", req.Name))
			}
			folders = []config.Folder{*f}
		} else {
			d.log.Debug("[daemon] sync: requested for all folders")
			var err error
			folders, err = d.cfgDB.All()
			if err != nil {
				return fail(err.Error())
			}
			if len(folders) == 0 {
				return fail("no sync folders registered; use 'cernbox-sync add' to add one")
			}
			// Filter out individually paused folders.
			var active []config.Folder
			for _, f := range folders {
				if !f.Settings.Paused {
					active = append(active, f)
				}
			}
			folders = active
		}
		d.log.Debug("[daemon] sync: dispatching", "count", len(folders))
		for _, f := range folders {
			go d.syncFolder(f)
		}
		return ok()

	case ipc.CmdStatus:
		d.mu.Lock()
		syncing := make([]string, 0, len(d.syncing))
		for name := range d.syncing {
			syncing = append(syncing, name)
		}
		last := make(map[string]string, len(d.lastSync))
		for name, t := range d.lastSync {
			last[name] = t.Format(time.RFC3339)
		}
		counts := make(map[string]ipc.FileCounts, len(d.counts))
		maps.Copy(counts, d.counts)
		globalPaused := d.globalPaused
		d.mu.Unlock()
		folders, _ := d.cfgDB.All()
		var pausedFolders []string
		for _, f := range folders {
			if f.Settings.Paused {
				pausedFolders = append(pausedFolders, f.Name)
			}
		}
		d.log.Debug("[daemon] status", "syncing", syncing, "last", last)
		return ipc.Response{OK: true, Status: &ipc.Status{
			Syncing:       syncing,
			LastSync:      last,
			Counts:        counts,
			GlobalPaused:  globalPaused,
			PausedFolders: pausedFolders,
		}}

	case ipc.CmdStop:
		d.log.Info("[daemon] stop: shutdown requested")
		// cancel() is called in handleConn after the response is sent.
		return ok()

	case ipc.CmdSetSettings:
		d.log.Debug("[daemon] set-settings", "log_rotate_max_age", req.Settings.LogRotateMaxAge, "sync_interval", req.Settings.SyncInterval, "upload_bw", req.Settings.UploadBandwidth, "download_bw", req.Settings.DownloadBandwidth)
		// Read current settings so fields not included in this request are preserved.
		s, err := d.cfgDB.GetSettings()
		if err != nil {
			return fail("read current settings: " + err.Error())
		}
		if req.Settings.LogRotateMaxAge != "" {
			dur, err := time.ParseDuration(req.Settings.LogRotateMaxAge)
			if err != nil {
				return fail("invalid log_rotate_max_age: " + err.Error())
			}
			s.LogRotateMaxAge = dur
		} else {
			s.LogRotateMaxAge = 0
		}
		var newSyncInterval time.Duration
		if req.Settings.SyncInterval != "" {
			dur, err := time.ParseDuration(req.Settings.SyncInterval)
			if err != nil {
				return fail("invalid sync_interval: " + err.Error())
			}
			if dur <= 0 {
				return fail("sync_interval must be positive")
			}
			s.SyncInterval = dur
			newSyncInterval = dur
		} else {
			s.SyncInterval = 0
		}
		if req.Settings.UploadBandwidth >= 0 {
			s.UploadBandwidth = req.Settings.UploadBandwidth
		}
		if req.Settings.DownloadBandwidth >= 0 {
			s.DownloadBandwidth = req.Settings.DownloadBandwidth
		}
		if req.Settings.TransferStreams >= 0 {
			s.TransferStreams = req.Settings.TransferStreams
		}
		if req.Settings.MetadataStreams >= 0 {
			s.MetadataStreams = req.Settings.MetadataStreams
		}
		if err := d.cfgDB.SetSettings(s); err != nil {
			return fail(err.Error())
		}
		d.mu.Lock()
		d.logRotateMaxAge = s.LogRotateMaxAge
		if newSyncInterval > 0 {
			d.interval = newSyncInterval
		}
		d.uploadLimiter = newLimiter(s.UploadBandwidth)
		d.downloadLimiter = newLimiter(s.DownloadBandwidth)
		d.transferStreams = s.TransferStreams
		d.metadataStreams = s.MetadataStreams
		d.mu.Unlock()
		if newSyncInterval > 0 {
			select {
			case d.syncTickerReset <- newSyncInterval:
			default:
			}
		}
		d.log.Info("[daemon] set-settings: applied", "log_rotate_max_age", s.LogRotateMaxAge, "sync_interval", s.SyncInterval)
		return ok()

	case ipc.CmdGetSettings:
		s, err := d.cfgDB.GetSettings()
		if err != nil {
			return fail(err.Error())
		}
		payload := ipc.SettingsPayload{}
		if s.LogRotateMaxAge > 0 {
			payload.LogRotateMaxAge = s.LogRotateMaxAge.String()
		}
		d.mu.Lock()
		interval := d.interval
		d.mu.Unlock()
		if s.SyncInterval > 0 {
			payload.SyncInterval = s.SyncInterval.String()
		} else {
			payload.SyncInterval = interval.String()
		}
		payload.UploadBandwidth = s.UploadBandwidth
		payload.DownloadBandwidth = s.DownloadBandwidth
		payload.TransferStreams = s.TransferStreams
		payload.MetadataStreams = s.MetadataStreams
		d.log.Debug("[daemon] get-settings", "log_rotate_max_age", payload.LogRotateMaxAge, "sync_interval", payload.SyncInterval, "upload_bw", payload.UploadBandwidth, "download_bw", payload.DownloadBandwidth, "transfer_streams", payload.TransferStreams, "metadata_streams", payload.MetadataStreams)
		return ipc.Response{OK: true, Settings: &payload}

	case ipc.CmdGetAccount:
		s, err := d.cfgDB.GetSettings()
		if err != nil {
			return fail(err.Error())
		}
		d.log.Debug("[daemon] get-account", "username", s.AccountUsername)
		return ipc.Response{OK: true, Account: &ipc.AccountPayload{
			Username: s.AccountUsername,
			Password: s.AccountPassword,
		}}

	case ipc.CmdSetAccount:
		if req.Account == nil {
			return fail("missing account payload")
		}
		d.log.Debug("[daemon] set-account", "username", req.Account.Username)
		s, err := d.cfgDB.GetSettings()
		if err != nil {
			return fail(err.Error())
		}
		s.AccountUsername = req.Account.Username
		s.AccountPassword = req.Account.Password
		if err := d.cfgDB.SetSettings(s); err != nil {
			return fail(err.Error())
		}
		d.mu.Lock()
		d.accountUsername = req.Account.Username
		d.accountPassword = req.Account.Password
		d.mu.Unlock()
		d.log.Info("[daemon] set-account: account updated", "username", req.Account.Username)
		return ok()

	case ipc.CmdListConflicts:
		folders, err := d.cfgDB.All()
		if err != nil {
			return fail(err.Error())
		}
		// Filter to a specific folder if requested.
		if req.Name != "" {
			var filtered []config.Folder
			for _, f := range folders {
				if f.Name == req.Name {
					filtered = append(filtered, f)
					break
				}
			}
			if len(filtered) == 0 {
				return fail(fmt.Sprintf("folder %q not found", req.Name))
			}
			folders = filtered
		}
		var conflicts []ipc.ConflictEntry
		for _, f := range folders {
			dbPath := filepath.Join(f.LocalRoot, ".sync.db")
			sdb, err := db.Open(dbPath)
			if err != nil {
				d.log.Error("[daemon] list-conflicts: open db", "folder", f.Name, "err", err)
				continue
			}
			entries, err := sdb.AllConflicts()
			_ = sdb.Close()
			if err != nil {
				d.log.Error("[daemon] list-conflicts: query", "folder", f.Name, "err", err)
				continue
			}
			for _, e := range entries {
				conflicts = append(conflicts, ipc.ConflictEntry{
					Folder:       f.Name,
					Path:         e.Path,
					ConflictPath: e.ConflictPath,
					CreatedAt:    e.CreatedAt.Format(time.RFC3339),
				})
			}
		}
		d.log.Debug("[daemon] list-conflicts", "count", len(conflicts))
		return ipc.Response{OK: true, Conflicts: conflicts}

	case ipc.CmdPin, ipc.CmdUnpin:
		if req.Name == "" || req.Path == "" {
			return fail("pin/unpin requires both name and path")
		}
		f, err := d.cfgDB.Get(req.Name)
		if err != nil {
			return fail(err.Error())
		}
		if f == nil {
			return fail(fmt.Sprintf("folder %q not found", req.Name))
		}
		if !f.Settings.OnDemand {
			return fail(fmt.Sprintf("folder %q is not on-demand", req.Name))
		}
		sdb, err := db.Open(filepath.Join(f.LocalRoot, ".sync.db"))
		if err != nil {
			return fail(err.Error())
		}
		defer sdb.Close()
		pin := req.Cmd == ipc.CmdPin
		if pin {
			if err := sdb.Pin(req.Path); err != nil {
				return fail(err.Error())
			}
		} else {
			if err := sdb.Unpin(req.Path); err != nil {
				return fail(err.Error())
			}
		}
		// Apply the OS-side pin state best-effort. On non-Windows
		// SetPinState returns ErrUnsupported and we just log it; the DB
		// row above is the source of truth and gets re-applied each cycle.
		abs := filepath.Join(f.LocalRoot, req.Path)
		if err := cloudfiles.SetPinState(abs, pin); err != nil {
			d.log.Warn("[daemon] CF SetPinState",
				"cmd", req.Cmd, "folder", req.Name, "path", req.Path, "err", err)
		}
		d.log.Info("[daemon] pin op", "cmd", req.Cmd, "folder", req.Name, "path", req.Path)
		return ok()

	default:
		d.log.Error("[daemon] unknown command", "cmd", req.Cmd)
		return fail(fmt.Sprintf("unknown command %q", req.Cmd))
	}
}

func ok() ipc.Response             { return ipc.Response{OK: true} }
func fail(msg string) ipc.Response { return ipc.Response{OK: false, Error: msg} }

// countLocalEntries walks localRoot and counts files and directories,
// skipping hidden entries (those starting with ".").
func countLocalEntries(localRoot string) ipc.FileCounts {
	var c ipc.FileCounts
	_ = filepath.WalkDir(localRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == localRoot {
			return nil // skip root itself
		}
		if d.IsDir() {
			c.Dirs++
		} else {
			c.Files++
			if info, err := d.Info(); err == nil {
				c.Size += info.Size()
			}
		}
		return nil
	})
	return c
}
