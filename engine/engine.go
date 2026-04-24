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
	"fmt"
	"io"
	"log/slog"
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

func newRateLimitedReader(r io.Reader, lim *rate.Limiter, counter *bandwidthCounter) io.Reader {
	if lim == nil && counter == nil {
		return r
	}
	burst := 0
	if lim != nil {
		burst = lim.Burst()
	}
	return &rateLimitedReader{r: r, lim: lim, burstSize: burst, counter: counter}
}

func (rl *rateLimitedReader) Read(p []byte) (int, error) {
	// Cap the read to the burst size so WaitN never exceeds the limiter's burst.
	if rl.lim != nil && len(p) > rl.burstSize {
		p = p[:rl.burstSize]
	}
	n, err := rl.r.Read(p)
	if n > 0 {
		if rl.lim != nil {
			if waitErr := rl.lim.WaitN(context.Background(), n); waitErr != nil {
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
	download     actionKind = iota // remote → local
	upload                         // local → remote
	deleteLocal                    // remote was deleted; remove local
	deleteRemote                   // local was deleted; remove remote
	mkcolRemote                    // create remote directory
	mkdirLocal                     // create local directory
	conflictTake                   // conflict: server wins, rename local
	conflictDeleteLocal            // remote deleted while local changed: rename local as conflict, then delete
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
	ctx := cfg.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	logf(cfg.FolderLog, "[sync] starting — local: %s  remote: %s", cfg.LocalRoot, cfg.RemoteBase)

	wdc := webdav.NewClient(cfg.RemoteBase, cfg.Username, cfg.Password)

	state, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer state.Close()

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
	remoteMap, err := scanRemote(wdc, cfg.Folders, cfg.SyncHiddenFiles, dbState)
	if err != nil {
		return fmt.Errorf("remote scan: %w", err)
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
	logf(cfg.FolderLog, "[sync] local resources: %d", len(localMap))

	// ── 4. Classify ──────────────────────────────────────────────────────────
	actions := classify(remoteMap, localMap, dbState)
	logf(cfg.FolderLog, "[sync] actions: %d", len(actions))

	// ── 5. Execute ───────────────────────────────────────────────────────────
	uploadCounter := &bandwidthCounter{}
	downloadCounter := &bandwidthCounter{}
	if err := execute(ctx, cfg.LocalRoot, cfg.FolderLog, wdc, state, actions, cfg.OnProgress, cfg.UploadLimiter, cfg.DownloadLimiter, uploadCounter, downloadCounter, cfg.TransferStreams, cfg.MetadataStreams); err != nil {
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
		if rel == ".sync.db" || rel == ".sync.log" || isConflictFile(d.Name()) {
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
	ctx context.Context,
	localRoot string,
	fl *synclog.Logger,
	wdc *webdav.Client,
	state *db.DB,
	actions []action,
	onProgress func(done, total int, current string, uploadBps, downloadBps, bytesDone, bytesTotal int64),
	uploadLimiter, downloadLimiter *rate.Limiter,
	uploadCounter, downloadCounter *bandwidthCounter,
	transferStreams, metadataStreams int,
) error {
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
		fileMu        sync.Mutex
		fileProgress  = make(map[string][2]int64) // path → [bytesDone, bytesTotal]
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

	// onByteProgress is passed into execOne so uploads/downloads can report
	// streaming byte progress.
	onByteProgress := func(path string, bytesDone, bytesTotal int64) {
		setFileProgress(path, bytesDone, bytesTotal)
		// No need to call onProgress here; the ticker fires every second.
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
					if err := execOne(localRoot, fl, wdc, state, a, uploadLimiter, downloadLimiter, uploadCounter, downloadCounter, onByteProgress); err != nil {
						logf(fl, "[sync] ERROR %s %q: %v", kindName(a.kind), a.path, err)
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
	var creates, conflicts, transfers, teardown []action
	for _, a := range actions {
		switch a.kind {
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

	// Phase 1a — concurrent dir creates, tier by tier (parents before children).
	runByDepthTier(creates, metadataStreams)

	// Phase 1b — conflicts: rename local then download; independent of each other.
	runPool(conflicts, transferStreams)

	// Phase 2 — concurrent file transfers.
	runPool(transfers, transferStreams)

	// Phase 3 — concurrent deletes, tier by tier (children before parents).
	runByDepthTier(teardown, metadataStreams)

	if onProgress != nil {
		up, down := bps()
		onProgress(total, total, "", up, down, 0, 0)
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
	uploadCounter, downloadCounter *bandwidthCounter,
	onByteProgress func(path string, bytesDone, bytesTotal int64),
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
		fileTotal := a.remote.Size
		src := newRateLimitedReader(rc, downloadLimiter, downloadCounter)
		if onByteProgress != nil && fileTotal > 0 {
			src = newByteProgressReader(src, a.path, fileTotal, onByteProgress)
		}
		if _, err := io.Copy(f, src); err != nil {
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
		src := newRateLimitedReader(f, uploadLimiter, uploadCounter)
		if onByteProgress != nil && a.local.size > 0 {
			src = newByteProgressReader(src, a.path, a.local.size, onByteProgress)
		}
		if err := wdc.Put(a.path, src, a.local.size); err != nil {
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
		if err := state.AddConflict(a.path, conflictPath, time.Now()); err != nil {
			logf(fl, "[sync] WARNING record conflict %q: %v", a.path, err)
		}
		// Now download the server version as if it were a fresh download.
		a.kind = download
		return execOne(localRoot, fl, wdc, state, a, uploadLimiter, downloadLimiter, uploadCounter, downloadCounter, onByteProgress)

	case conflictDeleteLocal:
		// Remote was deleted while local was modified: preserve local changes as a
		// conflict copy so no work is lost, then remove the original path.
		conflictPath := conflictName(localAbs)
		logf(fl, "[sync] conflict     %q — remote deleted, renaming local to %s", a.path, filepath.Base(conflictPath))
		if err := os.Rename(localAbs, conflictPath); err != nil {
			return fmt.Errorf("rename conflict copy: %w", err)
		}
		if err := state.AddConflict(a.path, conflictPath, time.Now()); err != nil {
			logf(fl, "[sync] WARNING record conflict %q: %v", a.path, err)
		}
		return state.Delete(a.path)
	}

	return nil
}

// isConflictFile reports whether name matches the conflict-rename pattern.
func isConflictFile(name string) bool {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	// base ends with ".conflict-YYYYMMDD-HHMMSS"
	const prefix = ".conflict-"
	idx := strings.LastIndex(base, prefix)
	if idx < 0 {
		return false
	}
	suffix := base[idx+len(prefix):]
	// expect exactly "YYYYMMDD-HHMMSS" (15 chars)
	if len(suffix) != 15 {
		return false
	}
	_, err := time.Parse("20060102-150405", suffix)
	return err == nil
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
	case conflictDeleteLocal:
		return "conflictDelete"
	default:
		return "unknown"
	}
}
