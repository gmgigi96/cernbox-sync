package engine

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gmgigi96/cernbox-sync/db"
	"github.com/gmgigi96/cernbox-sync/webdav"
)

// Rename and move detection.
//
// Both kinds of rename are detected before classification by rewriting the
// scanned maps as they will look once the rename has been applied (the
// "virtual" state): a renamed directory takes its whole subtree along, so the
// ordinary classification then sees the renamed items as in sync, and only
// real content changes remain to be transferred.
//
//   - Local rename: a DB path is gone locally but unchanged on the server, and a
//     new local path has the same inode (and, for files, size and mtime). The
//     action is one MOVE on the server.
//   - Remote rename: a DB path is gone on the server, the same oc:fileid shows
//     up at a new path, and the local item is untouched. The action is an
//     os.Rename, without downloading anything.

type fileKey struct{ dev, ino uint64 }

func permAllows(perms, letter string) bool { return perms == "" || strings.Contains(perms, letter) }

// backfillIdentity records the inode of in-sync items that were synced before
// inodes were stored, so renames of them can be recognised too.
func backfillIdentity(state *db.DB, dbState map[string]*db.Entry, remoteMap map[string]*webdav.Resource, localMap map[string]*localInfo) {
	var upd []db.Entry
	for p, e := range dbState {
		if e.Inode != 0 || p == "" {
			continue
		}
		l, rem := localMap[p], remoteMap[p]
		if l == nil || rem == nil || l.ino == 0 || l.isDir != e.IsDir || (!e.IsDir && rem.ETag != e.ETag) || isLocalChanged(l, e) {
			continue
		}
		c := *e
		c.Device, c.Inode = l.dev, l.ino
		dbState[p] = &c
		upd = append(upd, c)
	}
	if len(upd) > 0 {
		_ = state.SetIdentities(upd) // best effort: only speeds up rename detection
	}
}

// detectRenames returns the rename actions and rewrites the three maps to the
// state they will have once those are executed.
func detectRenames(remoteMap map[string]*webdav.Resource, localMap map[string]*localInfo, dbState map[string]*db.Entry) []action {
	seq := 0
	out := detectLocalRenames(remoteMap, localMap, dbState, &seq)
	return append(out, detectRemoteRenames(remoteMap, localMap, dbState, &seq)...)
}

func byDepth(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		if di, dj := depth(paths[i]), depth(paths[j]); di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
}

func detectLocalRenames(remoteMap map[string]*webdav.Resource, localMap map[string]*localInfo, dbState map[string]*db.Entry, seq *int) []action {
	var out []action
	for {
		// New local paths by identity ("" marks an ambiguous identity, e.g. hard links).
		byID := make(map[fileKey]string)
		for p, l := range localMap {
			if p == "" || l.ino == 0 || dbState[p] != nil || remoteMap[p] != nil {
				continue
			}
			k := fileKey{l.dev, l.ino}
			if _, dup := byID[k]; dup {
				byID[k] = ""
			} else {
				byID[k] = p
			}
		}
		if len(byID) == 0 {
			return out
		}
		var cands []string
		for p, e := range dbState {
			if p == "" || e.Inode == 0 || localMap[p] != nil {
				continue
			}
			// A directory's etag also changes when something inside it does (or is
			// merely stale in the DB), so only files must be unchanged remotely;
			// the children of a moved directory are classified on their own.
			if rem := remoteMap[p]; rem != nil && rem.IsDir == e.IsDir && (e.IsDir || rem.ETag == e.ETag) {
				cands = append(cands, p)
			}
		}
		byDepth(cands)

		progress := false
		for _, p := range cands {
			e, rem := dbState[p], remoteMap[p]
			if e == nil || rem == nil || localMap[p] != nil {
				continue // already moved along with a parent
			}
			n := byID[fileKey{e.Device, e.Inode}]
			if n == "" || strings.HasPrefix(n, p+"/") {
				continue
			}
			l := localMap[n]
			if l == nil || dbState[n] != nil || remoteMap[n] != nil || l.isDir != e.IsDir {
				continue
			}
			if !e.IsDir && (l.size != e.Size || l.modTime.Unix() != e.LastModified.Unix()) {
				continue
			}
			// Server permissions: N (rename) and V (move) on the source, and the
			// right to create the item in the destination folder.
			if !permAllows(rem.Permissions, "N") || !permAllows(rem.Permissions, "V") {
				continue
			}
			need := "C"
			if e.IsDir {
				need = "K"
			}
			if par := remoteMap[parentOf(n)]; par != nil && !permAllows(par.Permissions, need) {
				continue
			}

			moves := relocate(p, n, e.IsDir, dbState, remoteMap, nil)
			delete(byID, fileKey{e.Device, e.Inode})
			*seq++
			out = append(out, action{kind: moveRemote, path: n, from: p, isDir: e.IsDir, moves: moves, seq: *seq})
			progress = true
		}
		if !progress {
			return out
		}
	}
}

