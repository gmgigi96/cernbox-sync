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
//     - localDeleted: in DB, not on disk, remote unchanged → delete remote
//     - localDeletedRemoteUpdated: local deleted but remote changed → download (server wins)
//     - conflict   : changed both locally & remotely      → server wins, rename local copy
//     - remoteDeletedLocalUpdated: remote deleted, local changed → rename local as conflict copy, then delete
//     - inSync     : nothing changed                      → no-op
//  5. Execute the actions in safe order:
//     dirs before files (create), files before dirs (delete).
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gmgigi96/cernbox-sync/db"
	"github.com/gmgigi96/cernbox-sync/synclog"
	"github.com/gmgigi96/cernbox-sync/webdav"
	"golang.org/x/time/rate"
)

// bandwidthCounter tracks a rolling bytes-per-second rate using a sliding
// window of 1 second. It is safe for concurrent use.
type bandwidthCounter struct {
	mu        sync.Mutex
	buckets   [10]int64     // 100 ms buckets
	times     [10]time.Time // start time of each bucket
	bucketIdx int
}

func (c *bandwidthCounter) add(n int) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	b := &c.buckets[c.bucketIdx]
	t := &c.times[c.bucketIdx]
	// Rotate bucket every 100 ms.
	if now.Sub(*t) >= 100*time.Millisecond {
		c.bucketIdx = (c.bucketIdx + 1) % len(c.buckets)
		b = &c.buckets[c.bucketIdx]
		t = &c.times[c.bucketIdx]
		*b = 0
		*t = now
	}
	*b += int64(n)
}

// bytesPerSec returns the current rolling bytes/sec estimate over the last second.
func (c *bandwidthCounter) bytesPerSec() int64 {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	var total int64
	for i, t := range c.times {
		if !t.IsZero() && now.Sub(t) < time.Second {
			total += c.buckets[i]
		}
	}
	return total
}

// rateLimitedReader wraps an io.Reader and throttles reads using a token-bucket
// limiter. burstSize is the maximum number of bytes consumed per single WaitN
// call; using a fixed burst avoids having to split very large reads.
// When counter is non-nil, each read is also recorded for bandwidth measurement.
type rateLimitedReader struct {
	ctx       context.Context
	r         io.Reader
	lim       *rate.Limiter
	burstSize int
	counter   *bandwidthCounter
}

// byteProgressReader wraps an io.Reader and calls onProgress after each read
// so callers can track byte-level transfer progress.
type byteProgressReader struct {
	r          io.Reader
	path       string
	done       int64
	total      int64
	onProgress func(path string, bytesDone, bytesTotal int64)
}

func newByteProgressReader(r io.Reader, path string, total int64, onProgress func(string, int64, int64)) io.Reader {
	return &byteProgressReader{r: r, path: path, total: total, onProgress: onProgress}
}

func (b *byteProgressReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.done += int64(n)
		b.onProgress(b.path, b.done, b.total)
	}
	return n, err
}

func newRateLimitedReader(ctx context.Context, r io.Reader, lim *rate.Limiter, counter *bandwidthCounter) io.Reader {
	if lim == nil && counter == nil {
		return r
	}
	burst := 0
	if lim != nil {
		burst = lim.Burst()
	}
	return &rateLimitedReader{ctx: ctx, r: r, lim: lim, burstSize: burst, counter: counter}
}

func (rl *rateLimitedReader) Read(p []byte) (int, error) {
	// Cap the read to the burst size so WaitN never exceeds the limiter's burst.
	if rl.lim != nil && len(p) > rl.burstSize {
		p = p[:rl.burstSize]
	}
	n, err := rl.r.Read(p)
	if n > 0 {
		if rl.lim != nil {
			if waitErr := rl.lim.WaitN(rl.ctx, n); waitErr != nil {
				return n, waitErr
			}
		}
		if rl.counter != nil {
			rl.counter.add(n)
		}
	}
	return n, err
}

// Config holds the parameters for a sync session.
type Config struct {
	// Ctx is an optional context for cancelling an in-progress sync.
	// When cancelled, the execute phase stops after the current in-flight
	// operations finish. If nil, context.Background() is used.
	Ctx context.Context
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
	// OnProgress is called before each action is executed and periodically
	// during file transfers. done is the number of actions completed so far,
	// total is the total action count, current is the relative path being
	// processed, uploadBps and downloadBps are the current rolling bytes/sec
	// rates. bytesDone and bytesTotal are the bytes transferred and total size
	// for the current file (bytesTotal == 0 when not mid-transfer). May be nil.
	OnProgress func(done, total int, current string, uploadBps, downloadBps, bytesDone, bytesTotal int64)
	// TransferStreams is the number of concurrent upload/download operations.
	// 0 or 1 means sequential (no concurrency).
	TransferStreams int
	// MetadataStreams is the number of concurrent metadata operations
	// (directory creates and deletes) per depth tier. 0 or 1 means sequential.
	MetadataStreams int
}

// action classifies what needs to happen to a path.
type actionKind int

