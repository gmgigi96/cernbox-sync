package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gmgigi96/cernbox-sync/config"
)

// startWatcher creates a recursive filesystem watcher and begins watching all
// registered folders that have AutoSyncOnChange enabled. Errors are logged but
// do not prevent the daemon from running.
func (d *Daemon) startWatcher(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		d.log.Error("[watcher] create watcher", "err", err)
		return
	}
	d.watcher = w

	go d.processWatchEvents(ctx)

	folders, err := d.cfgDB.All()
	if err != nil {
		d.log.Error("[watcher] list folders", "err", err)
		return
	}
	for _, f := range folders {
		if f.Settings.AutoSyncOnChange {
			d.addFolderWatch(f)
		}
	}
}

func (d *Daemon) processWatchEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			d.watcher.Close()
			return

		case event, ok := <-d.watcher.Events:
			if !ok {
				return
			}
			// Ignore changes to internal sync files to avoid feedback loops.
			// The engine writes .sync.db (plus SQLite WAL/SHM side-files) on
			// every sync cycle and uses .tmp-sync-* as staging files during
			// downloads. Events from any of these must not re-trigger a sync.
			if isInternalSyncFile(event.Name) {
				continue
			}

			// When a new directory is created inside a watched root, add it to
			// the watcher so that changes inside it are also tracked.
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					d.watchMu.Lock()
					for root := range d.watchedRoots {
						rel, relErr := filepath.Rel(root, event.Name)
						if relErr == nil && !strings.HasPrefix(rel, "..") {
							_ = d.watcher.Add(event.Name)
							break
						}
					}
					d.watchMu.Unlock()
				}
			}

			// Find the registered folder that contains this path.
			d.watchMu.Lock()
			var matched *config.Folder
			for root, f := range d.watchedRoots {
				rel, err := filepath.Rel(root, event.Name)
				if err == nil && !strings.HasPrefix(rel, "..") {
					fCopy := f
					matched = &fCopy
					break
				}
			}
			d.watchMu.Unlock()

			if matched == nil {
				continue
			}
			d.log.Debug("[watcher] event → debouncing sync", "op", event.Op, "path", event.Name, "folder", matched.Name)
			d.triggerDebouncedSync(*matched)

		case err, ok := <-d.watcher.Errors:
			if !ok {
				return
			}
			d.log.Error("[watcher] error", "err", err)
		}
	}
}

// addFolderWatch adds inotify watches for f.LocalRoot and all of its
// sub-directories that already exist. New directories created later are
// handled dynamically in processWatchEvents.
func (d *Daemon) addFolderWatch(f config.Folder) {
	if d.watcher == nil {
		return
	}
	var addedAny bool
	err := filepath.WalkDir(f.LocalRoot, func(path string, de os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if de.IsDir() {
			if err := d.watcher.Add(path); err != nil {
				d.log.Error("[watcher] watch", "path", path, "err", err)
			} else {
				addedAny = true
			}
		}
		return nil
	})
	if err != nil {
		d.log.Error("[watcher] walk", "root", f.LocalRoot, "err", err)
		return
	}
	if !addedAny {
		return
	}
	d.watchMu.Lock()
	d.watchedRoots[f.LocalRoot] = f
	d.watchMu.Unlock()
	d.log.Info("[watcher] watching for changes", "root", f.LocalRoot)
}

// removeFolderWatch removes all watches associated with localRoot and cancels
// any pending debounce timer for folderName.
func (d *Daemon) removeFolderWatch(folderName, localRoot string) {
	d.watchMu.Lock()
	_, watched := d.watchedRoots[localRoot]
	if watched {
		delete(d.watchedRoots, localRoot)
	}
	d.watchMu.Unlock()

	if d.watcher != nil && watched {
		// Walk and remove watches for the root and all sub-directories.
		// Errors are best-effort (paths may have been deleted already).
		_ = filepath.WalkDir(localRoot, func(path string, de os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if de.IsDir() {
				_ = d.watcher.Remove(path)
			}
			return nil
		})
		d.log.Info("[watcher] stopped watching", "root", localRoot)
	}

	// Cancel any pending debounce so a stale sync is not triggered after removal.
	d.debounceMu.Lock()
	if timer, ok := d.debounceTimers[folderName]; ok {
		timer.Stop()
		delete(d.debounceTimers, folderName)
	}
	d.debounceMu.Unlock()
}

// updateFolderWatch adds or removes a watch depending on AutoSyncOnChange.
func (d *Daemon) updateFolderWatch(f config.Folder) {
	if f.Settings.AutoSyncOnChange {
		d.addFolderWatch(f)
	} else {
		d.removeFolderWatch(f.Name, f.LocalRoot)
	}
}

// isInternalSyncFile reports whether the path belongs to a file written by the
// sync engine itself (.sync.db and its SQLite WAL/SHM side-files, temporary
// download staging files, and the sync log).
func isInternalSyncFile(path string) bool {
	base := filepath.Base(path)
	// Catch .sync.db plus all SQLite auxiliary files:
	// .sync.db-journal (DELETE mode), .sync.db-wal, .sync.db-shm (WAL mode).
	return strings.HasPrefix(base, ".sync.db") ||
		base == ".sync.log" ||
		strings.HasPrefix(base, ".tmp-sync-")
}

// triggerDebouncedSync schedules a sync for f debounceDuration after the last
// filesystem event. If a timer is already pending it is reset.
func (d *Daemon) triggerDebouncedSync(f config.Folder) {
	d.debounceMu.Lock()
	defer d.debounceMu.Unlock()

	if timer, ok := d.debounceTimers[f.Name]; ok {
		timer.Stop()
	}
	d.debounceTimers[f.Name] = time.AfterFunc(d.debounceDuration, func() {
		d.debounceMu.Lock()
		delete(d.debounceTimers, f.Name)
		d.debounceMu.Unlock()

		// Re-read config to pick up any settings changes made since the event.
		fresh, err := d.cfgDB.Get(f.Name)
		if err != nil || fresh == nil {
			return
		}
		d.log.Info("[watcher] triggering sync after filesystem change", "folder", f.Name)
		d.syncFolder(*fresh)
	})
}
