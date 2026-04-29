//go:build windows

package daemon

import (
	"context"
	"io"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/engine"
)

// ensureProvider returns the cloudfiles.Provider for folder f, creating and
// starting it on the first call. Subsequent calls for the same folder name
// return the cached instance so the OS callback connection stays alive
// between sync cycles. Returns nil if CF API is unavailable (non-fatal: the
// engine falls back to downloading files normally).
func (d *Daemon) ensureProvider(f config.Folder) cloudfiles.Provider {
	d.providerMu.Lock()
	defer d.providerMu.Unlock()

	if p, ok := d.providers[f.Name]; ok {
		return p
	}

	p, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot:  f.LocalRoot,
		FolderName: f.Name,
		Fetch:      d.makeFetch(f),
	})
	if err != nil {
		d.log.Warn("[daemon] on-demand sync unavailable", "folder", f.Name, "err", err)
		return nil
	}
	if err := p.Start(context.Background()); err != nil {
		d.log.Warn("[daemon] cloudfiles provider start failed", "folder", f.Name, "err", err)
		return nil
	}
	d.providers[f.Name] = p
	d.log.Info("[daemon] on-demand sync enabled", "folder", f.Name, "local", f.LocalRoot)
	return p
}

// makeFetch builds the FetchFunc for a folder. It reads credentials and the
// download limiter from the daemon at call time, so set-account updates are
// picked up without restarting the provider.
func (d *Daemon) makeFetch(f config.Folder) cloudfiles.FetchFunc {
	return func(_ context.Context, relPath string) (io.ReadCloser, error) {
		d.log.Info("[daemon] fetch placeholder", "folder", f.Name, "rel", relPath)
		d.mu.Lock()
		cfg := engine.Config{
			RemoteBase:      f.RemoteBase,
			Username:        d.accountUsername,
			Password:        d.accountPassword,
			DownloadLimiter: d.downloadLimiter,
		}
		d.mu.Unlock()
		return engine.FetchFile(cfg, relPath)
	}
}

// stopFolderProvider stops the provider for the named folder and removes it
// from the cache. The sync root stays registered with the OS so the folder
// retains its cloud-aware icon across daemon restarts.
func (d *Daemon) stopFolderProvider(name string) {
	d.providerMu.Lock()
	p, ok := d.providers[name]
	if ok {
		delete(d.providers, name)
	}
	d.providerMu.Unlock()

	if ok {
		if err := p.Stop(); err != nil {
			d.log.Warn("[daemon] cloudfiles provider stop", "folder", name, "err", err)
		}
	}
}

// stopAllProviders stops every running provider. Called on daemon shutdown.
func (d *Daemon) stopAllProviders() {
	d.providerMu.Lock()
	names := make([]string, 0, len(d.providers))
	for name := range d.providers {
		names = append(names, name)
	}
	d.providerMu.Unlock()

	for _, name := range names {
		d.stopFolderProvider(name)
	}
}
