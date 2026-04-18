// Package logger provides helpers for configuring the slog default logger
// used throughout cernbox-sync.
//
// Levels (lowest to highest verbosity):
//
//	Off   — nothing is logged
//	Error — only errors
//	Info  — normal operational messages (default)
//	Debug — internal state changes, timing, decision points
//	Trace — every IPC message, raw payloads, fine-grained flow
package logger

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// LevelTrace is a custom slog level below Debug.
const LevelTrace = slog.Level(-8)

// LevelOff is a sentinel level that disables all logging.
const LevelOff = slog.Level(12) // above slog.LevelError (8)

// ParseLevel converts a string to an slog.Level.
// It is case-insensitive and returns an error for unknown names.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return LevelOff, nil
	case "error":
		return slog.LevelError, nil
	case "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "trace":
		return LevelTrace, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (valid: off, error, info, debug, trace)", s)
	}
}

// New creates an *slog.Logger writing to w at the given minimum level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	lv := &slog.LevelVar{}
	lv.Set(level)
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Replace the numeric level with a human-readable label.
			if a.Key == slog.LevelKey {
				l, _ := a.Value.Any().(slog.Level)
				switch {
				case l < slog.LevelDebug:
					a.Value = slog.StringValue("TRACE")
				case l >= LevelOff:
					// should never appear; suppress anyway
					return slog.Attr{}
				}
			}
			return a
		},
	})
	return slog.New(h)
}

// SetDefault installs l as the package-level slog default logger.
func SetDefault(l *slog.Logger) { slog.SetDefault(l) }