const (
	download            actionKind = iota // remote → local
	upload                                // local → remote
	deleteLocal                           // remote was deleted; remove local
	deleteRemote                          // local was deleted; remove remote
	mkcolRemote                           // create remote directory
	mkdirLocal                            // create local directory
	conflictTake                          // conflict: server wins, rename local
	conflictDeleteLocal                   // remote deleted while local changed: rename local as conflict, then delete
	moveRemote                            // local rename/move: one MOVE on the server
	renameLocal                           // remote rename/move: os.Rename locally
)

type action struct {
	kind   actionKind
	path   string // relative, forward-slash separated
	isDir  bool
	remote *webdav.Resource
	local  *localInfo

	// moveRemote / renameLocal only: path is the destination, from the source;
	// moves lists the DB rows to rewrite (the item and everything below it);
	// seq keeps renames in the order they were detected.
	from  string
	moves []db.Move
	seq   int
}

type localInfo struct {
	path    string // absolute
	size    int64
	modTime time.Time
	isDir   bool
	dev     uint64 // device and inode, 0 when unavailable
	ino     uint64
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
	ctx := cfg.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// runCtx is cancelled when the caller cancels or when the cycle must be
	// aborted (e.g. the credentials were rejected); it aborts in-flight HTTP
	// requests.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	logf(cfg.FolderLog, "[sync] starting — local: %s  remote: %s", cfg.LocalRoot, cfg.RemoteBase)

	wdc := webdav.NewClient(cfg.RemoteBase, cfg.Username, cfg.Password).WithContext(runCtx)
	folders := normalizeFolders(cfg.Folders)

	state, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = state.Close() }()

	// Remove conflict records whose .conflict- file has been deleted by the user.
	if entries, err := state.AllConflicts(); err != nil {
		logf(cfg.FolderLog, "[sync] WARNING sweep conflicts: %v", err)
	} else {
		for _, e := range entries {
			if _, statErr := os.Stat(e.ConflictPath); os.IsNotExist(statErr) {
				if err := state.DeleteConflict(e.ConflictPath); err != nil {
					logf(cfg.FolderLog, "[sync] WARNING delete conflict record %q: %v", e.Path, err)
				} else {
					logf(cfg.FolderLog, "[sync] conflict resolved %q — conflict copy removed by user", e.Path)
				}
			}
		}
	}

	// ── 1. Load DB state ─────────────────────────────────────────────────────
	// Loaded first so scanRemote can use stored ETags to skip unchanged subtrees.
	if err := ctx.Err(); err != nil {
		return err
	}
	dbState, err := state.All()
	if err != nil {
		return fmt.Errorf("load db: %w", err)
	}
	logf(cfg.FolderLog, "[sync] db entries: %d", len(dbState))

	// ── 2. Remote scan ───────────────────────────────────────────────────────
	if err := ctx.Err(); err != nil {
		return err
	}
	remoteMap, err := scanRemote(wdc, folders, cfg.SyncHiddenFiles, dbState)
	if err != nil {
		return wrapAuth(ctx, fmt.Errorf("remote scan: %w", err))
	}
	logf(cfg.FolderLog, "[sync] remote resources: %d", len(remoteMap))

	// ── 3. Local scan ────────────────────────────────────────────────────────
	if err := ctx.Err(); err != nil {
		return err
	}
	localMap, err := scanLocal(cfg.LocalRoot, cfg.SyncHiddenFiles)
	if err != nil {
		return fmt.Errorf("local scan: %w", err)
	}
	if len(folders) > 0 {
		keepSelected(localMap, remoteMap, folders)
	}
	logf(cfg.FolderLog, "[sync] local resources: %d", len(localMap))

	// ── 4. Classify ──────────────────────────────────────────────────────────
	backfillIdentity(state, dbState, remoteMap, localMap)
	renames := detectRenames(remoteMap, localMap, dbState)
	actions := append(renames, classify(remoteMap, localMap, dbState)...)
	sortActions(actions)
	actions, refused := applyPermissions(actions, remoteMap, dbState)
	logf(cfg.FolderLog, "[sync] actions: %d", len(actions))
	for _, fe := range refused {
		logf(cfg.FolderLog, "[sync] ERROR %s %q: %v", fe.Op, fe.Path, fe.Err)
	}

	// ── 5. Execute ───────────────────────────────────────────────────────────
	uploadCounter := &bandwidthCounter{}
	downloadCounter := &bandwidthCounter{}
	fileErrs, authErr := execute(runCtx, cancel, cfg.LocalRoot, cfg.FolderLog, wdc, state, actions, cfg.OnProgress, cfg.UploadLimiter, cfg.DownloadLimiter, uploadCounter, downloadCounter, cfg.TransferStreams, cfg.MetadataStreams)
	if authErr != nil {
		return wrapAuth(ctx, fmt.Errorf("execute: %w", authErr))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if all := append(refused, fileErrs...); len(all) > 0 {
		sort.SliceStable(all, func(i, j int) bool { return all[i].Path < all[j].Path })
		return &SyncError{Errors: all}
	}

	logf(cfg.FolderLog, "[sync] done")
	return nil
}

