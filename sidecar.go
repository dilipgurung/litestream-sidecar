package litestream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// DefaultShutdownTimeout is the grace period used when Sidecar.ShutdownTimeout
// is not set.
const DefaultShutdownTimeout = 10 * time.Second

// Sidecar manages a litestream child process as a sidecar.
//
// Fields set by the caller:
//   - DBPath: path to the SQLite database file (required).
//   - ConfigPath: path to litestream.yml (used in config-file mode).
//   - ReplicaURL: replica URL (e.g. s3://bucket/db). Takes priority over
//     the LITESTREAM_REPLICA_URL env var when set.
//   - BinaryPath: optional override path to the litestream binary.
//     If empty, exec.LookPath("litestream") is used.
//   - Logger: structured logger. If nil, a no-op logger is used internally.
//   - ShutdownTimeout: how long Replicate's subprocess is given to exit after
//     its context is cancelled (SIGTERM) before it is killed. Defaults to
//     DefaultShutdownTimeout when zero or negative.
type Sidecar struct {
	DBPath          string
	ConfigPath      string
	ReplicaURL      string
	BinaryPath      string
	Logger          Logger
	ShutdownTimeout time.Duration

	// Internal state — binary path resolution cache (success only)
	mu           sync.Mutex
	resolvedPath string // set only on successful lookups
}

// logger returns the Logger, defaulting to stdioLogger if none was set.
func (s *Sidecar) logger() Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return stdioLogger{}
}

// shutdownTimeout returns ShutdownTimeout, defaulting to DefaultShutdownTimeout.
func (s *Sidecar) shutdownTimeout() time.Duration {
	if s.ShutdownTimeout > 0 {
		return s.ShutdownTimeout
	}
	return DefaultShutdownTimeout
}

// validate checks that required fields are set before invoking litestream.
func (s *Sidecar) validate() error {
	if s.DBPath == "" {
		return errors.New("litestream: DBPath is required")
	}
	if s.getReplicaURL() == "" && s.ConfigPath == "" {
		return errors.New("litestream: ConfigPath is required when no replica URL is configured")
	}
	return nil
}

// litestreamPath resolves the path to the litestream binary, using the
// BinaryPath field if set, otherwise falling back to exec.LookPath.
// Successful results are cached so the lookup only happens once.
// On error, subsequent calls retry — enabling recovery from transient
// failures (e.g. PATH not yet set, binary being installed).
func (s *Sidecar) litestreamPath() (string, error) {
	// Fast path: already resolved successfully.
	s.mu.Lock()
	if s.resolvedPath != "" {
		s.mu.Unlock()
		return s.resolvedPath, nil
	}
	s.mu.Unlock()

	// Resolve.
	binPath := s.BinaryPath
	if binPath != "" {
		fi, err := os.Stat(binPath)
		if err != nil {
			return "", fmt.Errorf("litestream binary not found at %q: %w", binPath, err)
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("litestream binary path %q is not a regular file", binPath)
		}
		// Cache on success.
		s.mu.Lock()
		s.resolvedPath = binPath
		s.mu.Unlock()
		return binPath, nil
	}

	path, err := exec.LookPath("litestream")
	if err != nil {
		return "", fmt.Errorf("litestream: binary not found in PATH; install from https://litestream.io/install")
	}
	// Cache on success.
	s.mu.Lock()
	s.resolvedPath = path
	s.mu.Unlock()
	return path, nil
}

// getReplicaURL returns the replica URL, preferring the struct field over
// the LITESTREAM_REPLICA_URL env var.
func (s *Sidecar) getReplicaURL() string {
	if s.ReplicaURL != "" {
		return s.ReplicaURL
	}
	return os.Getenv("LITESTREAM_REPLICA_URL")
}

