package litestream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReplicate_ReturnsCmd(t *testing.T) {
	td := t.TempDir()
	binPath := filepath.Join(td, "litestream")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nwhile true; do sleep 1; done"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
		BinaryPath: binPath,
		Logger:     nil,
	}

	cmd, err := s.Replicate(testContext(t))
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}

	if cmd == nil {
		t.Fatal("Replicate() returned nil cmd")
	}

	if cmd.Process == nil {
		t.Fatal("cmd.Process is nil (process not started)")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
}

func TestReplicate_BinaryNotFound(t *testing.T) {
	s := &Sidecar{
		DBPath:     "/data/app.db",
		BinaryPath: "/nonexistent/litestream",
	}

	_, err := s.Replicate(testContext(t))
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

func TestShutdown_SendsTermThenKill(t *testing.T) {
	binPath := writeScript(t, "trap '' TERM\ntouch \"$0.ready\"\nwhile true; do sleep 1; done\n")
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
		BinaryPath: binPath,
	}

	cmd, err := s.Replicate(testContext(t))
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}
	waitReady(t, binPath)

	// SIGTERM is ignored, so Shutdown must fall back to SIGKILL after
	// the timeout, reap the process, and report the timeout.
	if err := Shutdown(cmd, 100*time.Millisecond); err == nil {
		t.Error("expected error when process had to be killed")
	}
	if cmd.ProcessState == nil {
		t.Fatal("Shutdown returned before the process was reaped")
	}
	if cmd.ProcessState.Success() {
		t.Error("expected killed process to report failure")
	}
}

func TestShutdown_GracefulExit(t *testing.T) {
	binPath := writeScript(t, "trap 'exit 0' TERM\ntouch \"$0.ready\"\nwhile true; do sleep 0.05; done\n")
	s := &Sidecar{DBPath: "/data/app.db", ConfigPath: "/etc/litestream.yml", BinaryPath: binPath}

	cmd, err := s.Replicate(testContext(t))
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}
	waitReady(t, binPath)

	if err := Shutdown(cmd, 5*time.Second); err != nil {
		t.Errorf("Shutdown() = %v, want nil for a clean SIGTERM exit", err)
	}
}

func TestShutdown_ZeroTimeoutReaps(t *testing.T) {
	binPath := writeScript(t, "while true; do sleep 1; done\n")
	s := &Sidecar{DBPath: "/data/app.db", ConfigPath: "/etc/litestream.yml", BinaryPath: binPath}

	cmd, err := s.Replicate(testContext(t))
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}
	if err := Shutdown(cmd, 0); err != nil {
		t.Errorf("Shutdown(0) = %v, want nil", err)
	}
	if cmd.ProcessState == nil {
		t.Error("Shutdown(0) returned before the process was reaped")
	}
}

func TestReplicate_ContextCancelSendsSIGTERM(t *testing.T) {
	binPath := writeScript(t, "trap 'exit 0' TERM\ntouch \"$0.ready\"\nwhile true; do sleep 0.05; done\n")
	s := &Sidecar{DBPath: "/data/app.db", ConfigPath: "/etc/litestream.yml", BinaryPath: binPath}

	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := s.Replicate(ctx)
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}
	waitReady(t, binPath)

	cancel()
	// A clean exit proves SIGTERM (not SIGKILL) was delivered. Wait still
	// reports the context error because the context caused the exit.
	err = cmd.Wait()
	if !cmd.ProcessState.Success() {
		t.Errorf("process did not exit cleanly on SIGTERM: %v", err)
	}
}

func TestReplicate_ContextCancelKillsAfterTimeout(t *testing.T) {
	binPath := writeScript(t, "trap '' TERM\ntouch \"$0.ready\"\nwhile true; do sleep 0.05; done\n")
	s := &Sidecar{
		DBPath:          "/data/app.db",
		ConfigPath:      "/etc/litestream.yml",
		BinaryPath:      binPath,
		ShutdownTimeout: 200 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := s.Replicate(ctx)
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}
	waitReady(t, binPath)

	cancel()
	start := time.Now()
	_ = cmd.Wait()
	if cmd.ProcessState.Success() {
		t.Error("expected process ignoring SIGTERM to be killed")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Wait took %s; ShutdownTimeout was not applied", elapsed)
	}
}

func TestReplicate_LogsAllOutputWithLogger(t *testing.T) {
	binPath := writeScript(t, "echo out-line\necho err-line >&2\nprintf no-newline\n")
	var mu sync.Mutex
	var lines []string
	record := func(msg string, _ ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, msg)
	}
	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
		BinaryPath: binPath,
		Logger:     LoggerFunc{infoFn: record, errFn: record},
	}

	cmd, err := s.Replicate(testContext(t))
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() error: %v", err)
	}

	want := []string{"litestream: out-line", "litestream err: err-line", "litestream: no-newline"}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := strings.Join(lines, "\n")
		mu.Unlock()
		missing := ""
		for _, w := range want {
			if !strings.Contains(got, w) {
				missing = w
				break
			}
		}
		if missing == "" {
			if strings.Contains(got, "read error") {
				t.Errorf("unexpected pipe read error logged:\n%s", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing %q in logged output:\n%s", missing, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// writeScript writes an executable shell script named "litestream" and
// returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "litestream")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
	return binPath
}

func TestShutdown_AlreadyExitedProcess(t *testing.T) {
	td := t.TempDir()
	binPath := filepath.Join(td, "litestream")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &Sidecar{
		DBPath:     "/data/app.db",
		ConfigPath: "/etc/litestream.yml",
		BinaryPath: binPath,
	}

	cmd, err := s.Replicate(testContext(t))
	if err != nil {
		t.Fatalf("Replicate() error: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Logf("cmd.Wait() (expected): %v", err)
	}

	// Process is already reaped; Shutdown should detect ProcessState
	// and return nil without sending any signals.
	if err := Shutdown(cmd, time.Second); err != nil {
		t.Errorf("Shutdown on already-exited process: got error %v, want nil", err)
	}
}

// waitReady blocks until a script written by writeScript has touched
// "$0.ready", signalling that its signal trap is installed.
func waitReady(t *testing.T, binPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(binPath + ".ready"); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("script did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
