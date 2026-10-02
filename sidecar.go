package litestream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultShutdownTimeout is the grace period used when Sidecar.ShutdownTimeout
// is not set.
const DefaultShutdownTimeout = 10 * time.Second

// Sidecar manages a litestream child process as a sidecar.
type Sidecar struct {
	// DBPath is the path to the SQLite database file. Required.
	DBPath string

	// ConfigPath is the path to litestream.yml. Required unless a replica
	// URL is configured, in which case it is ignored.
	ConfigPath string

	// ReplicaURL (e.g. s3://bucket/db) selects URL mode. If empty, the
	// LITESTREAM_REPLICA_URL env var is used instead.
	ReplicaURL string

	// BinaryPath overrides the litestream binary. If empty, it is looked
	// up on PATH.
	BinaryPath string

	// Logger receives structured logs and litestream's output. If nil,
	// litestream's output goes straight to os.Stdout and os.Stderr, and
	// nothing else is logged.
	Logger Logger

	// ShutdownTimeout is how long Replicate's subprocess is given to exit
	// after its context is cancelled before it is killed. Defaults to
	// DefaultShutdownTimeout when zero or negative.
	ShutdownTimeout time.Duration

	mu           sync.Mutex
	resolvedPath string
}

func (s *Sidecar) logger() Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return stdioLogger{}
}

func (s *Sidecar) shutdownTimeout() time.Duration {
	if s.ShutdownTimeout > 0 {
		return s.ShutdownTimeout
	}
	return DefaultShutdownTimeout
}

func (s *Sidecar) effectiveReplicaURL() string {
	if s.ReplicaURL != "" {
		return s.ReplicaURL
	}
	return os.Getenv("LITESTREAM_REPLICA_URL")
}

func (s *Sidecar) validate() error {
	if s.DBPath == "" {
		return errors.New("litestream: DBPath is required")
	}
	if s.effectiveReplicaURL() == "" && s.ConfigPath == "" {
		return errors.New("litestream: ConfigPath is required when no replica URL is configured")
	}
	return nil
}

// litestreamPath caches only successful lookups, so a binary installed after
// a failed call is still found on retry.
func (s *Sidecar) litestreamPath() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolvedPath != "" {
		return s.resolvedPath, nil
	}
	path, err := s.findBinary()
	if err != nil {
		return "", err
	}
	s.resolvedPath = path
	return path, nil
}

func (s *Sidecar) findBinary() (string, error) {
	if s.BinaryPath == "" {
		path, err := exec.LookPath("litestream")
		if err != nil {
			return "", fmt.Errorf("litestream: binary not found in PATH; install from https://litestream.io/install: %w", err)
		}
		return path, nil
	}
	fi, err := os.Stat(s.BinaryPath)
	if err != nil {
		return "", fmt.Errorf("litestream: binary not found at %q: %w", s.BinaryPath, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("litestream: binary path %q is not a regular file", s.BinaryPath)
	}
	return s.BinaryPath, nil
}

func (s *Sidecar) restoreArgs() []string {
	return s.restoreArgsWith("-if-db-not-exists", "-if-replica-exists")
}

func (s *Sidecar) forceRestoreArgs() []string {
	return s.restoreArgsWith("-force")
}

func (s *Sidecar) restoreArgsWith(flags ...string) []string {
	args := []string{"restore"}
	source := s.DBPath
	if url := s.effectiveReplicaURL(); url != "" {
		source = url
	} else {
		args = append(args, "-config", s.ConfigPath)
	}
	args = append(args, flags...)
	return append(args, "-o", s.DBPath, source)
}

func (s *Sidecar) replicateArgs() []string {
	if url := s.effectiveReplicaURL(); url != "" {
		return []string{"replicate", s.DBPath, url}
	}
	return []string{"replicate", "-config", s.ConfigPath}
}

// RestoreIfNeeded restores the database from the replica only if the local DB
// file does not exist, so it is safe to call on every boot. If the replica has
// no backups yet (first boot), it succeeds without restoring.
//
// Only the file's existence is checked, not its integrity. Use Restore to
// overwrite a corrupted database.
func (s *Sidecar) RestoreIfNeeded(ctx context.Context) error {
	return s.restore(ctx, s.restoreArgs())
}

// Restore unconditionally overwrites the local database from the replica.
// This is destructive; use RestoreIfNeeded for startup recovery.
func (s *Sidecar) Restore(ctx context.Context) error {
	return s.restore(ctx, s.forceRestoreArgs())
}

func (s *Sidecar) restore(ctx context.Context, args []string) error {
	if err := s.validate(); err != nil {
		return err
	}
	binPath, err := s.litestreamPath()
	if err != nil {
		return err
	}

	l := s.logger()
	l.Info("running litestream restore", "binary", binPath, "args", args)
	cmd := exec.CommandContext(ctx, binPath, args...)

	if _, ok := l.(stdioLogger); ok {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("litestream restore failed: %w", err)
		}
		return nil
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream restore failed: %s: %w", output, err)
	}
	l.Info("litestream restore completed", "output", string(output))
	return nil
}