// restoreArgs returns the command-line arguments for "litestream restore"
// with the -if-db-not-exists and -if-replica-exists flags (idempotent, and
// a no-op on first boot when the replica has no backups yet).
func (s *Sidecar) restoreArgs() []string {
	replicaURL := s.getReplicaURL()
	if replicaURL != "" {
		return []string{"restore", "-if-db-not-exists", "-if-replica-exists", "-o", s.DBPath, replicaURL}
	}
	return []string{"restore", "-config", s.ConfigPath, "-if-db-not-exists", "-if-replica-exists", "-o", s.DBPath, s.DBPath}
}

// forceRestoreArgs returns the command-line arguments for "litestream restore"
// with the -force flag (unconditional overwrite).
func (s *Sidecar) forceRestoreArgs() []string {
	replicaURL := s.getReplicaURL()
	if replicaURL != "" {
		return []string{"restore", "-force", "-o", s.DBPath, replicaURL}
	}
	return []string{"restore", "-config", s.ConfigPath, "-force", "-o", s.DBPath, s.DBPath}
}

// replicateArgs returns the command-line arguments for "litestream replicate".
func (s *Sidecar) replicateArgs() []string {
	replicaURL := s.getReplicaURL()
	if replicaURL != "" {
		return []string{"replicate", s.DBPath, replicaURL}
	}
	return []string{"replicate", "-config", s.ConfigPath}
}

// RestoreIfNeeded runs "litestream restore" to recover the database from
// the replica, but only if the local DB file doesn't already exist. If the
// replica has no backups yet (first boot), it succeeds without restoring.
//
// This method checks only whether the file exists. It does not verify
// database integrity. If the local DB file exists but is corrupted,
// this method will skip restoration. Use Restore() to force overwrite
// a corrupted database unconditionally.
//
// Safe to call on every boot — idempotent when the DB already exists.
func (s *Sidecar) RestoreIfNeeded(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	binPath, err := s.litestreamPath()
	if err != nil {
		return err
	}

	args := s.restoreArgs()
	s.logger().Info("running litestream restore", "binary", binPath, "args", args)

	cmd := exec.CommandContext(ctx, binPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream restore failed: %s: %w", string(output), err)
	}
	s.logger().Info("litestream restore completed", "output", string(output))
	return nil
}

// Restore runs "litestream restore" with the -force flag, unconditionally
// overwriting the local database from the replica. This is a destructive
// operation—use RestoreIfNeeded for idempotent startup recovery.
func (s *Sidecar) Restore(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	binPath, err := s.litestreamPath()
	if err != nil {
		return err
	}

	args := s.forceRestoreArgs()
	s.logger().Info("running litestream restore (force)", "binary", binPath, "args", args)

	cmd := exec.CommandContext(ctx, binPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream force restore failed: %s: %w", string(output), err)
	}
	s.logger().Info("litestream force restore completed", "output", string(output))
	return nil
}

// Replicate starts "litestream replicate" as a long-running subprocess and
// returns the *exec.Cmd so the caller can monitor it via cmd.Wait().
//
// When the provided context is cancelled, the subprocess is sent SIGTERM so
// litestream can flush pending changes. If it has not exited within
// ShutdownTimeout, it is killed. The caller must still call cmd.Wait() to
// observe the exit and release resources; after a clean exit caused by
// cancellation, cmd.Wait() returns ctx.Err(). On Windows there is no graceful
// shutdown: cancellation kills the process immediately.
//
// To stop gracefully, cancel ctx and call cmd.Wait(). Alternatively, use
// Shutdown — but never call Shutdown while another goroutine is blocked in
// cmd.Wait().
func (s *Sidecar) Replicate(ctx context.Context) (*exec.Cmd, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	binPath, err := s.litestreamPath()
	if err != nil {
		return nil, err
	}

	args := s.replicateArgs()
	s.logger().Info("starting litestream replicate", "binary", binPath, "args", args)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(sigTerm) }
	cmd.WaitDelay = s.shutdownTimeout()

	// When no Logger is configured, pipe subprocess output to os.Stdout/os.Stderr.
	l := s.logger()
	if _, isStdio := l.(stdioLogger); isStdio {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("litestream replicate start: %w", err)
		}
	} else {
		// Use our own os.Pipe rather than cmd.StdoutPipe: cmd.Wait closes
		// StdoutPipe readers as soon as the process exits, which races with
		// the reader goroutines and can drop the final lines of output.
		stdoutR, stdoutW, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("stdout pipe: %w", err)
		}
		stderrR, stderrW, err := os.Pipe()
		if err != nil {
			stdoutR.Close()
			stdoutW.Close()
			return nil, fmt.Errorf("stderr pipe: %w", err)
		}
		cmd.Stdout = stdoutW
		cmd.Stderr = stderrW

		startErr := cmd.Start()
		// The child holds its own copies of the write ends; close ours so
		// the readers see EOF when the child exits.
		stdoutW.Close()
		stderrW.Close()
		if startErr != nil {
			stdoutR.Close()
			stderrR.Close()
			return nil, fmt.Errorf("litestream replicate start: %w", startErr)
		}

		go func() {
			defer stdoutR.Close()
			pipeLinesInfo(stdoutR, l, "litestream: ")
		}()
		go func() {
			defer stderrR.Close()
			pipeLinesError(stderrR, l, "litestream err: ")
		}()
	}

	s.logger().Info("litestream replicate started", "pid", cmd.Process.Pid)
	return cmd, nil
}

