package litestream

import (
	"bytes"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSidecar_NilLoggerDoesNotPanic(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
		BinaryPath: writeScript(t, "exit 0\n"),
	}
	if err := s.RestoreIfNeeded(t.Context()); err != nil {
		t.Fatalf("RestoreIfNeeded() error: %v", err)
	}
}

func TestSidecar_BinaryPathField(t *testing.T) {
	binPath := writeScript(t, "exit 0\n")
	s := &Sidecar{DBPath: "/data/app.db", BinaryPath: binPath}

	path, err := s.litestreamPath()
	if err != nil {
		t.Fatalf("litestreamPath() error: %v", err)
	}
	if path != binPath {
		t.Errorf("got %q, want %q", path, binPath)
	}
}

func TestSidecar_BinaryPathNotFound(t *testing.T) {
	s := &Sidecar{DBPath: "/data/app.db", BinaryPath: "/nonexistent/litestream"}
	if _, err := s.litestreamPath(); err == nil {
		t.Fatal("expected error for nonexistent binary path")
	}
}

func TestSidecar_BinaryPathCaching(t *testing.T) {
	s := &Sidecar{DBPath: "/data/app.db", BinaryPath: writeScript(t, "exit 0\n")}

	path1, err := s.litestreamPath()
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	s.BinaryPath = "/nonexistent/litestream"
	path2, err := s.litestreamPath()
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if path1 != path2 {
		t.Errorf("caching failed: %q != %q", path1, path2)
	}
}

func TestSidecar_ReplicaURLField_Priority(t *testing.T) {
	t.Setenv("LITESTREAM_REPLICA_URL", "s3://env-bucket/db")
	s := &Sidecar{DBPath: "/data/app.db", ReplicaURL: "s3://field-bucket/db"}

	if got := s.effectiveReplicaURL(); got != "s3://field-bucket/db" {
		t.Errorf("effectiveReplicaURL() = %q, want %q", got, "s3://field-bucket/db")
	}
}

func TestSidecar_ReplicaURLField_Fallback(t *testing.T) {
	t.Setenv("LITESTREAM_REPLICA_URL", "s3://env-bucket/db")
	s := &Sidecar{DBPath: "/data/app.db"}

	if got := s.effectiveReplicaURL(); got != "s3://env-bucket/db" {
		t.Errorf("effectiveReplicaURL() = %q, want %q", got, "s3://env-bucket/db")
	}
}

func TestSidecar_RestoreArgs_URLMode(t *testing.T) {
	s := &Sidecar{DBPath: "/data/app.db", ReplicaURL: "s3://mybucket/db"}
	want := []string{"restore", "-if-db-not-exists", "-if-replica-exists", "-o", "/data/app.db", "s3://mybucket/db"}
	assertEq(t, s.restoreArgs(), want)
}

func TestSidecar_RestoreArgs_ConfigMode(t *testing.T) {
	t.Setenv("LITESTREAM_REPLICA_URL", "")
	s := &Sidecar{DBPath: "/data/app.db", ConfigPath: "/etc/litestream.yml"}
	want := []string{"restore", "-config", "/etc/litestream.yml", "-if-db-not-exists", "-if-replica-exists", "-o", "/data/app.db", "/data/app.db"}
	assertEq(t, s.restoreArgs(), want)
}

func TestSidecar_ForceRestoreArgs_URLMode(t *testing.T) {
	s := &Sidecar{DBPath: "/data/app.db", ReplicaURL: "s3://mybucket/db"}
	want := []string{"restore", "-force", "-o", "/data/app.db", "s3://mybucket/db"}
	assertEq(t, s.forceRestoreArgs(), want)
}

func TestSidecar_ForceRestoreArgs_ConfigMode(t *testing.T) {
	t.Setenv("LITESTREAM_REPLICA_URL", "")
	s := &Sidecar{DBPath: "/data/app.db", ConfigPath: "/etc/litestream.yml"}
	want := []string{"restore", "-config", "/etc/litestream.yml", "-force", "-o", "/data/app.db", "/data/app.db"}
	assertEq(t, s.forceRestoreArgs(), want)
}

func TestSidecar_ReplicateArgs_URLMode(t *testing.T) {
	s := &Sidecar{DBPath: "/data/app.db", ReplicaURL: "s3://mybucket/db"}
	want := []string{"replicate", "/data/app.db", "s3://mybucket/db"}
	assertEq(t, s.replicateArgs(), want)
}

func TestSidecar_ReplicateArgs_ConfigMode(t *testing.T) {
	t.Setenv("LITESTREAM_REPLICA_URL", "")
	s := &Sidecar{DBPath: "/data/app.db", ConfigPath: "/etc/litestream.yml"}
	want := []string{"replicate", "-config", "/etc/litestream.yml"}
	assertEq(t, s.replicateArgs(), want)
}

