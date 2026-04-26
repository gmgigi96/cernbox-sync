//go:build !windows

package cloudfiles

import (
	"context"

	"github.com/gmgigi96/cernbox-sync/webdav"
)

// New returns ErrUnsupported on platforms without Cloud Files API support.
// The daemon should detect this and either skip the folder or refuse to
// register an on-demand folder up front.
func New(_ Config) (Provider, error) {
	return nil, ErrUnsupported
}

// SetPinState always returns ErrUnsupported on non-Windows platforms.
// The daemon still persists the intent in its DB so a future Windows
// daemon can replay it.
func SetPinState(_ string, _ bool) error {
	return ErrUnsupported
}

// stubProvider is here for reference only — it is not constructible. Its
// purpose is to ensure the Provider interface stays implementable across
// platforms (compile-time check below).
type stubProvider struct{}

func (stubProvider) Create(string, webdav.Resource) error { return ErrUnsupported }
func (stubProvider) Update(string, webdav.Resource) error { return ErrUnsupported }
func (stubProvider) Start(context.Context) error          { return ErrUnsupported }
func (stubProvider) Stop() error                          { return ErrUnsupported }
func (stubProvider) Pin(string) error                     { return ErrUnsupported }
func (stubProvider) Unpin(string) error                   { return ErrUnsupported }

var _ Provider = stubProvider{}
