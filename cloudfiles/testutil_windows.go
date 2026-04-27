//go:build windows

package cloudfiles

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
)

// SyncRootTempDir creates a top-level test directory suitable for use as a
// Cloud Files sync root. The OS rejects sync-root paths under AppData
// (which is where t.TempDir() places them) with an "invalid path" error
// from GetFolderFromPathAsync, so tests must point somewhere else.
//
// The directory is created under C:\cernbox-sync-test\<random> and removed
// in a t.Cleanup. Callers that exercise registration should remember to
// also unregister the sync root before letting the cleanup remove the
// directory.
func SyncRootTempDir(t *testing.T) string {
	t.Helper()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	// CF sync roots seem to behave best at the volume root rather than
	// nested under another directory, so each test gets its own top-level
	// folder. The hex suffix avoids collisions across parallel runs.
	root := `C:\cernbox-test-` + hex.EncodeToString(buf[:])
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