func detectRemoteRenames(remoteMap map[string]*webdav.Resource, localMap map[string]*localInfo, dbState map[string]*db.Entry, seq *int) []action {
	var out []action
	for {
		byID := make(map[string]string) // file id -> new remote path ("" = ambiguous)
		for p, r := range remoteMap {
			if p == "" || r.FileID == "" || dbState[p] != nil || localMap[p] != nil {
				continue
			}
			if _, dup := byID[r.FileID]; dup {
				byID[r.FileID] = ""
			} else {
				byID[r.FileID] = p
			}
		}
		if len(byID) == 0 {
			return out
		}
		var cands []string
		for p, e := range dbState {
			if p == "" || e.FileID == "" || remoteMap[p] != nil {
				continue
			}
			if l := localMap[p]; l != nil && l.isDir == e.IsDir && !isLocalChanged(l, e) {
				cands = append(cands, p)
			}
		}
		byDepth(cands)

		progress := false
		for _, p := range cands {
			e, l := dbState[p], localMap[p]
			if e == nil || l == nil || remoteMap[p] != nil {
				continue
			}
			n := byID[e.FileID]
			if n == "" || strings.HasPrefix(n, p+"/") {
				continue
			}
			rem := remoteMap[n]
			if rem == nil || rem.IsDir != e.IsDir || dbState[n] != nil || localMap[n] != nil {
				continue
			}
			moves := relocate(p, n, e.IsDir, dbState, nil, localMap)
			// A rename can change the etag without touching the content; the
			// size and mtime tell the two apart, so nothing is downloaded for it.
			for i := range moves {
				to := &moves[i].To
				if r := remoteMap[to.Path]; r != nil && !to.IsDir && !r.IsDir && r.ETag != to.ETag &&
					r.Size == to.Size && r.LastModified.Unix() == to.LastModified.Unix() {
					to.ETag = r.ETag
					dbState[to.Path].ETag = r.ETag
				}
			}
			delete(byID, e.FileID)
			*seq++
			out = append(out, action{kind: renameLocal, path: n, from: p, isDir: e.IsDir, remote: rem, moves: moves, seq: *seq})
			progress = true
		}
		if !progress {
			return out
		}
	}
}