// pipeLinesInfo reads lines from r and logs them via the Logger at Info level.
// prefix is prepended to each line. Panics in the Logger are recovered
// so a misbehaving logger cannot crash the program.
func pipeLinesInfo(r io.Reader, l Logger, prefix string) {
	pipeLines(r, l, prefix, l.Info)
}

// pipeLinesError reads lines from r and logs them via the Logger at Error level.
// prefix is prepended to each line. Panics in the Logger are recovered
// so a misbehaving logger cannot crash the program.
func pipeLinesError(r io.Reader, l Logger, prefix string) {
	pipeLines(r, l, prefix, l.Error)
}

// pipeLines reads lines from r and logs them via the provided log function.
// prefix is prepended to each line. Uses bufio.Reader to read full lines
// (no 64KB limit like bufio.Scanner). Panics in the Logger are recovered
// so a misbehaving logger cannot crash the program.
func pipeLines(r io.Reader, l Logger, prefix string, logFn func(msg string, args ...any)) {
	defer func() {
		if v := recover(); v != nil {
			// Attempt to log the panic so the operator knows litestream
			// output has stopped. If the Logger panics again, swallow it.
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
			// Strip trailing newline if present (ReadString includes the delimiter)
			if line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			logFn(prefix + line)
		}
		if err != nil {
			if err != io.EOF {
				l.Error("pipeLines: read error", "error", err.Error())
			}
			break
		}
	}
}

// Shutdown gracefully stops the litestream replicate subprocess.
// It sends SIGTERM to the process and waits up to timeout for it to exit.
// If the timeout expires before the process exits, it sends SIGKILL, waits
// for the process to be reaped, and returns an error reporting the timeout.
//
// Passing a zero or negative timeout causes immediate Kill with no SIGTERM.
//
// Shutdown calls cmd.Wait(), so it must not be called while another
// goroutine is blocked in cmd.Wait(). If you monitor the process with
// cmd.Wait(), stop it by cancelling the context passed to Replicate instead.
//
// On Windows, SIGTERM is not supported. sigTerm falls back to os.Kill so
// Shutdown with a positive timeout will still kill immediately. There is no
// graceful shutdown on Windows.
func Shutdown(cmd *exec.Cmd, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("shutdown: process not started")
	}

	// Process already reaped (caller called cmd.Wait()). Nothing to do.
	if cmd.ProcessState != nil {
		return nil
	}

	if timeout <= 0 {
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		// The exit status is just the kill we sent; reap and ignore it.
		_ = cmd.Wait()
		return nil
	}

	// Send SIGTERM (Unix only). On Windows, sigTerm is os.Kill so Signal
	// will kill immediately and the subsequent Wait will return quickly.
	if err := cmd.Process.Signal(sigTerm); err != nil {
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		_ = cmd.Wait()
		return nil
	}

	// Wait for the process to exit within the timeout.
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

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
