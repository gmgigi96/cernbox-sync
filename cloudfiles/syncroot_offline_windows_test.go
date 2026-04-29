//go:build windows

package cloudfiles_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/cloudfiles"
	"github.com/gmgigi96/cernbox-sync/webdav"
)

// TestSyncRoot_ListAndReadDoNotHang is the end-to-end regression test for
// the bug that made every Explorer-shell access to a fresh on-demand folder
// hang with "cloud operation not completed before the time-out period
// expired."
//
// The previous code passed CF_REGISTER_FLAG_NONE to CfRegisterSyncRoot,
// which left the sync-root directory marked FILE_ATTRIBUTE_OFFLINE +
// FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS. Every shell-path directory
// enumeration (Explorer, `cmd dir`, `Get-ChildItem`) routed through the
// cloud filter and timed out waiting for a recall response.
//
// The fix passes CF_REGISTER_FLAG_MARK_IN_SYNC_ON_ROOT |
// CF_REGISTER_FLAG_DISABLE_ON_DEMAND_POPULATION_ON_ROOT instead, so the
// root is registered as already-in-sync and not subject to on-demand
// population. Child placeholders still hydrate normally via FETCH_DATA.
//
// This test asserts both halves of the contract:
//
//  1. Shell-path listing of the sync root completes well within the
//     recall timeout and returns the placeholder.
//  2. Reading the placeholder file goes through FETCH_DATA → Fetch and
//     returns the bytes the (fake) server provided.
func TestSyncRoot_ListAndReadDoNotHang(t *testing.T) {
	const (
		attrOffline            uint32 = 0x1000
		attrRecallOnDataAccess uint32 = 0x400000

		fileName = "hello.txt"
		content  = "bytes from the cloud"
	)

	root := cloudfiles.SyncRootTempDir(t)

	var fetchCount int
	provider, err := cloudfiles.New(cloudfiles.Config{
		LocalRoot:  root,
		FolderName: "syncroot-list-read",
		Fetch: func(_ context.Context, rel string) (io.ReadCloser, error) {
			fetchCount++
			if rel != fileName {
				t.Errorf("Fetch called with unexpected rel=%q, want %q", rel, fileName)
			}
			return io.NopCloser(strings.NewReader(content)), nil
		},
	})
	if err != nil {
		t.Fatalf("cloudfiles.New: %v", err)
	}
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Provider.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Stop()
		if u, ok := provider.(interface{ Unregister() error }); ok {
			_ = u.Unregister()
		}
	})

	// Sync root must NOT be marked OFFLINE / RECALL_ON_DATA_ACCESS — those
	// are the flags that cause shell-path access to wait on the cloud filter.
	attrs := mustGetAttributes(t, root)
	if attrs&attrOffline != 0 {
		t.Errorf("sync root has FILE_ATTRIBUTE_OFFLINE (attrs=0x%x); shell access will hang", attrs)
	}
	if attrs&attrRecallOnDataAccess != 0 {
		t.Errorf("sync root has FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS (attrs=0x%x); shell access will hang", attrs)
	}

	// Place one placeholder, exactly as the engine does for a remote-new file.
	abs := filepath.Join(root, fileName)
	if err := provider.Create(abs, webdav.Resource{
		Path:         fileName,
		Size:         int64(len(content)),
		LastModified: time.Now().UTC().Add(-time.Hour).Truncate(time.Second),
		ETag:         "etag-hello",
	}); err != nil {
		t.Fatalf("Create placeholder: %v", err)
	}

	// (1) Shell-path listing must complete promptly. Both `cmd dir` and
	// `Get-ChildItem` reproduce the user's failure mode — Explorer uses
	// the same kernel/shell path. 5s is a hugely generous upper bound;
	// before the fix these calls hung the full Windows recall timeout
	// (~30s+) and then returned empty.
	if out := runShellList(t, 5*time.Second, "cmd.exe", "/c", "dir", "/b", root); !strings.Contains(out, fileName) {
		t.Errorf("cmd `dir /b` did not surface placeholder %q. Output:\n%s", fileName, out)
	}
	if out := runShellList(t, 5*time.Second, "powershell.exe", "-NoProfile", "-Command",
		"Get-ChildItem -LiteralPath '"+root+"' | Select-Object -ExpandProperty Name"); !strings.Contains(out, fileName) {
		t.Errorf("PowerShell `Get-ChildItem` did not surface placeholder %q. Output:\n%s", fileName, out)
	}

	// (2) Reading the placeholder must go through FETCH_DATA → Fetch →
	// and return what the server gave us.
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile (hydration): %v", err)
	}
	if string(got) != content {
		t.Errorf("hydrated content = %q, want %q", string(got), content)
	}
	if fetchCount == 0 {
		t.Error("Fetch callback was never invoked — FETCH_DATA didn't fire")
	}
}

// mustGetAttributes returns the Win32 file-attribute bitmask for path via
// PowerShell's Get-Item (reliable even when shell-path enumeration is
// broken, since it goes through the .NET FileInfo path rather than the
// shell namespace).
func mustGetAttributes(t *testing.T, path string) uint32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command",
		"(Get-Item -Force -LiteralPath '"+path+"').Attributes.value__").Output()
	if err != nil {
		t.Fatalf("Get-Item attributes: %v", err)
	}
	s := strings.TrimSpace(string(out))
	var v uint32
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("unexpected attribute output %q", s)
		}
		v = v*10 + uint32(c-'0')
	}
	return v
}

// runShellList runs a directory-listing command with a hard timeout. A
// hang past the deadline t.Fatal's, since "still going after 5 seconds"
// for a one-file directory means we're back to the OFFLINE+RECALL state
// that prompted this whole test.
func runShellList(t *testing.T, d time.Duration, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("%s %v hung past %v — sync root is back in the OFFLINE/RECALL state the fix was meant to prevent", name, args, d)
	}
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}
