// Package engine implements the bidirectional sync algorithm.
//
// High-level flow
// ───────────────
//  1. Walk the remote tree with recursive PROPFIND (Depth 1, BFS).
//  2. Walk the local filesystem tree.
//  3. Load the last-known state from the SQLite DB.
//  4. Classify every path into one of:
//     - remoteNew   : exists remotely, not in DB          → download
//     - remoteUpdated: etag changed vs DB                 → download
//     - remoteDeleted: in DB, not in remote scan          → delete local
//     - localNew   : exists locally, not in DB            → upload
//     - localUpdated: mtime/size changed vs DB            → upload
//     - localDeleted: in DB, not on disk                  → delete remote
//     - conflict   : changed both locally & remotely      → server wins, rename local copy
//     - inSync     : nothing changed                      → no-op
//  5. Execute the actions in safe order:
//     dirs before files (create), files before dirs (delete).
package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gmgigi96/cernbox-sync/db"
	"github.com/gmgigi96/cernbox-sync/synclog"
	"github.com/gmgigi96/cernbox-sync/webdav"
	"golang.org/x/time/rate"
)

// rateLimitedReader wraps an io.Reader and throttles reads using a token-bucket
// limiter. burstSize is the maximum number of bytes consumed per single WaitN
// call; using a fixed burst avoids having to split very large reads.
type rateLimitedReader struct {
	r         io.Reader
	lim       *rate.Limiter
	burstSize int
}

func newRateLimitedReader(r io.Reader, lim *rate.Limiter) io.Reader {
	if lim == nil {
		return r
	}
	return &rateLimitedReader{r: r, lim: lim, burstSize: lim.Burst()}
}