// Replicate starts "litestream replicate" as a long-running subprocess. The
// caller must call cmd.Wait() to observe its exit and release resources.
//
// Cancelling ctx sends SIGTERM so litestream can flush pending changes, then
// kills it if it is still running after ShutdownTimeout. After a clean exit
// caused by cancellation, cmd.Wait() returns ctx.Err(). On Windows,
// cancellation kills the process immediately.
//
// Shutdown is an alternative to cancelling ctx, but must not be called while
// another goroutine is blocked in cmd.Wait().
func (s *Sidecar) Replicate(ctx context.Context) (*exec.Cmd, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	binPath, err := s.litestreamPath()
	if err != nil {
		return nil, err
	}

	args := s.replicateArgs()
	l := s.logger()
	l.Info("starting litestream replicate", "binary", binPath, "args", args)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(sigTerm) }
	cmd.WaitDelay = s.shutdownTimeout()

	if _, ok := l.(stdioLogger); ok {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Start()
	} else {
		err = startWithLoggedOutput(cmd, l)
	}
	if err != nil {
		return nil, fmt.Errorf("litestream replicate start: %w", err)
	}

	l.Info("litestream replicate started", "pid", cmd.Process.Pid)
	return cmd, nil
}

// startWithLoggedOutput uses its own os.Pipe pair rather than cmd.StdoutPipe:
// cmd.Wait closes StdoutPipe readers as soon as the process exits, which races
// with the reader goroutines and can drop the final lines of output.
func startWithLoggedOutput(cmd *exec.Cmd, l Logger) error {
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	startErr := cmd.Start()
	// The child holds its own copies of the write ends; close ours so the
	// readers see EOF when the child exits.
	stdoutW.Close()
	stderrW.Close()
	if startErr != nil {
		stdoutR.Close()
		stderrR.Close()
		return startErr
	}

	go func() {
		defer stdoutR.Close()
		pipeLines(stdoutR, l, "litestream: ", l.Info)
	}()
	go func() {
		defer stderrR.Close()
		pipeLines(stderrR, l, "litestream err: ", l.Error)
	}()
	return nil
}

// pipeLines logs each line of r via logFn. It uses bufio.Reader rather than
// bufio.Scanner so lines over 64KB are not dropped. A panicking Logger is
// recovered so it cannot crash the program.
func pipeLines(r io.Reader, l Logger, prefix string, logFn func(msg string, args ...any)) {
	defer func() {
		if v := recover(); v != nil {
			// Tell the operator that litestream output is no longer being
			// logged, ignoring a second panic.
			func() {
				defer func() { recover() }()
				l.Error("pipeLines: recovered panic in Logger", "panic", v)
			}()
			// Keep draining so the subprocess never blocks on a full pipe.
			_, _ = io.Copy(io.Discard, r)
		}
	}()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			logFn(prefix + strings.TrimSuffix(line, "\n"))
		}
		if err != nil {
			if err != io.EOF {
				l.Error("pipeLines: read error", "error", err.Error())
			}
			return
		}
	}
}

// Shutdown stops a process started by Replicate. It sends SIGTERM and waits up
// to timeout for the process to exit. If it is still running after timeout, it
// is killed and reaped, and an error reporting the timeout is returned. A zero
// or negative timeout kills immediately. On Windows there is no SIGTERM, so
// Shutdown always kills immediately.
//
// Shutdown calls cmd.Wait(), so it must not be called while another goroutine
// is blocked in cmd.Wait(). In that case, cancel the context passed to
// Replicate instead.
func Shutdown(cmd *exec.Cmd, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("shutdown: process not started")
	}
	if cmd.ProcessState != nil {
		return nil // already reaped
	}

	if timeout <= 0 {
		return killAndReap(cmd)
	}
	if err := cmd.Process.Signal(sigTerm); err != nil {
		return killAndReap(cmd)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		return err
	case <-timer.C:
		// Kill fails only if the process already exited, in which case
		// Wait returns its real status below.
		killErr := cmd.Process.Kill()
		waitErr := <-done
		if killErr != nil {
			return waitErr
		}
		return fmt.Errorf("shutdown: process did not exit within %s and was killed: %w", timeout, waitErr)
	}
}

func killAndReap(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil {
		return err
	}
	// The exit status only reflects the kill we just sent.
	_ = cmd.Wait()
	return nil
}
