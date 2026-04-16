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
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/engine"
	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/synclog"
)

// Daemon runs sync cycles on a schedule and serves IPC requests.
type Daemon struct {
	cfgDB    *config.DB
	interval time.Duration

	// cancel is set by Run; calling it shuts the daemon down.
	cancel context.CancelFunc

	mu       sync.Mutex
	syncing  map[string]bool      // folders currently being synced
	lastSync map[string]time.Time // time of last successful sync per folder

	logRotateMaxAge time.Duration // 0 means no rotation; loaded from settings at startup
	accountUsername string        // loaded from settings at startup; updated by set-account
	accountPassword string
}

// New creates a new Daemon. interval controls how often all registered folders
// are synced automatically.
func New(cfgDB *config.DB, interval time.Duration) *Daemon {
	return &Daemon{
		cfgDB:    cfgDB,
		interval: interval,
		syncing:  make(map[string]bool),
		lastSync: make(map[string]time.Time),
	}
}

// Run starts the daemon: removes any stale socket, binds sockPath, runs the
// periodic sync loop, and serves IPC connections until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context, sockPath string) error {
	ctx, d.cancel = context.WithCancel(ctx)

	// Load settings (best-effort; non-fatal if table is missing on old DBs).
	if s, err := d.cfgDB.GetSettings(); err != nil {
		log.Printf("[daemon] load settings: %v", err)
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

	log.Printf("[daemon] listening on %s", sockPath)
	log.Printf("[daemon] sync interval: %s", d.interval)

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
					log.Printf("[daemon] accept: %v", err)
				}
				return
			}
			go d.handleConn(conn)
		}
	}()

	<-ctx.Done()
	ln.Close()
	os.Remove(sockPath)
	log.Printf("[daemon] stopped")
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
		log.Printf("[daemon] list folders: %v", err)
		return
	}
	for _, f := range folders {
		d.syncFolder(f)
	}
}

func (d *Daemon) syncFolder(f config.Folder) {
	d.mu.Lock()
	if d.syncing[f.Name] {
		d.mu.Unlock()
		log.Printf("[daemon] %q already syncing, skipping", f.Name)
		return
	}
	d.syncing[f.Name] = true
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.syncing, f.Name)
		d.lastSync[f.Name] = time.Now()
		d.mu.Unlock()
	}()

	// Open per-folder log; rotate stale entries before the new cycle.
	fl, err := synclog.Open(f.LocalRoot, d.logRotateMaxAge)
	if err != nil {
		log.Printf("[daemon] open folder log for %q: %v", f.Name, err)
	} else {
		if err := fl.Rotate(); err != nil {
			log.Printf("[daemon] rotate folder log for %q: %v", f.Name, err)
		}
		defer fl.Close()
	}

	d.mu.Lock()
	username := d.accountUsername
	password := d.accountPassword
	d.mu.Unlock()

	cfg := engine.Config{
		LocalRoot:  f.LocalRoot,
		RemoteBase: f.RemoteBase,
		Folders:    f.Folders,
		Username:   username,
		Password:   password,
		DBPath:     filepath.Join(f.LocalRoot, ".sync.db"),
		FolderLog:  fl,
	}
	if err := engine.Run(cfg); err != nil {
		log.Printf("[daemon] sync %q: %v", f.Name, err)
		if fl != nil {
			fl.Printf("[sync] ERROR: %v", err)
		}
	}
}

// ── IPC handling ──────────────────────────────────────────────────────────────

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()

	var req ipc.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		log.Printf("[daemon] decode request: %v", err)
		return
	}

	resp := d.dispatch(req)
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("[daemon] encode response: %v", err)
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
		return ok()

	case ipc.CmdList:
		folders, err := d.cfgDB.All()
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Response{OK: true, Folders: folders}

	case ipc.CmdRemove:
		if err := d.cfgDB.Remove(req.Name); err != nil {
			return fail(err.Error())
		}
		return ok()

	case ipc.CmdUpdate:
		abs, err := filepath.Abs(req.Folder.LocalRoot)
		if err != nil {
			return fail("invalid local path: " + err.Error())
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return fail("cannot create local dir: " + err.Error())
		}
		req.Folder.LocalRoot = abs
		if err := d.cfgDB.Update(req.Folder); err != nil {
			return fail(err.Error())
		}
		return ok()

	case ipc.CmdSync:
		var folders []config.Folder
		if req.Name != "" {
			f, err := d.cfgDB.Get(req.Name)
			if err != nil {
				return fail(err.Error())
			}
			if f == nil {
				return fail(fmt.Sprintf("folder %q not found", req.Name))
			}
			folders = []config.Folder{*f}
		} else {
			var err error
			folders, err = d.cfgDB.All()
			if err != nil {
				return fail(err.Error())
			}
			if len(folders) == 0 {
				return fail("no sync folders registered; use 'cernbox-sync add' to add one")
			}
		}
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
		d.mu.Unlock()
		return ipc.Response{OK: true, Status: &ipc.Status{
			Syncing:  syncing,
			LastSync: last,
		}}

	case ipc.CmdStop:
		// cancel() is called in handleConn after the response is sent.
		return ok()

	case ipc.CmdSetSettings:
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
		return ipc.Response{OK: true, Settings: &payload}

	case ipc.CmdGetAccount:
		s, err := d.cfgDB.GetSettings()
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Response{OK: true, Account: &ipc.AccountPayload{
			Username: s.AccountUsername,
			Password: s.AccountPassword,
		}}

	case ipc.CmdSetAccount:
		if req.Account == nil {
			return fail("missing account payload")
		}
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
		return ok()

	default:
		return fail(fmt.Sprintf("unknown command %q", req.Cmd))
	}
}

func ok() ipc.Response             { return ipc.Response{OK: true} }
func fail(msg string) ipc.Response { return ipc.Response{OK: false, Error: msg} }
