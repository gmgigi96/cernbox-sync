// Package logger provides a lightweight levelled logger for the cernbox-sync
// daemon.
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
	"log"
	"os"
	"strings"
)

// Level represents a log verbosity level.
type Level int

const (
	LevelOff   Level = iota // nothing logged
	LevelError              // errors only
	LevelInfo               // normal operation (default)
	LevelDebug              // internal state, timings
	LevelTrace              // every IPC message, raw payloads
)

// String returns the canonical name of the level.
func (l Level) String() string {
	switch l {
	case LevelOff:
		return "off"
	case LevelError:
		return "error"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	case LevelTrace:
		return "trace"
	default:
		return fmt.Sprintf("Level(%d)", int(l))
	}
}

// ParseLevel converts a string to a Level. It is case-insensitive and returns
// an error for unknown names.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return LevelOff, nil
	case "error":
		return LevelError, nil
	case "info":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "trace":
		return LevelTrace, nil
	default:
		return LevelInfo, fmt.Errorf("unknown log level %q (valid: off, error, info, debug, trace)", s)
	}
}

// Logger is a levelled logger that wraps the standard library logger.
type Logger struct {
	level  Level
	logger *log.Logger
}

// New creates a Logger that writes to w at the given level.
// flags are passed directly to log.New (e.g. log.Ldate|log.Ltime).
func New(w io.Writer, level Level, flags int) *Logger {
	return &Logger{
		level:  level,
		logger: log.New(w, "", flags),
	}
}

// Default returns a Logger wrapping the standard library's default logger.
func Default(level Level) *Logger {
	return &Logger{
		level:  level,
		logger: log.Default(),
	}
}

// SetLevel changes the active log level.
func (l *Logger) SetLevel(level Level) { l.level = level }

// Level returns the current active log level.
func (l *Logger) Level() Level { return l.level }

// Errorf logs at Error level.
func (l *Logger) Errorf(format string, args ...any) {
	if l.level >= LevelError {
		l.logger.Printf("[ERROR] "+format, args...)
	}
}

// Infof logs at Info level.
func (l *Logger) Infof(format string, args ...any) {
	if l.level >= LevelInfo {
		l.logger.Printf("[INFO]  "+format, args...)
	}
}

// Debugf logs at Debug level.
func (l *Logger) Debugf(format string, args ...any) {
	if l.level >= LevelDebug {
		l.logger.Printf("[DEBUG] "+format, args...)
	}
}

// Tracef logs at Trace level.
func (l *Logger) Tracef(format string, args ...any) {
	if l.level >= LevelTrace {
		l.logger.Printf("[TRACE] "+format, args...)
	}
}

// package-level default logger (Info, stderr).
var std = Default(LevelInfo)

// SetDefault replaces the package-level logger.
func SetDefault(l *Logger) { std = l }

// GetDefault returns the package-level logger.
func GetDefault() *Logger { return std }

// Convenience wrappers that use the package-level logger.

func Errorf(format string, args ...any) { std.Errorf(format, args...) }
func Infof(format string, args ...any)  { std.Infof(format, args...) }
func Debugf(format string, args ...any) { std.Debugf(format, args...) }
func Tracef(format string, args ...any) { std.Tracef(format, args...) }

// NewStderr is a convenience constructor that writes to stderr with the
// standard date+time flags.
func NewStderr(level Level) *Logger {
	return New(os.Stderr, level, log.Ldate|log.Ltime|log.Lmsgprefix)
}
