//go:build !windows

package cloudfiles_test

import (
	"errors"
	"testing"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
)

// TestNew_UnsupportedOnNonWindows verifies the no-op stub: callers on
// Linux/macOS get ErrUnsupported and can fall back to normal sync.
func TestNew_UnsupportedOnNonWindows(t *testing.T) {
	_, err := cloudfiles.New(cloudfiles.Config{LocalRoot: "/tmp/x"})
	if !errors.Is(err, cloudfiles.ErrUnsupported) {
		t.Errorf("New on non-Windows: got err=%v, want ErrUnsupported", err)
	}
}