// wrapAuth makes an authentication failure recognisable by its text as well
// as by errors.Is(err, webdav.ErrUnauthorized). A cancelled caller context wins
// over any error it caused.
func wrapAuth(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, webdav.ErrUnauthorized) {
		return fmt.Errorf("%w (the server rejected the credentials): %w", webdav.ErrUnauthorized, err)
	}
	return err
}

// normalizeFolders turns the stored selection (URL-encoded, as taken from
// server hrefs) into plain relative paths.
func normalizeFolders(folders []string) []string {
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		if decoded, err := url.PathUnescape(f); err == nil {
			f = decoded
		}
		if f = strings.Trim(f, "/"); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ─── remote scan ─────────────────────────────────────────────────────────────

// scanRemote does a BFS PROPFIND (Depth 1) to build the remote tree.
// Returns a map of relative path → Resource.
// The root itself (empty path "") is included.
//
// If folders is non-empty only those top-level sub-directories (and their
// descendants) are visited; the root entry is still included so the engine
// can track the anchor point.
//
// Incremental optimisation: when a directory's ETag matches the stored DB
// entry, its subtree is replayed from the DB instead of issuing further
// PROPFIND requests. On ownCloud/CERNBox a directory's ETag changes whenever
// any descendant changes, so an unchanged ETag guarantees the entire subtree
// is identical to the last sync.
func scanRemote(wdc *webdav.Client, folders []string, syncHiddenFiles bool, dbState map[string]*db.Entry) (map[string]*webdav.Resource, error) {
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
		queue = append(queue, folders...)
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

			name := rel
			if idx := strings.LastIndex(rel, "/"); idx >= 0 {
				name = rel[idx+1:]
			}
			if isSyncMetadata(name) || isLockFile(name) {
				continue
			}
			// Skip hidden entries unless enabled.
			if !syncHiddenFiles && isHidden(name, "") {
				continue
			}

			result[rel] = e

			if e.IsDir && rel != cur {
				// Incremental check: if this directory has a DB entry whose ETag
				// matches the server, the entire subtree is unchanged. Replay the
				// DB entries and skip the PROPFIND traversal for this subtree.
				if dbEntry, ok := dbState[rel]; ok && dbEntry.ETag == e.ETag {
					if err := replaySubtree(rel, dbState, syncHiddenFiles, result); err != nil {
						return nil, fmt.Errorf("replay subtree %q: %w", rel, err)
					}
					continue
				}
				queue = append(queue, rel)
			}
		}
	}

	return result, nil
}

// replaySubtree copies all DB entries under prefix into result without issuing
// any network requests. Called when a directory's ETag is unchanged.
func replaySubtree(prefix string, dbState map[string]*db.Entry, syncHiddenFiles bool, result map[string]*webdav.Resource) error {
	for path, e := range dbState {
		if !strings.HasPrefix(path, prefix+"/") {
			continue
		}
		if _, seen := result[path]; seen {
			continue
		}
		// Skip hidden entries unless enabled.
		if !syncHiddenFiles {
			name := path
			if idx := strings.LastIndex(path, "/"); idx >= 0 {
				name = path[idx+1:]
			}
			if isHidden(name, "") {
				continue
			}
		}
		result[path] = dbEntryToResource(e)
	}
	return nil
}

// dbEntryToResource converts a DB entry back into a webdav.Resource so that
// replayed subtree entries are indistinguishable from freshly fetched ones.
func dbEntryToResource(e *db.Entry) *webdav.Resource {
	return &webdav.Resource{
		Path:         e.Path,
		ETag:         e.ETag,
		IsDir:        e.IsDir,
		Size:         e.Size,
		LastModified: e.LastModified,
		FileID:       e.FileID,
		Permissions:  e.Permissions,
	}
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
		if rel != "" && (isSyncMetadata(d.Name()) || isConflictFile(d.Name()) || isLockFile(d.Name())) {
			if d.IsDir() && isLockFile(d.Name()) {
				return filepath.SkipDir
			}
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
		li := &localInfo{
			path:    absPath,
			size:    info.Size(),
			modTime: info.ModTime(),
			isDir:   d.IsDir(),
		}
		li.dev, li.ino = fileIdentity(info)
		result[rel] = li
		return nil
	})
	return result, err
}

// isSyncMetadata reports whether name belongs to sync-client bookkeeping that
// must never be transferred: this engine's state DB (with its SQLite side
// files), log and download staging files, and the journals and logs that the
// ownCloud desktop client and its brandings keep inside a sync folder (so a
// folder taken over from that client does not upload them).
func isSyncMetadata(name string) bool {
	switch {
	case strings.HasPrefix(name, ".sync.db"), name == ".sync.log", strings.HasPrefix(name, ".tmp-sync-"):
		return true
	case strings.HasPrefix(name, ".sync_"), strings.HasPrefix(name, "._sync_"):
		return strings.Contains(name, ".db")
	case strings.HasPrefix(name, ".csync_journal.db"), strings.HasPrefix(name, ".owncloudsync.log"):
		return true
	}
	return false
}

