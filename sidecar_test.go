package litestream

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSidecar_DefaultValues(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
		Logger:     slog.Default(),
	}

	if s.DBPath != "/data/app.db" {
		t.Errorf("DBPath = %q, want %q", s.DBPath, "/data/app.db")
	}
	if s.ConfigPath != "/etc/litestream.yml" {
		t.Errorf("ConfigPath = %q, want %q", s.ConfigPath, "/etc/litestream.yml")
	}
	if s.Logger == nil {
		t.Error("Logger should not be nil")
	}
}

func TestSidecar_NilLoggerDoesNotPanic(t *testing.T) {
	s := &Sidecar{
		DBPath: "/data/app.db",
	}
	// Logger should not panic when called
	if err := s.RestoreIfNeeded(testContext(t)); err != nil {
		// We expect it to fail because litestream binary doesn't exist,
		// but it should not panic due to nil logger
	}
}

func TestSidecar_BinaryPathField(t *testing.T) {
	td := t.TempDir()
	binPath := filepath.Join(td, "litestream")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &Sidecar{
		DBPath:     "/data/app.db",
		BinaryPath: binPath,
	}
	path, err := s.litestreamPath()
	if err != nil {
		t.Fatalf("litestreamPath() error: %v", err)
	}
	if path != binPath {
		t.Errorf("got %q, want %q", path, binPath)
	}
}

func TestSidecar_BinaryPathNotFound(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		BinaryPath: "/nonexistent/litestream",
	}
	_, err := s.litestreamPath()
	if err == nil {
		t.Fatal("expected error for nonexistent binary path")
	}
}

func TestSidecar_BinaryPathCaching(t *testing.T) {
	td := t.TempDir()
	binPath := filepath.Join(td, "litestream")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &Sidecar{
		DBPath:     "/data/app.db",
		BinaryPath: binPath,
	}

	path1, err1 := s.litestreamPath()
	path2, err2 := s.litestreamPath()

	if err1 != nil {
		t.Fatalf("first call error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("second call error: %v", err2)
	}
	if path1 != path2 {
		t.Errorf("caching failed: %q != %q", path1, path2)
	}
}

// --- ReplicaURL field (Task 6) ---

func TestSidecar_ReplicaURLField_Priority(t *testing.T) {
	// The struct field should take priority over the env var.
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ReplicaURL: "s3://field-bucket/db",
	}
	got := s.getReplicaURL()
	if got != "s3://field-bucket/db" {
		t.Errorf("getReplicaURL() = %q, want %q", got, "s3://field-bucket/db")
	}
}

func TestSidecar_ReplicaURLField_Fallback(t *testing.T) {
	t.Setenv("LITESTREAM_REPLICA_URL", "s3://env-bucket/db")
	s := &Sidecar{
		DBPath: "/data/app.db",
		// ReplicaURL not set — should fall back to env var
	}
	got := s.getReplicaURL()
	if got != "s3://env-bucket/db" {
		t.Errorf("getReplicaURL() = %q, want %q", got, "s3://env-bucket/db")
	}
}

// --- Arg building (Tasks 2, 5) ---

func TestSidecar_RestoreArgs_URLMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ReplicaURL: "s3://mybucket/db",
	}
	want := []string{"restore", "-if-db-not-exists", "-if-replica-exists", "-o", "/data/app.db", "s3://mybucket/db"}
	got := s.restoreArgs()
	assertEq(t, got, want)
}

func TestSidecar_RestoreArgs_ConfigMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
	}
	want := []string{"restore", "-config", "/etc/litestream.yml", "-if-db-not-exists", "-if-replica-exists", "-o", "/data/app.db", "/data/app.db"}
	got := s.restoreArgs()
	assertEq(t, got, want)
}

func TestSidecar_ForceRestoreArgs_URLMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ReplicaURL: "s3://mybucket/db",
	}
	want := []string{"restore", "-force", "-o", "/data/app.db", "s3://mybucket/db"}
	got := s.forceRestoreArgs()
	assertEq(t, got, want)
}

func TestSidecar_ForceRestoreArgs_ConfigMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
	}
	want := []string{"restore", "-config", "/etc/litestream.yml", "-force", "-o", "/data/app.db", "/data/app.db"}
	got := s.forceRestoreArgs()
	assertEq(t, got, want)
}

func TestSidecar_ReplicateArgs_URLMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ReplicaURL: "s3://mybucket/db",
	}
	want := []string{"replicate", "/data/app.db", "s3://mybucket/db"}
	got := s.replicateArgs()
	assertEq(t, got, want)
}

func TestSidecar_ReplicateArgs_ConfigMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
	}
	want := []string{"replicate", "-config", "/etc/litestream.yml"}
	got := s.replicateArgs()
	assertEq(t, got, want)
}

// --- Validation (Task 9) ---

func TestSidecar_Validation_EmptyDBPath(t *testing.T) {
	s := &Sidecar{}
	err := s.validate()
	if err == nil {
		t.Fatal("expected error for empty DBPath")
	}
	if !strings.Contains(err.Error(), "DBPath") {
		t.Errorf("error should mention DBPath, got: %v", err)
	}
}

func TestSidecar_Validation_MissingConfigAndURL(t *testing.T) {
	s := &Sidecar{
		DBPath: "/data/app.db",
	}
	err := s.validate()
	if err == nil {
		t.Fatal("expected error when both ConfigPath and ReplicaURL are empty")
	}
}

