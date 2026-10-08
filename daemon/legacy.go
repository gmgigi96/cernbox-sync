package daemon

import (
	"errors"
	"sync"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/migrate"
)

// Taking over the sync folders of the ownCloud / CERNBox desktop client. The
// logic lives in package migrate; this wires it to the IPC commands.

// legacyDetect lists the desktop-client folders on r.ServerURL and, when
// asked, plans their import.
func (d *Daemon) legacyDetect(r ipc.LegacyRequest) ([]migrate.Client, error) {
	clients := migrate.ForServer(migrate.Detect(), r.ServerURL)
	if !r.Plan || len(clients) == 0 {
		return clients, nil
	}
	planner, err := d.legacyPlanner(r.ServerURL)
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	for _, c := range clients {
		for _, a := range c.Accounts {
			for i := range a.Folders {
				wg.Go(func() {
					plan := planner.Plan(a, a.Folders[i])
					a.Folders[i].Plan = &plan
				})
			}
		}
	}
	wg.Wait()
	return clients, nil
}

// legacyImport registers the requested desktop-client folders. Each folder is
// planned again here, so the import never relies on what the client sent.
func (d *Daemon) legacyImport(r ipc.LegacyRequest) (ipc.LegacyImportReport, error) {
	report := ipc.LegacyImportReport{Results: []ipc.LegacyImportResult{}}
	planner, err := d.legacyPlanner(r.ServerURL)
	if err != nil {
		return report, err
	}
	existing, err := d.cfgDB.All()
	if err != nil {
		return report, err
	}
	taken := map[string]bool{}
	for _, f := range existing {
		taken[f.Name] = true
	}

	clients := migrate.ForServer(migrate.Detect(), r.ServerURL)
	var limits *migrate.Client
	for _, ref := range r.Folders {
		res := ipc.LegacyImportResult{FolderRef: ref}
		f, err := d.importLegacyFolder(clients, planner, ref, taken)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Name = f.Name
			for i := range clients {
				if limits == nil && clients[i].ConfigPath == ref.ConfigPath {
					limits = &clients[i]
				}
			}
		}
		report.Results = append(report.Results, res)
	}
	if r.ImportLimits && limits != nil {
		if err := d.applyLegacyLimits(*limits); err != nil {
			report.LimitsError = err.Error()
		}
	}
	return report, nil
}

func (d *Daemon) importLegacyFolder(clients []migrate.Client, planner *migrate.Planner, ref migrate.FolderRef, taken map[string]bool) (config.Folder, error) {
	account, folder, ok := migrate.Find(clients, ref)
	if !ok {
		return config.Folder{}, errors.New("no longer configured in the desktop client")
	}
	plan := planner.Plan(account, folder)
	if !plan.Ready {
		return config.Folder{}, errors.New(plan.Reason)
	}
	baseline, err := migrate.Baseline(folder)
	if err != nil {
		return config.Folder{}, err
	}
	f, err := d.addFolder(config.Folder{
		Name:       migrate.UniqueName(folder.DisplayName, taken),
		LocalRoot:  folder.LocalPath,
		RemoteBase: plan.RemoteBase,
		Folders:    plan.Folders,
		Settings: config.FolderSettings{
			SyncHiddenFiles: !folder.IgnoreHiddenFiles,
			// The desktop client syncs as soon as local files change.
			AutoSyncOnChange: true,
			Paused:           folder.Paused,
		},
	}, baseline)
	if err != nil {
		return f, err
	}
	taken[f.Name] = true
	d.log.Info("[daemon] legacy-import: imported folder", "folder", f.Name, "from", folder.LocalPath, "baseline", len(baseline))

	d.mu.Lock()
	globalPaused := d.globalPaused
	d.mu.Unlock()
	if !f.Settings.Paused && !globalPaused {
		go d.syncFolder(f)
	}
	return f, nil
}

// legacyPlanner plans imports onto serverURL with the account of this app.
func (d *Daemon) legacyPlanner(serverURL string) (*migrate.Planner, error) {
	if serverURL == "" {
		return nil, errors.New("missing server URL")
	}
	d.mu.Lock()
	username, password := d.accountUsername, d.accountPassword
	d.mu.Unlock()
	if username == "" {
		return nil, errors.New("no account configured")
	}
	existing, err := d.cfgDB.All()
	if err != nil {
		return nil, err
	}
	return migrate.NewPlanner(serverURL, username, password, existing)
}

// applyLegacyLimits applies the desktop client's bandwidth limits where this
// app has none.
func (d *Daemon) applyLegacyLimits(c migrate.Client) error {
	s, err := d.cfgDB.GetSettings()
	if err != nil {
		return err
	}
	changed := false
	// The desktop client counts in KB/s of 1000 bytes.
	if s.UploadBandwidth == 0 && c.UploadLimitKBps > 0 {
		s.UploadBandwidth, changed = c.UploadLimitKBps*1000, true
	}
	if s.DownloadBandwidth == 0 && c.DownloadLimitKBps > 0 {
		s.DownloadBandwidth, changed = c.DownloadLimitKBps*1000, true
	}
	if !changed {
		return nil
	}
	if err := d.cfgDB.SetSettings(s); err != nil {
		return err
	}
	d.mu.Lock()
	d.uploadLimiter = newLimiter(s.UploadBandwidth)
	d.downloadLimiter = newLimiter(s.DownloadBandwidth)
	d.mu.Unlock()
	d.log.Info("[daemon] legacy-import: applied bandwidth limits", "upload", s.UploadBandwidth, "download", s.DownloadBandwidth)
	return nil
}
