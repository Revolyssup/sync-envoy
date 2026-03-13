package logging

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestSetLevel(t *testing.T) {
	tests := []struct {
		name  string
		ok    bool
		level int
	}{
		{"debug", true, LevelDebug},
		{"info", true, LevelInfo},
		{"warn", true, LevelWarn},
		{"error", true, LevelError},
		{"DEBUG", true, LevelDebug},
		{"invalid", false, 0},
	}
	for _, tt := range tests {
		ok := SetLevel(tt.name)
		if ok != tt.ok {
			t.Errorf("SetLevel(%q) returned %v, want %v", tt.name, ok, tt.ok)
		}
		if ok && GetLevel() != tt.level {
			t.Errorf("SetLevel(%q) set level to %d, want %d", tt.name, GetLevel(), tt.level)
		}
	}
}

func TestLogFunctions(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	log.SetFlags(0)

	SetLevel("debug")

	buf.Reset()
	Debug("test %s", "debug")
	if !strings.Contains(buf.String(), "[DEBUG] test debug") {
		t.Errorf("Debug output: %q", buf.String())
	}

	buf.Reset()
	Info("test %s", "info")
	if !strings.Contains(buf.String(), "[INFO] test info") {
		t.Errorf("Info output: %q", buf.String())
	}

	buf.Reset()
	Warn("test %s", "warn")
	if !strings.Contains(buf.String(), "[WARN] test warn") {
		t.Errorf("Warn output: %q", buf.String())
	}

	buf.Reset()
	Errorf("test %s", "error")
	if !strings.Contains(buf.String(), "[ERROR] test error") {
		t.Errorf("Error output: %q", buf.String())
	}
}

func TestLogLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	log.SetFlags(0)

	SetLevel("warn")

	buf.Reset()
	Debug("should not appear")
	Info("should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected no output at warn level for debug/info, got: %q", buf.String())
	}

	buf.Reset()
	Warn("should appear")
	if !strings.Contains(buf.String(), "should appear") {
		t.Errorf("expected warn output, got: %q", buf.String())
	}

	buf.Reset()
	Errorf("should appear")
	if !strings.Contains(buf.String(), "should appear") {
		t.Errorf("expected error output, got: %q", buf.String())
	}
}
