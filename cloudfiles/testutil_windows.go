//go:build windows

package cloudfiles

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// SyncRootTempDir creates a test directory suitable for use as a Cloud
// Files sync root. The path is under the calling user's home directory
// (e.g. C:\Users\testuser\cernbox-test-<hex>) because the modern WinRT
// registration API (StorageProviderSyncRootManager.Register) refuses
// paths outside the user profile with HRESULT 0x80004005 (E_FAIL),
// even when the calling MSIX has runFullTrust + cloudFiles capabilities.
// We previously rooted these at C:\... for the legacy CfRegisterSyncRoot
// path; that's no longer accepted under Phase 2's WinRT path.
//
// Note that the actual path must NOT be under user library locations
// (Documents, Pictures, ...) either - those raise the same E_FAIL. A
// plain home-dir subdirectory works.
//
// The directory is removed in a t.Cleanup. Callers that exercise
// registration should remember to also unregister the sync root before
// letting the cleanup remove the directory.
func SyncRootTempDir(t *testing.T) string {
	t.Helper()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir: %v", err)
	}
	root := filepath.Join(home, "cernbox-test-"+hex.EncodeToString(buf[:]))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir sync root %q: %v", root, err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup of %q failed: %v\n", root, err)
		}
	})
	return root
}
