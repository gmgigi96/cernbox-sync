//go:build !windows

package daemon

import (
	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/config"
)

// On non-Windows platforms the Cloud Files API is not available, so these
// are no-ops. The engine runs in normal download mode (Config.Placeholders
// is nil).

func (d *Daemon) ensureProvider(_ config.Folder) cloudfiles.Provider { return nil }
func (d *Daemon) stopFolderProvider(_ string)                        {}
func (d *Daemon) stopAllProviders()                                  {}
func (d *Daemon) removeFolderProvider(_, _ string)                   {}