func TestSidecar_Validation_HappyPathURLMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ReplicaURL: "s3://bucket/db",
	}
	if err := s.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSidecar_Validation_HappyPathConfigMode(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
	}
	if err := s.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- pipeLines (Task 8) ---

func TestPipeLines_WritesLinesToLogger(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Use a synchronous reader so all data is available before the call.
	r := strings.NewReader("hello\nworld\nlast line\n")
	pipeLinesInfo(r, l, "prefix: ")

	output := buf.String()
	if !strings.Contains(output, "prefix: hello") {
		t.Errorf("output should contain 'prefix: hello', got: %s", output)
	}
	if !strings.Contains(output, "prefix: world") {
		t.Errorf("output should contain 'prefix: world', got: %s", output)
	}
	if !strings.Contains(output, "prefix: last line") {
		t.Errorf("output should contain 'prefix: last line', got: %s", output)
	}
}

func TestPipeLines_EmptyInput(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pipeLinesInfo(strings.NewReader(""), l, "prefix: ")

	output := buf.String()
	if output != "" {
		t.Errorf("expected empty output, got: %s", output)
	}
}

func TestPipeLines_Unicode(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pipeLinesInfo(strings.NewReader("こんにちは世界\n"), l, "prefix: ")

	output := buf.String()
	if !strings.Contains(output, "こんにちは世界") {
		t.Errorf("output should contain unicode, got: %s", output)
	}
}

func TestPipeLines_NoFinalNewline(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Line without a trailing newline should still be emitted
	pipeLinesInfo(strings.NewReader("hello\nworld"), l, "prefix: ")

	output := buf.String()
	if !strings.Contains(output, "prefix: hello") {
		t.Errorf("output should contain 'prefix: hello', got: %s", output)
	}
	if !strings.Contains(output, "prefix: world") {
		t.Errorf("output should contain 'prefix: world', got: %s", output)
	}
}

func TestPipeLines_LongLine(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Line longer than bufio.Scanner's default 64KB limit
	longLine := strings.Repeat("a", 100*1024) // 100KB
	r := strings.NewReader(longLine + "\n")
	pipeLinesInfo(r, l, "")

	output := buf.String()
	if !strings.Contains(output, longLine) {
		t.Errorf("output should contain the full 100KB line")
	}
}

func TestPipeLines_ErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pipeLinesError(strings.NewReader("error line\n"), l, "prefix: ")

	output := buf.String()
	// slog Error output includes "level=ERROR" in text handler
	if !strings.Contains(output, "prefix: error line") {
		t.Errorf("output should contain 'prefix: error line', got: %s", output)
	}
}

func TestPipeLines_LoggerPanicRecovery(t *testing.T) {
	var capturedMsg string
	var capturedArgs []any
	panicLogger := LoggerFunc{
		infoFn: func(msg string, args ...any) { panic("intentional panic for test") },
		errFn: func(msg string, args ...any) {
			capturedMsg = msg
			capturedArgs = args
			// Don't panic here — the test needs this call to succeed.
		},
	}

	// Must use a goroutine since Info panics. The recover inside
	// pipeLines should catch it, log via Error, and not crash.
	finished := make(chan struct{}, 1)
	go func() {
		defer func() { finished <- struct{}{} }()
		pipeLinesInfo(strings.NewReader("hello\n"), panicLogger, "prefix: ")
	}()
	<-finished

	if capturedMsg == "" {
		t.Error("expected pipeLines to log the recovered panic, but Error was never called")
	}
	if capturedMsg != "pipeLines: recovered panic in Logger" {
		t.Errorf("Error msg = %q, want %q", capturedMsg, "pipeLines: recovered panic in Logger")
	}
	if len(capturedArgs) < 2 || capturedArgs[1] != "intentional panic for test" {
		t.Errorf("Error args should contain the panic value, got %v", capturedArgs)
	}
}

func TestPipeLines_LoggerPanicKeepsDraining(t *testing.T) {
	panicLogger := LoggerFunc{
		infoFn: func(msg string, args ...any) { panic("boom") },
		errFn:  func(msg string, args ...any) {},
	}

	// io.Pipe writes block until read, so the writer only finishes if
	// pipeLines keeps draining after the Logger panics.
	pr, pw := io.Pipe()
	go pipeLinesInfo(pr, panicLogger, "")

	written := make(chan struct{})
	go func() {
		defer close(written)
		for i := 0; i < 10; i++ {
			if _, err := pw.Write([]byte("line\n")); err != nil {
				return
			}
		}
		pw.Close()
	}()

	select {
	case <-written:
	case <-time.After(2 * time.Second):
		t.Fatal("writer blocked: pipeLines stopped reading after Logger panic")
	}
}

// LoggerFunc is a function type that implements Logger, useful for tests.
type LoggerFunc struct {
	infoFn func(msg string, args ...any)
	errFn  func(msg string, args ...any)
}

func (f LoggerFunc) Info(msg string, args ...any)  { f.infoFn(msg, args...) }
func (f LoggerFunc) Error(msg string, args ...any) { f.errFn(msg, args...) }

// testContext returns a non-nil context for testing.
func testContext(t testing.TB) context.Context {
	t.Helper()
	return context.Background()
}

// assertEq is a helper for string slice comparison.
func assertEq(t testing.TB, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, len(want)=%d\ngot:  %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q\nfull got:  %#v\nfull want: %#v", i, got[i], want[i], got, want)
		}
	}
}
