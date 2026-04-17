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
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/engine"
	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/logger"
	"github.com/gmgigi96/cernbox-sync/synclog"
)

// Daemon runs sync cycles on a schedule and serves IPC requests.
type Daemon struct {
	cfgDB    *config.DB
	interval time.Duration
	log      *logger.Logger

	// cancel is set by Run; calling it shuts the daemon down.
	cancel context.CancelFunc

	mu       sync.Mutex
	syncing  map[string]bool           // folders currently being synced
	lastSync map[string]time.Time      // time of last successful sync per folder
	counts   map[string]ipc.FileCounts // local file/dir counts after last sync

	logRotateMaxAge time.Duration // 0 means no rotation; loaded from settings at startup
	accountUsername string        // loaded from settings at startup; updated by set-account
	accountPassword string

	// filesystem watcher (auto-sync on change)
	watcher          *fsnotify.Watcher
	watchedRoots     map[string]config.Folder // localRoot → Folder
	watchMu          sync.Mutex
	debounceTimers   map[string]*time.Timer // folderName → pending debounce timer
	debounceMu       sync.Mutex
	debounceDuration time.Duration // how long to wait after the last event before syncing
}

// New creates a new Daemon. interval controls how often all registered folders
// are synced automatically. log is the levelled logger to use; if nil the
// package-level default logger is used.
func New(cfgDB *config.DB, interval time.Duration, log *logger.Logger) *Daemon {
	if log == nil {
		log = logger.GetDefault()
	}
	return &Daemon{
		cfgDB:            cfgDB,
		interval:         interval,
		log:              log,
		syncing:          make(map[string]bool),
		lastSync:         make(map[string]time.Time),
		counts:           make(map[string]ipc.FileCounts),
		watchedRoots:     make(map[string]config.Folder),
		debounceTimers:   make(map[string]*time.Timer),
		debounceDuration: 2 * time.Second,
	}
}

// Run starts the daemon: removes any stale socket, binds sockPath, runs the
// periodic sync loop, and serves IPC connections until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context, sockPath string) error {
	ctx, d.cancel = context.WithCancel(ctx)

	// Load settings (best-effort; non-fatal if table is missing on old DBs).
	if s, err := d.cfgDB.GetSettings(); err != nil {
		d.log.Errorf("[daemon] load settings: %v", err)
	} else {
		d.logRotateMaxAge = s.LogRotateMaxAge
		d.accountUsername = s.AccountUsername
		d.accountPassword = s.AccountPassword
	}

	// Remove stale socket from a previous (crashed) run.
	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}

	d.log.Infof("[daemon] listening on %s", sockPath)
	d.log.Infof("[daemon] sync interval: %s", d.interval)
	d.log.Debugf("[daemon] log level: %s", d.log.Level())

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
					d.log.Errorf("[daemon] accept: %v", err)
				}
				return
			}
			go d.handleConn(conn)
		}
	}()

	<-ctx.Done()
	ln.Close()
	os.Remove(sockPath)
	d.log.Infof("[daemon] stopped")
	return nil
}

// ── sync loop ─────────────────────────────────────────────────────────────────

func (d *Daemon) syncLoop(ctx context.Context) {
	d.syncAll() // run immediately on startup

	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.syncAll()
		}
	}
}

func (d *Daemon) syncAll() {
	folders, err := d.cfgDB.All()
	if err != nil {
		d.log.Errorf("[daemon] list folders: %v", err)
		return
	}
	d.log.Infof("[daemon] starting sync cycle for %d folder(s)", len(folders))
	for _, f := range folders {
		d.syncFolder(f)
	}
	d.log.Infof("[daemon] sync cycle complete")
}

