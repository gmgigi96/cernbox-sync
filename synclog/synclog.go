// Package synclog manages the per-folder sync log file (.sync.log).
//
// Each synced directory gets a hidden log file that records every action
// taken during sync cycles (downloads, uploads, deletes, conflicts, errors).
// Log rotation trims entries older than a configurable maximum age so the
// file does not grow unboundedly.
package synclog

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LogName is the name of the per-folder log file inside the local root.
const LogName = ".sync.log"

// Logger writes timestamped lines to a per-folder log file.
type Logger struct {
	path   string
	maxAge time.Duration // 0 means no rotation
	f      *os.File
}

// Open opens (or creates) the log file at <localRoot>/.sync.log.
// maxAge controls how old entries are trimmed during rotation; pass 0 to
// disable rotation.
func Open(localRoot string, maxAge time.Duration) (*Logger, error) {
	path := filepath.Join(localRoot, LogName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sync log %s: %w", path, err)
	}
	return &Logger{path: path, maxAge: maxAge, f: f}, nil
}

// Close flushes and closes the underlying file.
func (l *Logger) Close() error { return l.f.Close() }

// Printf writes a formatted line prefixed with a timestamp.
func (l *Logger) Printf(format string, args ...any) {
	ts := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.f, "%s %s\n", ts, line)
}

// Rotate removes log entries older than maxAge. It is a no-op when maxAge
// is 0. Rotation rewrites the file in-place so the file descriptor held by
// the logger remains valid after the call.
func (l *Logger) Rotate() error {
	if l.maxAge == 0 {
		return nil
	}

	cutoff := time.Now().Add(-l.maxAge)

	// Close the write handle before reading/rewriting.
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("close log before rotate: %w", err)
	}

	kept, err := filterLines(l.path, cutoff)
	if err != nil {
		// Re-open append handle even on error so the logger stays usable.
		l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		return err
	}

	// Rewrite file with only the kept lines.
	tmp := l.path + ".rotate-tmp"
	tf, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		return fmt.Errorf("create rotate tmp: %w", err)
	}
	for _, line := range kept {
		fmt.Fprintln(tf, line)
	}
	tf.Close()

	if err := os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp)
		l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		return fmt.Errorf("rename rotate tmp: %w", err)
	}

	// Re-open for appending.
	l.f, err = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reopen log after rotate: %w", err)
	}
	return nil
}

// filterLines reads the log file and returns only lines whose timestamp is
// at or after cutoff. Lines that cannot be parsed are kept (defensive).
func filterLines(path string, cutoff time.Time) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open log for rotate: %w", err)
	}
	defer f.Close()

	var kept []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		ts, ok := parseTimestamp(line)
		if !ok || !ts.Before(cutoff) {
			kept = append(kept, line)
		}
	}
	return kept, scanner.Err()
}

// parseTimestamp extracts the leading RFC3339 timestamp from a log line.
func parseTimestamp(line string) (time.Time, bool) {
	// Lines are: "<RFC3339> <message>"
	before, _, ok := strings.Cut(line, " ")
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, before)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