// isLockFile reports whether name is an office/editor lock or scratch file
// (MS Office "~$doc", LibreOffice ".~lock.doc#", Word "~WRL0001.tmp"). These
// are only meaningful on the machine that created them and are never synced,
// in either direction, whatever the hidden-files setting.
func isLockFile(name string) bool {
	switch {
	case strings.HasPrefix(name, "~$"):
		return true
	case strings.HasPrefix(name, ".~lock.") && strings.HasSuffix(name, "#"):
		return true
	case strings.HasPrefix(name, "~") && strings.HasSuffix(name, ".tmp"):
		return true
	}
	return false
}

// keepSelected (folders already normalised) removes from localMap every entry outside the selected
// sub-folders. The remote scan only covers the selection, so anything else
// (e.g. files at the root of a selectively synced folder) would otherwise look
// new and be uploaded, or look remotely deleted and be removed locally.
// Entries seen by the remote scan are always kept, so a selection spelled
// differently from the scanned paths can never hide a file that exists on
// both sides (which would look locally deleted).
func keepSelected(localMap map[string]*localInfo, remoteMap map[string]*webdav.Resource, folders []string) {
	selection := folders
	for path := range localMap {
		if _, scanned := remoteMap[path]; scanned || path == "" {
			continue
		}
		selected := false
		for _, f := range selection {
			if path == f || strings.HasPrefix(path, f+"/") {
				selected = true
				break
			}
		}
		if !selected {
			delete(localMap, path)
		}
	}
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
			if rem.ETag != dbEntry.ETag {
				// Remote changed while local was deleted → server wins; download.
				a := action{path: path, isDir: rem.IsDir, remote: rem}
				if rem.IsDir {
					a.kind = mkdirLocal
				} else {
					a.kind = download
				}
				actions = append(actions, a)
			} else {
				// Remote unchanged; propagate local deletion to server.
				actions = append(actions, action{
					kind:  deleteRemote,
					path:  path,
					isDir: rem.IsDir,
				})
			}

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
		} else if !loc.isDir && isLocalChanged(loc, dbEntry) {
			// Remote deleted while local was modified → save conflict copy, then delete.
			actions = append(actions, action{
				kind:  conflictDeleteLocal,
				path:  path,
				isDir: loc.isDir,
				local: loc,
			})
		} else {
			// Remote deleted, local unchanged → delete local.
			actions = append(actions, action{
				kind:  deleteLocal,
				path:  path,
				isDir: loc.isDir,
				local: loc,
			})
		}
	}

	// DB entries that are gone from both sides — clean up DB (handled during execute).

	protectRemotelyDeletedDirs(actions)
	sortActions(actions)
	return actions
}

// protectRemotelyDeletedDirs keeps local data alive when a directory was
// deleted on the server while something inside it is new or modified locally.
// Such a directory (and every ancestor of the data to keep) is recreated on the
// server instead of being removed locally; only the known and unchanged files
// below it are still deleted locally.
func protectRemotelyDeletedDirs(actions []action) {
	doomed := make(map[string]int) // deleteLocal directories → index in actions
	for i, a := range actions {
		if a.kind == deleteLocal && a.isDir {
			doomed[a.path] = i
		}
	}
	if len(doomed) == 0 {
		return
	}
	keep := make(map[string]bool)
	for _, a := range actions {
		switch a.kind {
		case upload, mkcolRemote, conflictDeleteLocal:
		default:
			continue
		}
		for p := parentOf(a.path); p != ""; p = parentOf(p) {
			if _, ok := doomed[p]; ok {
				keep[p] = true
			}
		}
	}
	for p := range keep {
		actions[doomed[p]].kind = mkcolRemote
	}
}

// parentOf returns the parent directory of a relative path ("" for top level).
func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
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

		// Renames first, in the order they were detected (each one assumes
		// the previous ones were applied).
		if mi, mj := isMove(ai.kind), isMove(aj.kind); mi || mj {
			if mi && mj {
				return ai.seq < aj.seq
			}
			return mi
		}

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
func isMove(k actionKind) bool   { return k == moveRemote || k == renameLocal }
func depth(p string) int         { return strings.Count(p, "/") }

// ─── permissions ─────────────────────────────────────────────────────────────