func (d *Daemon) syncFolder(f config.Folder) {
	d.mu.Lock()
	if d.syncing[f.Name] {
		d.mu.Unlock()
		d.log.Debugf("[daemon] %q already syncing, skipping", f.Name)
		return
	}
	d.syncing[f.Name] = true
	d.mu.Unlock()

	start := time.Now()
	d.log.Infof("[daemon] sync start: %q (local=%s remote=%s)", f.Name, f.LocalRoot, f.RemoteBase)

	defer func() {
		c := countLocalEntries(f.LocalRoot)
		d.mu.Lock()
		delete(d.syncing, f.Name)
		d.lastSync[f.Name] = time.Now()
		d.counts[f.Name] = c
		d.mu.Unlock()
		d.log.Infof("[daemon] sync done: %q (elapsed=%s files=%d dirs=%d)", f.Name, time.Since(start).Round(time.Millisecond), c.Files, c.Dirs)
	}()

	// Open per-folder log; rotate stale entries before the new cycle.
	fl, err := synclog.Open(f.LocalRoot, d.logRotateMaxAge)
	if err != nil {
		d.log.Errorf("[daemon] open folder log for %q: %v", f.Name, err)
	} else {
		if err := fl.Rotate(); err != nil {
			d.log.Errorf("[daemon] rotate folder log for %q: %v", f.Name, err)
		}
		defer fl.Close()
	}

	d.mu.Lock()
	username := d.accountUsername
	password := d.accountPassword
	d.mu.Unlock()

	cfg := engine.Config{
		LocalRoot:       f.LocalRoot,
		RemoteBase:      f.RemoteBase,
		Folders:         f.Folders,
		Username:        username,
		Password:        password,
		DBPath:          filepath.Join(f.LocalRoot, ".sync.db"),
		FolderLog:       fl,
		SyncHiddenFiles: f.Settings.SyncHiddenFiles,
	}
	d.log.Debugf("[daemon] running engine for %q", f.Name)
	if err := engine.Run(cfg); err != nil {
		d.log.Errorf("[daemon] sync %q: %v", f.Name, err)
		if fl != nil {
			fl.Printf("[sync] ERROR: %v", err)
		}
	} else {
		d.log.Infof("[daemon] sync %q: OK", f.Name)
	}
}

// ── IPC handling ──────────────────────────────────────────────────────────────

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()

	var req ipc.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		d.log.Errorf("[daemon] decode request: %v", err)
		return
	}

	// Log raw command at trace; summary at debug.
	d.log.Tracef("[daemon] received command %q payload=%+v", req.Cmd, req)
	d.log.Debugf("[daemon] received command %q", req.Cmd)

	resp := d.dispatch(req)

	if resp.OK {
		d.log.Debugf("[daemon] command %q: OK", req.Cmd)
	} else {
		d.log.Errorf("[daemon] command %q: FAILED: %s", req.Cmd, resp.Error)
	}

	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		d.log.Errorf("[daemon] encode response: %v", err)
		return
	}

	// Trigger stop after the response has been flushed.
	if req.Cmd == ipc.CmdStop && resp.OK {
		d.cancel()
	}
}