func TestSidecar_Validation_EmptyDBPath(t *testing.T) {
	err := (&Sidecar{}).validate()
	if err == nil {
		t.Fatal("expected error for empty DBPath")
	}
	if !strings.Contains(err.Error(), "DBPath") {
		t.Errorf("error should mention DBPath, got: %v", err)
	}
}

func TestSidecar_Validation_MissingConfigAndURL(t *testing.T) {
	t.Setenv("LITESTREAM_REPLICA_URL", "")
	s := &Sidecar{DBPath: "/data/app.db"}
	if err := s.validate(); err == nil {
		t.Fatal("expected error when both ConfigPath and ReplicaURL are empty")
	}
}

func TestSidecar_Validation_HappyPathURLMode(t *testing.T) {
	s := &Sidecar{DBPath: "/data/app.db", ReplicaURL: "s3://bucket/db"}
	if err := s.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSidecar_Validation_HappyPathConfigMode(t *testing.T) {
	s := &Sidecar{DBPath: "/data/app.db", ConfigPath: "/etc/litestream.yml"}
	if err := s.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPipeLines_WritesLinesToLogger(t *testing.T) {
	l, buf := bufferLogger()
	pipeLines(strings.NewReader("hello\nworld\nlast line\n"), l, "prefix: ", l.Info)

	for _, want := range []string{"prefix: hello", "prefix: world", "prefix: last line"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output should contain %q, got: %s", want, buf)
		}
	}
}

func TestPipeLines_EmptyInput(t *testing.T) {
	l, buf := bufferLogger()
	pipeLines(strings.NewReader(""), l, "prefix: ", l.Info)

	if buf.Len() != 0 {
		t.Errorf("expected empty output, got: %s", buf)
	}
}

func TestPipeLines_Unicode(t *testing.T) {
	l, buf := bufferLogger()
	pipeLines(strings.NewReader("こんにちは世界\n"), l, "prefix: ", l.Info)

	if !strings.Contains(buf.String(), "こんにちは世界") {
		t.Errorf("output should contain unicode, got: %s", buf)
	}
}

func TestPipeLines_NoFinalNewline(t *testing.T) {
	l, buf := bufferLogger()
	pipeLines(strings.NewReader("hello\nworld"), l, "prefix: ", l.Info)

	for _, want := range []string{"prefix: hello", "prefix: world"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output should contain %q, got: %s", want, buf)
		}
	}
}

func TestPipeLines_LineOver64KB(t *testing.T) {
	l, buf := bufferLogger()
	longLine := strings.Repeat("a", 100*1024)
	pipeLines(strings.NewReader(longLine+"\n"), l, "", l.Info)

	if !strings.Contains(buf.String(), longLine) {
		t.Errorf("output should contain the full 100KB line")
	}
}

func TestPipeLines_ErrorLevel(t *testing.T) {
	l, buf := bufferLogger()
	pipeLines(strings.NewReader("error line\n"), l, "prefix: ", l.Error)

	if !strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "prefix: error line") {
		t.Errorf("output should contain an ERROR-level 'prefix: error line', got: %s", buf)
	}
}

func TestPipeLines_LoggerPanicRecovery(t *testing.T) {
	var capturedMsg string
	var capturedArgs []any
	l := funcLogger{
		infoFn: func(string, ...any) { panic("intentional panic for test") },
		errFn: func(msg string, args ...any) {
			capturedMsg = msg
			capturedArgs = args
		},
	}

	pipeLines(strings.NewReader("hello\n"), l, "prefix: ", l.Info)

	if capturedMsg != "pipeLines: recovered panic in Logger" {
		t.Errorf("Error msg = %q, want %q", capturedMsg, "pipeLines: recovered panic in Logger")
	}
	if len(capturedArgs) < 2 || capturedArgs[1] != "intentional panic for test" {
		t.Errorf("Error args should contain the panic value, got %v", capturedArgs)
	}
}

func TestPipeLines_LoggerPanicKeepsDraining(t *testing.T) {
	l := funcLogger{
		infoFn: func(string, ...any) { panic("boom") },
		errFn:  func(string, ...any) {},
	}

	// io.Pipe writes block until read, so the writer only finishes if
	// pipeLines keeps draining after the Logger panics.
	pr, pw := io.Pipe()
	go pipeLines(pr, l, "", l.Info)

	written := make(chan struct{})
	go func() {
		defer close(written)
		for range 10 {
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

type funcLogger struct {
	infoFn func(msg string, args ...any)
	errFn  func(msg string, args ...any)
}

func (f funcLogger) Info(msg string, args ...any)  { f.infoFn(msg, args...) }
func (f funcLogger) Error(msg string, args ...any) { f.errFn(msg, args...) }

func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func assertEq(t testing.TB, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("got:  %#v\nwant: %#v", got, want)
	}
}