// applyPermissions enforces the server's oc:permissions on the planned
// actions. Changes the server would refuse are not sent: modified or deleted
// files are restored from the server, new items stay on disk untouched. Each
// refusal is reported as a FileError. Resources without a permissions string
// (property not provided) are unrestricted.
//
// Letters used: W (write file), D (delete), C (create file in folder),
// K (create folder in folder).
func applyPermissions(actions []action, remoteMap map[string]*webdav.Resource, dbState map[string]*db.Entry) ([]action, []FileError) {
	out := make([]action, 0, len(actions))
	var refused []FileError
	refusedDirs := make(map[string]bool) // new local directories that were refused
	mustKeep := make(map[string]bool)    // directories that still contain refused deletions
	changed := false

	refuse := func(a action) {
		refused = append(refused, FileError{Path: a.path, Op: kindName(a.kind), Err: ErrReadOnly})
	}
	allows := func(perms, letter string) bool { return perms == "" || strings.Contains(perms, letter) }
	createDenied := func(path, letter string) bool {
		parent := parentOf(path)
		if refusedDirs[parent] {
			return true
		}
		r := remoteMap[parent]
		return r != nil && !allows(r.Permissions, letter)
	}
	restore := func(rem *webdav.Resource, path string) action {
		changed = true
		if rem.IsDir {
			return action{kind: mkdirLocal, path: path, isDir: true, remote: rem}
		}
		return action{kind: download, path: path, remote: rem}
	}

	for _, a := range actions {
		rem := remoteMap[a.path]
		switch a.kind {
		case upload:
			if rem != nil && !rem.IsDir {
				if allows(rem.Permissions, "W") {
					break
				}
				switch {
				case dbState[a.path] != nil:
					// Edited locally in a read-only location: put the server version back.
					refuse(a)
					out = append(out, restore(rem, a.path))
				case a.local.size == rem.Size:
					out = append(out, restore(rem, a.path))
				default:
					// First sync, contents differ: keep the local file as a conflict copy.
					changed = true
					out = append(out, action{kind: conflictTake, path: a.path, remote: rem, local: a.local})
				}
				continue
			}
			if rem == nil && createDenied(a.path, "C") {
				refuse(a)
				continue
			}
		case mkcolRemote:
			if rem == nil && createDenied(a.path, "K") {
				refusedDirs[a.path] = true
				refuse(a)
				continue
			}
		case deleteRemote:
			if rem == nil {
				break
			}
			denied := !allows(rem.Permissions, "D")
			if denied || mustKeep[a.path] {
				if denied {
					refuse(a)
				}
				for p := parentOf(a.path); p != ""; p = parentOf(p) {
					mustKeep[p] = true
				}
				out = append(out, restore(rem, a.path))
				continue
			}
		}
		out = append(out, a)
	}
	if changed {
		sortActions(out)
	}
	return out, refused
}

// ─── execute ─────────────────────────────────────────────────────────────────

// executor carries everything the actions of one cycle share.
type executor struct {
	ctx       context.Context
	localRoot string
	fl        *synclog.Logger
	wdc       *webdav.Client
	state     *db.DB

	uploadLimiter, downloadLimiter *rate.Limiter
	uploadCounter, downloadCounter *bandwidthCounter
	onByteProgress                 func(path string, bytesDone, bytesTotal int64)

	// dirs are the final DB entries of directories handled this cycle. They
	// are committed only once everything below them is synced: a directory's
	// ETag in the DB lets the next scan skip its whole subtree, so recording it
	// early would hide children that failed or were never reached.
	dirMu sync.Mutex
	dirs  []db.Entry

	// failedMoves are the destinations of renames that could not be done; the
	// rest of the cycle leaves them (and everything below) alone.
	failMu      sync.Mutex
	failedMoves []string
}

// upsert writes an entry, recording the local identity (inode) of the item.
func (x *executor) upsert(e db.Entry) error {
	if e.Inode == 0 {
		if st, err := os.Lstat(filepath.Join(x.localRoot, filepath.FromSlash(e.Path))); err == nil {
			e.Device, e.Inode = fileIdentity(st)
		}
	}
	return x.state.Upsert(e)
}

func (x *executor) moveFailed(dst string) {
	x.failMu.Lock()
	x.failedMoves = append(x.failedMoves, dst)
	x.failMu.Unlock()
}