func (d *Daemon) dispatch(req ipc.Request) ipc.Response {
	switch req.Cmd {

	case ipc.CmdAdd:
		d.log.Debugf("[daemon] add: name=%q local=%q remote=%q", req.Folder.Name, req.Folder.LocalRoot, req.Folder.RemoteBase)
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
		d.log.Infof("[daemon] add: registered folder %q", req.Folder.Name)
		d.updateFolderWatch(req.Folder)
		return ok()

	case ipc.CmdList:
		folders, err := d.cfgDB.All()
		if err != nil {
			return fail(err.Error())
		}
		d.log.Debugf("[daemon] list: returning %d folder(s)", len(folders))
		return ipc.Response{OK: true, Folders: folders}

	case ipc.CmdRemove:
		d.log.Debugf("[daemon] remove: name=%q", req.Name)
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
		d.log.Infof("[daemon] remove: removed folder %q", req.Name)
		return ok()

	case ipc.CmdUpdate:
		d.log.Debugf("[daemon] update: name=%q local=%q remote=%q", req.Folder.Name, req.Folder.LocalRoot, req.Folder.RemoteBase)
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
		d.updateFolderWatch(req.Folder)
		d.log.Infof("[daemon] update: updated folder %q (selected=%v settings=%+v)", req.Folder.Name, req.Folder.Folders, req.Folder.Settings)
		return ok()

	case ipc.CmdSync:
		var folders []config.Folder
		if req.Name != "" {
			d.log.Debugf("[daemon] sync: requested for folder %q", req.Name)
			f, err := d.cfgDB.Get(req.Name)
			if err != nil {
				return fail(err.Error())
			}
			if f == nil {
				return fail(fmt.Sprintf("folder %q not found", req.Name))
			}
			folders = []config.Folder{*f}
		} else {
			d.log.Debugf("[daemon] sync: requested for all folders")
			var err error
			folders, err = d.cfgDB.All()
			if err != nil {
				return fail(err.Error())
			}
			if len(folders) == 0 {
				return fail("no sync folders registered; use 'cernbox-sync add' to add one")
			}
		}
		d.log.Debugf("[daemon] sync: dispatching %d folder(s)", len(folders))
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
		d.mu.Unlock()
		d.log.Debugf("[daemon] status: syncing=%v last=%v", syncing, last)
		return ipc.Response{OK: true, Status: &ipc.Status{
			Syncing:  syncing,
			LastSync: last,
			Counts:   counts,
		}}

	case ipc.CmdStop:
		d.log.Infof("[daemon] stop: shutdown requested")
		// cancel() is called in handleConn after the response is sent.
		return ok()

	case ipc.CmdSetSettings:
		d.log.Debugf("[daemon] set-settings: log_rotate_max_age=%q", req.Settings.LogRotateMaxAge)
		s := config.Settings{}
		if req.Settings.LogRotateMaxAge != "" {
			dur, err := time.ParseDuration(req.Settings.LogRotateMaxAge)
			if err != nil {
				return fail("invalid log_rotate_max_age: " + err.Error())
			}
			s.LogRotateMaxAge = dur
		}
		if err := d.cfgDB.SetSettings(s); err != nil {
			return fail(err.Error())
		}
		d.mu.Lock()
		d.logRotateMaxAge = s.LogRotateMaxAge
		d.mu.Unlock()
		d.log.Infof("[daemon] set-settings: applied log_rotate_max_age=%s", s.LogRotateMaxAge)
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
		d.log.Debugf("[daemon] get-settings: log_rotate_max_age=%s", payload.LogRotateMaxAge)
		return ipc.Response{OK: true, Settings: &payload}

	case ipc.CmdGetAccount:
		s, err := d.cfgDB.GetSettings()
		if err != nil {
			return fail(err.Error())
		}
		d.log.Debugf("[daemon] get-account: username=%q", s.AccountUsername)
		return ipc.Response{OK: true, Account: &ipc.AccountPayload{
			Username: s.AccountUsername,
			Password: s.AccountPassword,
		}}

	case ipc.CmdSetAccount:
		if req.Account == nil {
			return fail("missing account payload")
		}
		d.log.Debugf("[daemon] set-account: username=%q", req.Account.Username)
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
		d.log.Infof("[daemon] set-account: account updated for username=%q", req.Account.Username)
		return ok()

	default:
		d.log.Errorf("[daemon] unknown command %q", req.Cmd)
		return fail(fmt.Sprintf("unknown command %q", req.Cmd))
	}
}

func ok() ipc.Response             { return ipc.Response{OK: true} }
func fail(msg string) ipc.Response { return ipc.Response{OK: false, Error: msg} }

// countLocalEntries walks localRoot and counts files and directories,
// skipping hidden entries (those starting with ".").
func countLocalEntries(localRoot string) ipc.FileCounts {
	var c ipc.FileCounts
	filepath.WalkDir(localRoot, func(path string, d os.DirEntry, err error) error {
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
