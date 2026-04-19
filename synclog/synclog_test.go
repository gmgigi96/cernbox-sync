package synclog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/synclog"
)

func openLogger(t *testing.T, maxAge time.Duration) (*synclog.Logger, string) {
	t.Helper()
	dir := t.TempDir()
	l, err := synclog.Open(dir, maxAge)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l, filepath.Join(dir, synclog.LogName)
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readLog: %v", err)
	}
	return string(b)
}

// ── Open / Close ──────────────────────────────────────────────────────────────

func TestOpen_CreatesLogFile(t *testing.T) {
	dir := t.TempDir()
	l, err := synclog.Open(dir, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	path := filepath.Join(dir, synclog.LogName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
}

func TestOpen_NonExistentDir_ReturnsError(t *testing.T) {
	_, err := synclog.Open("/nonexistent/path/that/does/not/exist", 0)
	if err == nil {
		t.Fatal("expected error opening log in non-existent dir")
	}
}

// ── Printf ────────────────────────────────────────────────────────────────────

func TestPrintf_WritesTimestampedLine(t *testing.T) {
	l, path := openLogger(t, 0)
	l.Printf("downloaded %s", "file.txt")
	l.Close()

	content := readLog(t, path)
	if !strings.Contains(content, "downloaded file.txt") {
		t.Errorf("log content missing message: %q", content)
	}
	// RFC3339 timestamps contain 'T' between date and time.
	if !strings.Contains(content, "T") {
		t.Errorf("log content missing timestamp: %q", content)
	}
}

func TestPrintf_MultipleLines(t *testing.T) {
	l, path := openLogger(t, 0)
	l.Printf("line one")
	l.Printf("line two")
	l.Printf("line three")
	l.Close()

	content := readLog(t, path)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %q", len(lines), content)
	}
}

// ── Rotate ────────────────────────────────────────────────────────────────────

func TestRotate_NoMaxAge_IsNoop(t *testing.T) {
	l, path := openLogger(t, 0) // maxAge = 0 disables rotation
	l.Printf("old line")
	if err := l.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	// File should be unchanged.
	content := readLog(t, path)
	if !strings.Contains(content, "old line") {
		t.Errorf("rotate with no maxAge removed entries unexpectedly: %q", content)
	}
}

func TestRotate_RemovesOldEntries(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, synclog.LogName)

	// Write two old entries directly into the file (2 hours ago).
	oldTS := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	oldContent := oldTS + " old entry one\n" + oldTS + " old entry two\n"
	if err := os.WriteFile(logPath, []byte(oldContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Open logger with a 1-hour maxAge so the 2-hour-old entries are trimmed.
	l, err := synclog.Open(dir, time.Hour)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	l.Printf("new entry")

	if err := l.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	content := readLog(t, logPath)
	if strings.Contains(content, "old entry") {
		t.Errorf("old entries should have been rotated away: %q", content)
	}
	if !strings.Contains(content, "new entry") {
		t.Errorf("new entry should be kept: %q", content)
	}
}

func TestRotate_KeepsRecentEntries(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, synclog.LogName)

	recentTS := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	recentContent := recentTS + " recent entry\n"
	if err := os.WriteFile(logPath, []byte(recentContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l, err := synclog.Open(dir, time.Hour) // maxAge 1h, entry is 10min old
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if err := l.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	content := readLog(t, logPath)
	if !strings.Contains(content, "recent entry") {
		t.Errorf("recent entry should have been kept: %q", content)
	}
}

func TestRotate_UnparsableLines_AreKept(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, synclog.LogName)

	// Write a line without a parsable timestamp.
	if err := os.WriteFile(logPath, []byte("not-a-timestamp something happened\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l, err := synclog.Open(dir, time.Millisecond) // very short maxAge
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if err := l.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	content := readLog(t, logPath)
	if !strings.Contains(content, "not-a-timestamp") {
		t.Errorf("unparsable line should have been kept defensively: %q", content)
	}
}

func TestRotate_EmptyFile(t *testing.T) {
	l, _ := openLogger(t, time.Hour)
	if err := l.Rotate(); err != nil {
		t.Fatalf("Rotate on empty file: %v", err)
	}
}