// relocate moves p, and for directories everything below it, to n in the given
// maps (nil maps are skipped) and returns the matching DB rewrites.
func relocate(p, n string, isDir bool, dbState map[string]*db.Entry, remoteMap map[string]*webdav.Resource, localMap map[string]*localInfo) []db.Move {
	keysOf := func(has func(string) bool, each func(func(string))) []string {
		if !isDir {
			if has(p) {
				return []string{p}
			}
			return nil
		}
		var ks []string
		each(func(k string) {
			if k == p || strings.HasPrefix(k, p+"/") {
				ks = append(ks, k)
			}
		})
		return ks
	}
	dst := func(k string) string { return n + k[len(p):] }

	var moves []db.Move
	if dbState != nil {
		ks := keysOf(func(k string) bool { return dbState[k] != nil }, func(f func(string)) {
			for k := range dbState {
				f(k)
			}
		})
		for _, k := range ks {
			e := *dbState[k]
			delete(dbState, k)
			e.Path = dst(k)
			dbState[e.Path] = &e
			moves = append(moves, db.Move{From: k, To: e})
		}
	}
	if remoteMap != nil {
		ks := keysOf(func(k string) bool { return remoteMap[k] != nil }, func(f func(string)) {
			for k := range remoteMap {
				f(k)
			}
		})
		for _, k := range ks {
			r := *remoteMap[k]
			delete(remoteMap, k)
			r.Path = dst(k)
			remoteMap[r.Path] = &r
		}
	}
	if localMap != nil {
		ks := keysOf(func(k string) bool { return localMap[k] != nil }, func(f func(string)) {
			for k := range localMap {
				f(k)
			}
		})
		for _, k := range ks {
			l := *localMap[k]
			delete(localMap, k)
			l.path = filepath.Join(strings.TrimSuffix(l.path, filepath.FromSlash(k)), filepath.FromSlash(dst(k)))
			localMap[dst(k)] = &l
		}
	}
	return moves
}

// interrupted reports whether err means the cycle is being stopped rather
// than that the rename itself failed.
func (x *executor) interrupted(err error) bool {
	return errors.Is(err, webdav.ErrUnauthorized) || x.ctx.Err() != nil
}

// moveRemote performs a detected local rename with a single MOVE.
func (x *executor) moveRemote(a action) error {
	logf(x.fl, "[sync] move remote  %q -> %q", a.from, a.path)
	if err := x.wdc.Move(a.from, a.path, false); err != nil {
		if x.interrupted(err) {
			return err
		}
		x.moveFailed(a.path)
		var se *webdav.StatusError
		if errors.As(err, &se) && se.Code >= 400 && se.Code < 500 && se.Code != http.StatusRequestTimeout && se.Code != http.StatusTooManyRequests {
			// The server will not do this move: forget the identity so the next
			// cycle uploads the item at its new path and deletes the old one.
			if cerr := x.state.ClearMoveHints(a.from); cerr != nil {
				logf(x.fl, "[sync] WARNING clear move hints %q: %v", a.from, cerr)
			}
		}
		return err
	}

	moves := append([]db.Move(nil), a.moves...)
	var top *db.Entry
	for i := range moves {
		if moves[i].To.Path == a.path {
			top = &moves[i].To
		}
	}
	// The server gives the moved item a new etag.
	res, err := x.wdc.Propfind(a.path, 0)
	if err != nil && x.interrupted(err) {
		return err
	}
	if err == nil && len(res) > 0 && top != nil {
		r := res[0]
		if a.isDir {
			// Committed with the other directories, once everything below is synced.
			x.deferDir(db.Entry{Path: a.path, ETag: r.ETag, IsDir: true, LastModified: r.LastModified, FileID: r.FileID, Permissions: r.Permissions})
		} else {
			top.ETag, top.FileID, top.Permissions = r.ETag, r.FileID, r.Permissions
		}
	}
	return x.state.Rename(moves)
}

// renameLocal performs a detected remote rename on the local file system.
func (x *executor) renameLocal(a action) error {
	logf(x.fl, "[sync] rename local %q -> %q", a.from, a.path)
	src := filepath.Join(x.localRoot, filepath.FromSlash(a.from))
	dst := filepath.Join(x.localRoot, filepath.FromSlash(a.path))
	err := func() error {
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("destination %q already exists", a.path)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Rename(src, dst)
	}()
	if err != nil {
		x.moveFailed(a.path)
		// Next cycle: delete the old local copy and download the new path.
		if cerr := x.state.ClearMoveHints(a.from); cerr != nil {
			logf(x.fl, "[sync] WARNING clear move hints %q: %v", a.from, cerr)
		}
		return err
	}
	return x.state.Rename(a.moves)
}
