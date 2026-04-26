//go:build windows

package cloudfiles_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
)

// fakeFetch satisfies cloudfiles.FetchFunc for tests that don't actually
// hydrate. Returns a zero-byte stream.
func fakeFetch(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// TestSyncRoot_RegisterConnect exercises the full register → connect →
// disconnect → unregister lifecycle against a temp directory. Only the
// CF API plumbing is tested here; placeholder operations land in later
// phases.
//
// This test requires running inside a Windows VM with the Cloud Files
// runtime available (every Windows 10 1709+ has it).
func TestSyncRoot_RegisterConnect(t *testing.T) {
	root := t.TempDir()

	p, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot:  root,
		FolderName: "test-folder",
		Fetch:      fakeFetch,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Idempotent: second Start should be a no-op.
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start (second): %v", err)
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Idempotent: second Stop should be a no-op.
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop (second): %v", err)
	}

	// Cleanup: unregister so the temp dir doesn't leave a sync-root entry
	// in the user's registry. winProvider exposes Unregister even though
	// it isn't part of the Provider interface (yet).
	type unregisterer interface {
		Unregister() error
	}
	if u, ok := p.(unregisterer); ok {
		if err := u.Unregister(); err != nil {
			t.Errorf("Unregister: %v", err)
		}
	}

	// Sanity: temp dir should still exist (CfUnregisterSyncRoot doesn't
	// delete the directory, just removes the cloud-aware status).
	if _, err := os.Stat(root); err != nil {
		t.Errorf("temp dir disappeared: %v", err)
	}
}