func (rl *rateLimitedReader) Read(p []byte) (int, error) {
	// Cap the read to the burst size so WaitN never exceeds the limiter's burst.
	if len(p) > rl.burstSize {
		p = p[:rl.burstSize]
	}
	n, err := rl.r.Read(p)
	if n > 0 {
		if waitErr := rl.lim.WaitN(context.Background(), n); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

// Config holds the parameters for a sync session.
type Config struct {
	// LocalRoot is the absolute path of the local directory to sync.
	LocalRoot string
	// RemoteBase is the full WebDAV URL of the remote root directory.
	RemoteBase string
	// Folders is the list of top-level sub-folder names (relative to
	// RemoteBase) to synchronize. An empty slice means "sync everything".
	Folders []string
	// Username / Password for basic auth.
	Username string
	Password string
	// DBPath is the path to the SQLite state database.
	DBPath string
	// FolderLog is the per-folder logger. When non-nil, sync actions are
	// written to it in addition to the global logger.
	FolderLog *synclog.Logger
	// SyncHiddenFiles controls whether files and directories whose names begin
	// with a dot are included in the sync. Defaults to false.
	SyncHiddenFiles bool
	// UploadLimiter is the global rate limiter for uploads. nil means unlimited.
	UploadLimiter *rate.Limiter
	// DownloadLimiter is the global rate limiter for downloads. nil means unlimited.
	DownloadLimiter *rate.Limiter
	// OnProgress is called before each action is executed. done is the number
	// of actions completed so far, total is the total action count, current is
	// the relative path being processed. May be nil.
	OnProgress func(done, total int, current string)
}

// action classifies what needs to happen to a path.
type actionKind int

const (
	download     actionKind = iota // remote → local
	upload                         // local → remote
	deleteLocal                    // remote was deleted; remove local
	deleteRemote                   // local was deleted; remove remote
	mkcolRemote                    // create remote directory
	mkdirLocal                     // create local directory
	conflictTake                   // conflict: server wins, rename local
)

type action struct {
	kind   actionKind
	path   string // relative, forward-slash separated
	isDir  bool
	remote *webdav.Resource
	local  *localInfo
}

type localInfo struct {
	path    string // absolute
	size    int64
	modTime time.Time
	isDir   bool
}

// logf writes to both the slog default logger and, when set, the per-folder logger.
func logf(fl *synclog.Logger, format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
	if fl != nil {
		fl.Printf(format, args...)
	}
}

// Run executes one full sync cycle.
func Run(cfg Config) error {
	logf(cfg.FolderLog, "[sync] starting — local: %s  remote: %s", cfg.LocalRoot, cfg.RemoteBase)

	wdc := webdav.NewClient(cfg.RemoteBase, cfg.Username, cfg.Password)

	state, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer state.Close()

	// ── 1. Remote scan ───────────────────────────────────────────────────────
	remoteMap, err := scanRemote(wdc, cfg.Folders, cfg.SyncHiddenFiles)
	if err != nil {
		return fmt.Errorf("remote scan: %w", err)
	}
	logf(cfg.FolderLog, "[sync] remote resources: %d", len(remoteMap))

	// ── 2. Local scan ────────────────────────────────────────────────────────
	localMap, err := scanLocal(cfg.LocalRoot, cfg.SyncHiddenFiles)
	if err != nil {
		return fmt.Errorf("local scan: %w", err)
	}
	logf(cfg.FolderLog, "[sync] local resources: %d", len(localMap))

	// ── 3. Load DB state ─────────────────────────────────────────────────────
	dbState, err := state.All()
	if err != nil {
		return fmt.Errorf("load db: %w", err)
	}
	logf(cfg.FolderLog, "[sync] db entries: %d", len(dbState))

	// ── 4. Classify ──────────────────────────────────────────────────────────
	actions := classify(remoteMap, localMap, dbState)
	logf(cfg.FolderLog, "[sync] actions: %d", len(actions))

	// ── 5. Execute ───────────────────────────────────────────────────────────
	if err := execute(cfg.LocalRoot, cfg.FolderLog, wdc, state, actions, cfg.OnProgress, cfg.UploadLimiter, cfg.DownloadLimiter); err != nil {
		return fmt.Errorf("execute: %w", err)
	}

	logf(cfg.FolderLog, "[sync] done")
	return nil
}

// ─── remote scan ─────────────────────────────────────────────────────────────

// scanRemote does a BFS PROPFIND (Depth 1) to build the remote tree.
// Returns a map of relative path → Resource.
// The root itself (empty path "") is included.
//
// If folders is non-empty only those top-level sub-directories (and their
// descendants) are visited; the root entry is still included so the engine
// can track the anchor point.
func scanRemote(wdc *webdav.Client, folders []string, syncHiddenFiles bool) (map[string]*webdav.Resource, error) {
	result := make(map[string]*webdav.Resource)

	// Seed the BFS queue.
	// When a folder filter is active we start directly from each selected
	// sub-folder instead of the root, but we still record the root entry so
	// the rest of the algorithm has a stable anchor.
	var queue []string
	if len(folders) == 0 {
		// No filter — traverse everything from the root.
		queue = []string{""}
	} else {
		// Record the root resource without recursing into all children.
		rootEntries, err := wdc.Propfind("", 1)
		if err != nil {
			return nil, fmt.Errorf("propfind root: %w", err)
		}
		if len(rootEntries) > 0 {
			result[""] = &rootEntries[0]
		}
		// Seed the queue with only the selected sub-folders.
		for _, f := range folders {
			queue = append(queue, f)
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		entries, err := wdc.Propfind(cur, 1)
		if err != nil {
			return nil, fmt.Errorf("propfind %q: %w", cur, err)
		}

		for i := range entries {
			e := &entries[i]
			// The first entry with an empty path is the root itself; normalise.
			rel := e.Path
			if rel == "" && cur != "" {
				rel = cur
			}

			if _, seen := result[rel]; seen {
				continue
			}

			// Skip hidden entries unless enabled.
			if !syncHiddenFiles {
				name := rel
				if idx := strings.LastIndex(rel, "/"); idx >= 0 {
					name = rel[idx+1:]
				}
				if isHidden(name, "") {
					continue
				}
			}

			result[rel] = e

			// Recurse into subdirectories (but not the entry we just came from).
			if e.IsDir && rel != cur {
				queue = append(queue, rel)
			}
		}
	}

	return result, nil
}

// ─── local scan ──────────────────────────────────────────────────────────────

func scanLocal(root string, syncHiddenFiles bool) (map[string]*localInfo, error) {
	result := make(map[string]*localInfo)
	err := filepath.WalkDir(root, func(absPath string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return err
		}
		// Convert OS path separator to forward slash for consistency.
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}

		// Skip internal files — they live inside the local root but must
		// never be uploaded or treated as sync-able files.
		if rel == ".sync.db" || rel == ".sync.log" {
			return nil
		}

		// Skip hidden entries unless enabled.
		if !syncHiddenFiles && isHidden(d.Name(), absPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		result[rel] = &localInfo{
			path:    absPath,
			size:    info.Size(),
			modTime: info.ModTime(),
			isDir:   d.IsDir(),
		}
		return nil
	})
	return result, err
}

// ─── classify ────────────────────────────────────────────────────────────────

func classify(
	remoteMap map[string]*webdav.Resource,
	localMap map[string]*localInfo,
	dbState map[string]*db.Entry,
) []action {
	var actions []action
	visited := make(map[string]bool)

	// Paths known remotely.
	for path, rem := range remoteMap {
		if path == "" {
			continue // skip root placeholder
		}
		visited[path] = true

		dbEntry := dbState[path]
		loc := localMap[path]

		switch {
		case dbEntry == nil && loc == nil:
			// New on remote, not known locally → download.
			a := action{path: path, isDir: rem.IsDir, remote: rem}
			if rem.IsDir {
				a.kind = mkdirLocal
			} else {
				a.kind = download
			}
			actions = append(actions, a)

		case dbEntry == nil && loc != nil:
			// Remote has it, local has it, DB doesn't → ambiguous first run;
			// treat remote as authoritative only if etags differ.
			// Since we have no etag baseline, trust local (upload wins).
			a := action{path: path, isDir: loc.isDir, local: loc}
			if loc.isDir {
				a.kind = mkcolRemote
			} else {
				a.kind = upload
			}
			actions = append(actions, a)

		case dbEntry != nil && loc == nil:
			// In DB and remote but not local → local was deleted → delete remote.
			actions = append(actions, action{
				kind:  deleteRemote,
				path:  path,
				isDir: rem.IsDir,
			})

		default:
			// dbEntry != nil && loc != nil
			remoteChanged := rem.ETag != dbEntry.ETag
			localChanged := isLocalChanged(loc, dbEntry)

			switch {
			case remoteChanged && localChanged:
				// Conflict: server wins; rename local copy.
				actions = append(actions, action{
					kind:   conflictTake,
					path:   path,
					isDir:  rem.IsDir,
					remote: rem,
					local:  loc,
				})
			case remoteChanged:
				a := action{path: path, isDir: rem.IsDir, remote: rem}
				if rem.IsDir {
					a.kind = mkdirLocal
				} else {
					a.kind = download
				}
				actions = append(actions, a)
			case localChanged:
				a := action{path: path, isDir: loc.isDir, local: loc}
				if loc.isDir {
					a.kind = mkcolRemote
				} else {
					a.kind = upload
				}
				actions = append(actions, a)
				// else: in sync, nothing to do
			}
		}
	}

	// Paths known locally but not remotely.
	for path, loc := range localMap {
		if path == "" {
			continue
		}
		if visited[path] {
			continue
		}
		dbEntry := dbState[path]

		if dbEntry == nil {
			// New local file/dir → upload.
			a := action{path: path, isDir: loc.isDir, local: loc}
			if loc.isDir {
				a.kind = mkcolRemote
			} else {
				a.kind = upload
			}
			actions = append(actions, a)
		} else {
			// Was in DB, was on remote, now only local → remote deleted → delete local.
			actions = append(actions, action{
				kind:  deleteLocal,
				path:  path,
				isDir: loc.isDir,
				local: loc,
			})
		}
	}

	// DB entries that are gone from both sides — clean up DB (handled during execute).

	sortActions(actions)
	return actions
}

// isLocalChanged returns true if the local file differs from the DB baseline.
func isLocalChanged(loc *localInfo, e *db.Entry) bool {
	if loc.isDir {
		return false // directories don't have meaningful content changes
	}
	return loc.size != e.Size || loc.modTime.After(e.LastModified.Add(time.Second))
}

// sortActions orders actions so that:
//   - directory creations come before file operations inside them
//   - file deletions come before directory deletions
func sortActions(actions []action) {
	sort.SliceStable(actions, func(i, j int) bool {
		ai, aj := actions[i], actions[j]

		// Deletions: shallowest last (deepest first).
		if isDelete(ai.kind) && isDelete(aj.kind) {
			return depth(ai.path) > depth(aj.path)
		}
		// Creations: shallowest first (parents before children).
		if !isDelete(ai.kind) && !isDelete(aj.kind) {
			return depth(ai.path) < depth(aj.path)
		}
		// Non-deletes before deletes.
		return !isDelete(ai.kind)
	})
}

func isDelete(k actionKind) bool { return k == deleteLocal || k == deleteRemote }
func depth(p string) int         { return strings.Count(p, "/") }

// ─── execute ─────────────────────────────────────────────────────────────────

func execute(
	localRoot string,
	fl *synclog.Logger,
	wdc *webdav.Client,
	state *db.DB,
	actions []action,
	onProgress func(done, total int, current string),
	uploadLimiter, downloadLimiter *rate.Limiter,
) error {
	total := len(actions)
	for i, a := range actions {
		if onProgress != nil {
			onProgress(i, total, a.path)
		}
		if err := execOne(localRoot, fl, wdc, state, a, uploadLimiter, downloadLimiter); err != nil {
			// Log and continue — partial sync is better than aborting entirely.
			logf(fl, "[sync] ERROR %s %q: %v", kindName(a.kind), a.path, err)
		}
	}
	if onProgress != nil {
		onProgress(total, total, "")
	}
	return nil
}

func execOne(
	localRoot string,
	fl *synclog.Logger,
	wdc *webdav.Client,
	state *db.DB,
	a action,
	uploadLimiter, downloadLimiter *rate.Limiter,
) error {
	localAbs := filepath.Join(localRoot, filepath.FromSlash(a.path))

	switch a.kind {

	case mkdirLocal:
		logf(fl, "[sync] mkdir local  %q", a.path)
		if err := os.MkdirAll(localAbs, 0o755); err != nil {
			return err
		}
		return state.Upsert(db.Entry{
			Path:         a.path,
			ETag:         a.remote.ETag,
			IsDir:        true,
			LastModified: a.remote.LastModified,
			FileID:       a.remote.FileID,
		})

	case download:
		logf(fl, "[sync] download     %q", a.path)
		if err := os.MkdirAll(filepath.Dir(localAbs), 0o755); err != nil {
			return err
		}
		rc, err := wdc.Get(a.path)
		if err != nil {
			return err
		}
		defer rc.Close()
		f, err := os.CreateTemp(filepath.Dir(localAbs), ".tmp-sync-*")
		if err != nil {
			return err
		}
		tmpPath := f.Name()
		if _, err := io.Copy(f, newRateLimitedReader(rc, downloadLimiter)); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return err
		}
		f.Close()
		if err := os.Rename(tmpPath, localAbs); err != nil {
			os.Remove(tmpPath)
			return err
		}
		if !a.remote.LastModified.IsZero() {
			_ = os.Chtimes(localAbs, a.remote.LastModified, a.remote.LastModified)
		}
		return state.Upsert(db.Entry{
			Path:         a.path,
			ETag:         a.remote.ETag,
			IsDir:        false,
			Size:         a.remote.Size,
			LastModified: a.remote.LastModified,
			FileID:       a.remote.FileID,
		})

	case mkcolRemote:
		logf(fl, "[sync] mkcol remote %q", a.path)
		if err := wdc.Mkcol(a.path); err != nil {
			return err
		}
		// Re-fetch the new etag for this directory.
		resources, err := wdc.Propfind(a.path, 0)
		if err != nil || len(resources) == 0 {
			// Best-effort: store with empty etag; next sync will correct it.
			return state.Upsert(db.Entry{Path: a.path, IsDir: true})
		}
		r := resources[0]
		return state.Upsert(db.Entry{
			Path:         a.path,
			ETag:         r.ETag,
			IsDir:        true,
			LastModified: r.LastModified,
			FileID:       r.FileID,
		})

	case upload:
		logf(fl, "[sync] upload       %q", a.path)
		f, err := os.Open(localAbs)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := wdc.Put(a.path, newRateLimitedReader(f, uploadLimiter), a.local.size); err != nil {
			return err
		}
		// Re-fetch etag so DB matches server.
		resources, err := wdc.Propfind(a.path, 0)
		if err != nil || len(resources) == 0 {
			return state.Upsert(db.Entry{
				Path:         a.path,
				IsDir:        false,
				Size:         a.local.size,
				LastModified: a.local.modTime,
			})
		}
		r := resources[0]
		return state.Upsert(db.Entry{
			Path:         a.path,
			ETag:         r.ETag,
			IsDir:        false,
			Size:         r.Size,
			LastModified: r.LastModified,
			FileID:       r.FileID,
		})

	case deleteLocal:
		logf(fl, "[sync] delete local %q", a.path)
		var err error
		if a.isDir {
			err = os.RemoveAll(localAbs)
		} else {
			err = os.Remove(localAbs)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return state.Delete(a.path)

	case deleteRemote:
		logf(fl, "[sync] delete remote %q", a.path)
		if err := wdc.Delete(a.path); err != nil {
			return err
		}
		return state.Delete(a.path)

	case conflictTake:
		// Rename local copy, then download server version.
		conflictPath := conflictName(localAbs)
		logf(fl, "[sync] conflict     %q — renaming local to %s", a.path, filepath.Base(conflictPath))
		if err := os.Rename(localAbs, conflictPath); err != nil {
			return fmt.Errorf("rename conflict copy: %w", err)
		}
		// Now download the server version as if it were a fresh download.
		a.kind = download
		return execOne(localRoot, fl, wdc, state, a, uploadLimiter, downloadLimiter)
	}

	return nil
}

// conflictName appends a timestamp suffix before the file extension.
func conflictName(abs string) string {
	ext := filepath.Ext(abs)
	base := strings.TrimSuffix(abs, ext)
	ts := time.Now().Format("20060102-150405")
	return fmt.Sprintf("%s.conflict-%s%s", base, ts, ext)
}

func kindName(k actionKind) string {
	switch k {
	case download:
		return "download"
	case upload:
		return "upload"
	case deleteLocal:
		return "deleteLocal"
	case deleteRemote:
		return "deleteRemote"
	case mkcolRemote:
		return "mkcolRemote"
	case mkdirLocal:
		return "mkdirLocal"
	case conflictTake:
		return "conflict"
	default:
		return "unknown"
	}
}