func (x *executor) underFailedMove(path string) bool {
	x.failMu.Lock()
	defer x.failMu.Unlock()
	for _, d := range x.failedMoves {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}

func isAncestorOfAny(dir string, paths []string) bool {
	for _, p := range paths {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

func isInsideAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

func (x *executor) deferDir(e db.Entry) {
	x.dirMu.Lock()
	x.dirs = append(x.dirs, e)
	x.dirMu.Unlock()
}

// placeholderDir records a directory without ETag so that it is revisited on
// the next scan, unless the DB already knows it.
func (x *executor) placeholderDir(path string) error {
	e, err := x.state.Get(path)
	if err != nil {
		return err
	}
	if e != nil {
		return nil
	}
	return x.upsert(db.Entry{Path: path, IsDir: true})
}

// execute runs the planned actions. A failing action is collected and the
// others continue; the DB is only updated for actions that succeeded. When
// the server rejects the credentials the whole cycle is aborted (cancel is
// called, nothing further is executed) and that error is returned separately.
func execute(
	ctx context.Context,
	cancel context.CancelFunc,
	localRoot string,
	fl *synclog.Logger,
	wdc *webdav.Client,
	state *db.DB,
	actions []action,
	onProgress func(done, total int, current string, uploadBps, downloadBps, bytesDone, bytesTotal int64),
	uploadLimiter, downloadLimiter *rate.Limiter,
	uploadCounter, downloadCounter *bandwidthCounter,
	transferStreams, metadataStreams int,
) ([]FileError, error) {
	if transferStreams < 1 {
		transferStreams = 1
	}
	if metadataStreams < 1 {
		metadataStreams = 1
	}
	total := len(actions)

	// Track completed actions for onProgress; protected by a mutex so the
	// worker goroutines can safely increment it.
	var (
		doneMu sync.Mutex
		done   int
	)
	bps := func() (int64, int64) {
		return uploadCounter.bytesPerSec(), downloadCounter.bytesPerSec()
	}

	// Per-file byte-level progress: track the currently-transferring file's
	// path, bytes done, and total size so the ticker can report sub-action
	// progress between action completions.
	var (
		fileMu       sync.Mutex
		fileProgress = make(map[string][2]int64) // path → [bytesDone, bytesTotal]
	)
	setFileProgress := func(path string, bytesDone, bytesTotal int64) {
		fileMu.Lock()
		if bytesDone >= bytesTotal && bytesTotal > 0 {
			delete(fileProgress, path)
		} else {
			fileProgress[path] = [2]int64{bytesDone, bytesTotal}
		}
		fileMu.Unlock()
	}
	// aggregateFileProgress returns the sum of bytes across all in-flight transfers.
	aggregateFileProgress := func() (int64, int64) {
		fileMu.Lock()
		defer fileMu.Unlock()
		var bd, bt int64
		for _, v := range fileProgress {
			bd += v[0]
			bt += v[1]
		}
		return bd, bt
	}

	reportDone := func(path string) {
		doneMu.Lock()
		done++
		d := done
		doneMu.Unlock()
		if onProgress != nil {
			up, down := bps()
			bd, bt := aggregateFileProgress()
			onProgress(d, total, path, up, down, bd, bt)
		}
	}

	x := &executor{
		ctx:             ctx,
		localRoot:       localRoot,
		fl:              fl,
		wdc:             wdc,
		state:           state,
		uploadLimiter:   uploadLimiter,
		downloadLimiter: downloadLimiter,
		uploadCounter:   uploadCounter,
		downloadCounter: downloadCounter,
		// No need to call onProgress here; the ticker fires every second.
		onByteProgress: setFileProgress,
	}

	var (
		errMu    sync.Mutex
		fileErrs []FileError
		authErr  error
	)
	fail := func(a action, err error) {
		logf(fl, "[sync] ERROR %s %q: %v", kindName(a.kind), a.path, err)
		errMu.Lock()
		defer errMu.Unlock()
		switch {
		case errors.Is(err, webdav.ErrUnauthorized):
			if authErr == nil {
				authErr = err
			}
			cancel() // abort the cycle: nothing more is sent or deleted
		case ctx.Err() != nil:
			// Interrupted, not failed.
		default:
			fileErrs = append(fileErrs, FileError{Path: a.path, Op: kindName(a.kind), Err: err})
		}
	}

	// runPool dispatches a slice of actions to a worker pool of size n.
	// All actions are independent within the slice (caller guarantees this).
	// Workers stop picking up new actions when ctx is cancelled.
	runPool := func(items []action, n int) {
		if len(items) == 0 {
			return
		}
		work := make(chan action, len(items))
		for _, a := range items {
			work <- a
		}
		close(work)
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() {
				for a := range work {
					if ctx.Err() != nil {
						return
					}
					if onProgress != nil {
						doneMu.Lock()
						d := done
						doneMu.Unlock()
						up, down := bps()
						bd, bt := aggregateFileProgress()
						onProgress(d, total, a.path, up, down, bd, bt)
					}
					if err := x.execOne(a); err != nil {
						fail(a, err)
					}
					reportDone(a.path)
				}
			})
		}
		wg.Wait()
	}

	// runByDepthTier groups an already-depth-sorted slice into tiers of equal
	// depth and runs each tier through a pool. This preserves the parent-before-
	// child (or child-before-parent) invariant while parallelising within a tier.
	runByDepthTier := func(items []action, n int) {
		i := 0
		for i < len(items) {
			d := depth(items[i].path)
			j := i + 1
			for j < len(items) && depth(items[j].path) == d {
				j++
			}
			runPool(items[i:j], n)
			i = j
		}
	}

	// Partition actions into three ordered phases:
	//   1. Dir creates  — mkdirLocal / mkcolRemote  (shallowest-first, then conflicts)
	//   2. Transfers    — upload / download          (fully independent)
	//   3. Deletes      — deleteLocal / deleteRemote (deepest-first)
	var creates, conflicts, transfers, teardown, moves []action
	for _, a := range actions {
		switch a.kind {
		case moveRemote, renameLocal:
			moves = append(moves, a)
		case upload, download:
			transfers = append(transfers, a)
		case deleteLocal, deleteRemote:
			teardown = append(teardown, a)
		case conflictTake, conflictDeleteLocal:
			conflicts = append(conflicts, a)
		default: // mkdirLocal, mkcolRemote
			creates = append(creates, a)
		}
	}

	if onProgress != nil {
		up, down := bps()
		onProgress(0, total, "", up, down, 0, 0)
	}

	// Ticker goroutine: fire onProgress every second so bandwidth readings
	// update in real time even during a long single-file transfer.
	if onProgress != nil {
		stopTicker := make(chan struct{})
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					doneMu.Lock()
					d := done
					doneMu.Unlock()
					up, down := bps()
					bd, bt := aggregateFileProgress()
					onProgress(d, total, "", up, down, bd, bt)
				case <-stopTicker:
					return
				}
			}
		}()
		defer close(stopTicker)
	}

	// Phase 0 — renames, one at a time in detection order. Remote directories
	// that a move lands in must exist first; directories inside a moved
	// subtree can only be created afterwards.
	var dests []string
	for _, m := range moves {
		if m.kind == moveRemote {
			dests = append(dests, m.path)
		}
	}
	var early []action
	if len(dests) > 0 {
		rest := creates[:0:0]
		for _, c := range creates {
			if c.kind == mkcolRemote && isAncestorOfAny(c.path, dests) && !isInsideAny(c.path, dests) {
				early = append(early, c)
			} else {
				rest = append(rest, c)
			}
		}
		creates = rest
	}
	runByDepthTier(early, metadataStreams)
	runPool(moves, 1)

	// Phase 1a — concurrent dir creates, tier by tier (parents before children).
	runByDepthTier(creates, metadataStreams)

	// Phase 1b — conflicts: rename local then download; independent of each other.
	runPool(conflicts, transferStreams)

	// Phase 2 — concurrent file transfers.
	runPool(transfers, transferStreams)

	// Phase 3 — concurrent deletes, tier by tier (children before parents).
	runByDepthTier(teardown, metadataStreams)

	// Commit directory state, except for directories with a failed descendant
	// and when the cycle was cut short (some of their content never ran).
	if authErr == nil && ctx.Err() == nil {
		for _, e := range x.dirs {
			blocked := false
			for _, fe := range fileErrs {
				if strings.HasPrefix(fe.Path, e.Path+"/") {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
			if err := x.upsert(e); err != nil {
				fileErrs = append(fileErrs, FileError{Path: e.Path, Op: "updateState", Err: err})
			}
		}
	}

	if onProgress != nil {
		up, down := bps()
		onProgress(total, total, "", up, down, 0, 0)
	}
	return fileErrs, authErr
}

func (x *executor) execOne(a action) error {
	localAbs := filepath.Join(x.localRoot, filepath.FromSlash(a.path))
	fl := x.fl

	if !isMove(a.kind) && x.underFailedMove(a.path) {
		logf(fl, "[sync] skip %s %q — its rename failed", kindName(a.kind), a.path)
		return nil
	}

	switch a.kind {

	case moveRemote:
		return x.moveRemote(a)

	case renameLocal:
		return x.renameLocal(a)

	case mkdirLocal:
		logf(fl, "[sync] mkdir local  %q", a.path)
		if err := os.MkdirAll(localAbs, 0o755); err != nil {
			return err
		}
		x.deferDir(db.Entry{
			Path:         a.path,
			ETag:         a.remote.ETag,
			IsDir:        true,
			LastModified: a.remote.LastModified,
			FileID:       a.remote.FileID,
			Permissions:  a.remote.Permissions,
		})
		return x.placeholderDir(a.path)

	case download:
		logf(fl, "[sync] download     %q", a.path)
		return x.download(a, localAbs)

	case mkcolRemote:
		logf(fl, "[sync] mkcol remote %q", a.path)
		if err := x.wdc.Mkcol(a.path); err != nil {
			return err
		}
		// Fetch the new etag for this directory.
		resources, err := x.wdc.Propfind(a.path, 0)
		if err != nil && (errors.Is(err, webdav.ErrUnauthorized) || x.ctx.Err() != nil) {
			return err
		}
		if err == nil && len(resources) > 0 {
			r := resources[0]
			x.deferDir(db.Entry{
				Path:         a.path,
				ETag:         r.ETag,
				IsDir:        true,
				LastModified: r.LastModified,
				FileID:       r.FileID,
				Permissions:  r.Permissions,
			})
		}
		// Otherwise the directory stays without etag; the next sync corrects it.
		return x.placeholderDir(a.path)

	case upload:
		logf(fl, "[sync] upload       %q", a.path)
		size := a.local.size
		wrap := func(r io.Reader) io.Reader {
			src := newRateLimitedReader(x.ctx, r, x.uploadLimiter, x.uploadCounter)
			if x.onByteProgress != nil && size > 0 {
				src = newByteProgressReader(src, a.path, size, x.onByteProgress)
			}
			return src
		}
		if err := x.wdc.PutFile(a.path, localAbs, wrap); err != nil {
			return err
		}
		// The DB records the local file as uploaded (size and mtime as found
		// locally), so that a later server-side change is not mistaken for a
		// local one. The etag is the one the server assigned.
		e := db.Entry{
			Path:         a.path,
			Size:         size,
			LastModified: a.local.modTime,
		}
		resources, err := x.wdc.Propfind(a.path, 0)
		if err != nil && (errors.Is(err, webdav.ErrUnauthorized) || x.ctx.Err() != nil) {
			return err
		}
		if err == nil && len(resources) > 0 {
			r := resources[0]
			e.ETag, e.FileID, e.Permissions = r.ETag, r.FileID, r.Permissions
		}
		return x.upsert(e)

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
		return x.state.Delete(a.path)

	case deleteRemote:
		logf(fl, "[sync] delete remote %q", a.path)
		if err := x.wdc.Delete(a.path); err != nil {
			return err
		}
		return x.state.Delete(a.path)

	case conflictTake:
		// Rename local copy, then download server version.
		conflictPath := conflictName(localAbs)
		logf(fl, "[sync] conflict     %q — renaming local to %s", a.path, filepath.Base(conflictPath))
		if err := os.Rename(localAbs, conflictPath); err != nil {
			return fmt.Errorf("rename conflict copy: %w", err)
		}
		if err := x.state.AddConflict(a.path, conflictPath, time.Now()); err != nil {
			logf(fl, "[sync] WARNING record conflict %q: %v", a.path, err)
		}
		// Now download the server version as if it were a fresh download.
		return x.download(a, localAbs)

	case conflictDeleteLocal:
		// Remote was deleted while local was modified: preserve local changes as a
		// conflict copy so no work is lost, then remove the original path.
		conflictPath := conflictName(localAbs)
		logf(fl, "[sync] conflict     %q — remote deleted, renaming local to %s", a.path, filepath.Base(conflictPath))
		if err := os.Rename(localAbs, conflictPath); err != nil {
			return fmt.Errorf("rename conflict copy: %w", err)
		}
		if err := x.state.AddConflict(a.path, conflictPath, time.Now()); err != nil {
			logf(fl, "[sync] WARNING record conflict %q: %v", a.path, err)
		}
		return x.state.Delete(a.path)
	}

	return nil
}

// download fetches a.path into a temporary file next to its destination and
// moves it into place only once the whole body has arrived. The DB records
// what was actually written (the ETag, size and mtime of the content served by
// the GET), which may be newer than the PROPFIND the plan was built from.
func (x *executor) download(a action, localAbs string) error {
	if err := os.MkdirAll(filepath.Dir(localAbs), 0o755); err != nil {
		return err
	}
	rc, info, err := x.wdc.GetWithInfo(a.path)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	f, err := os.CreateTemp(filepath.Dir(localAbs), ".tmp-sync-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	abort := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}

	fileTotal := a.remote.Size
	if info.ContentLength >= 0 {
		fileTotal = info.ContentLength
	}
	src := newRateLimitedReader(x.ctx, rc, x.downloadLimiter, x.downloadCounter)
	if x.onByteProgress != nil && fileTotal > 0 {
		src = newByteProgressReader(src, a.path, fileTotal, x.onByteProgress)
	}
	n, err := io.Copy(f, src)
	if err != nil {
		return abort(err)
	}
	// A different ETag means the file changed on the server after the scan;
	// whatever was served is then the truth. Otherwise the body must be as
	// long as the scan said.
	changed := info.ETag != "" && info.ETag != a.remote.ETag
	if info.ContentLength >= 0 && n != info.ContentLength {
		return abort(fmt.Errorf("incomplete download: received %d of %d bytes", n, info.ContentLength))
	}
	if !changed && a.remote.Size > 0 && n != a.remote.Size {
		return abort(fmt.Errorf("incomplete download: received %d of %d bytes", n, a.remote.Size))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, localAbs); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	etag, mtime := a.remote.ETag, a.remote.LastModified
	if info.ETag != "" {
		etag = info.ETag
	}
	if changed && !info.LastModified.IsZero() {
		mtime = info.LastModified
	}
	if !mtime.IsZero() {
		_ = os.Chtimes(localAbs, mtime, mtime)
	} else if st, err := os.Stat(localAbs); err == nil {
		mtime = st.ModTime()
	}
	return x.upsert(db.Entry{
		Path:         a.path,
		ETag:         etag,
		IsDir:        false,
		Size:         n,
		LastModified: mtime,
		FileID:       a.remote.FileID,
		Permissions:  a.remote.Permissions,
	})
}

// isConflictFile reports whether name matches the conflict-rename pattern
// "<base>.conflict-YYYYMMDD-HHMMSS[-N]<ext>".
func isConflictFile(name string) bool {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	const prefix = ".conflict-"
	idx := strings.LastIndex(base, prefix)
	if idx < 0 {
		return false
	}
	suffix := base[idx+len(prefix):]
	// "YYYYMMDD-HHMMSS" (15 chars), optionally followed by "-<number>".
	if len(suffix) < 15 {
		return false
	}
	if rest := suffix[15:]; rest != "" {
		if rest[0] != '-' || len(rest) == 1 {
			return false
		}
		for _, c := range rest[1:] {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	_, err := time.Parse("20060102-150405", suffix[:15])
	return err == nil
}

// conflictName appends a timestamp suffix before the file extension. If that
// name is taken (several conflicts within a second), a counter is added so an
// existing conflict copy is never overwritten.
func conflictName(abs string) string {
	ext := filepath.Ext(abs)
	base := strings.TrimSuffix(abs, ext)
	ts := time.Now().Format("20060102-150405")
	name := fmt.Sprintf("%s.conflict-%s%s", base, ts, ext)
	for n := 2; ; n++ {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s.conflict-%s-%d%s", base, ts, n, ext)
	}
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
	case conflictDeleteLocal:
		return "conflictDelete"
	case moveRemote:
		return "move"
	case renameLocal:
		return "renameLocal"
	default:
		return "unknown"
	}
}
