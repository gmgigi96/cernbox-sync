//go:build windows

package cloudfiles

import (
	"context"
	"errors"

	"github.com/gmgigi96/cernbox-sync/webdav"
)

// errNotImplemented marks methods whose Windows-side bodies will be filled
// in by later phases (CF API registration, placeholder creation, callbacks,
// pinning). Until then they fail loudly so a misconfigured daemon doesn't
// silently turn into a normal-mode sync.
var errNotImplemented = errors.New("cloudfiles: not implemented yet")

// winProvider is the Windows Cloud Files API-backed implementation of
// Provider. The fields are deliberately empty in this phase — subsequent
// phases will add the cgo handles for the sync root and callback registration.
type winProvider struct {
	cfg Config
}

// New constructs a Provider for the given configuration. On Windows it
// returns a winProvider; the actual CF API plumbing arrives in later phases.
func New(cfg Config) (Provider, error) {
	if cfg.LocalRoot == "" {
		return nil, errors.New("cloudfiles: LocalRoot is required")
	}
	if cfg.Fetch == nil {
		return nil, errors.New("cloudfiles: Fetch is required")
	}
	return &winProvider{cfg: cfg}, nil
}

func (p *winProvider) Start(ctx context.Context) error {
	return errNotImplemented
}

func (p *winProvider) Stop() error {
	return errNotImplemented
}

func (p *winProvider) Create(absPath string, r webdav.Resource) error {
	return errNotImplemented
}

func (p *winProvider) Update(absPath string, r webdav.Resource) error {
	return errNotImplemented
}

func (p *winProvider) Pin(relPath string) error {
	return errNotImplemented
}

func (p *winProvider) Unpin(relPath string) error {
	return errNotImplemented
}

var _ Provider = (*winProvider)(nil)
