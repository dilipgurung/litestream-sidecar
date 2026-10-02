package litestream

import (
	"log/slog"
	"testing"
)

// compileTimeCheck verifies at compile time that *slog.Logger satisfies Logger.
func TestLoggerInterface_compatibleWithSlog(t *testing.T) {
	// This is a compile-time check: *slog.Logger should satisfy Logger.
	var _ Logger = slog.Default()

	// Ensure it works at runtime too — no panic from the assignment.
	logger := slog.Default()
	if logger == nil {
		t.Fatal("slog.Default() returned nil")
	}
}

func TestLoggerInterface_stdioLoggerDoesNotPanic(t *testing.T) {
	var l Logger = stdioLogger{}
	// Should be safe to call
	l.Info("hello", "key", "value")
	l.Error("world", "err", "something")

	// Call with no args
	l.Info("no-args")
	l.Error("no-args")
}
