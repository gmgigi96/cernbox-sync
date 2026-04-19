package logger_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/gmgigi96/cernbox-sync/logger"
)

func TestParseLevel_ValidLevels(t *testing.T) {
	cases := []struct {
		input string
		want  slog.Level
	}{
		{"off", logger.LevelOff},
		{"OFF", logger.LevelOff},
		{"  Off  ", logger.LevelOff},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"trace", logger.LevelTrace},
		{"TRACE", logger.LevelTrace},
	}
	for _, tc := range cases {
		got, err := logger.ParseLevel(tc.input)
		if err != nil {
			t.Errorf("ParseLevel(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseLevel(%q): got %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestParseLevel_UnknownLevel_ReturnsError(t *testing.T) {
	_, err := logger.ParseLevel("verbose")
	if err == nil {
		t.Fatal("expected error for unknown level, got nil")
	}
	if !strings.Contains(err.Error(), "unknown log level") {
		t.Errorf("error message: %q", err.Error())
	}
}

func TestNew_WritesToWriter(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, slog.LevelInfo)
	l.Info("hello from test")
	if !strings.Contains(buf.String(), "hello from test") {
		t.Errorf("expected log output to contain message, got: %q", buf.String())
	}
}

func TestNew_FiltersLevelsBelowMinimum(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, slog.LevelError)
	l.Info("should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected no output below minimum level, got: %q", buf.String())
	}
}

func TestNew_TraceLevel(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, logger.LevelTrace)
	l.Log(nil, logger.LevelTrace, "trace message")
	if !strings.Contains(buf.String(), "TRACE") {
		t.Errorf("expected TRACE label in output, got: %q", buf.String())
	}
}

func TestSetDefault_DoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, slog.LevelInfo)
	// Just ensure no panic; restoring the original is best-effort.
	logger.SetDefault(l)
}
