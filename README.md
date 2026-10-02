# litestream-sidecar

A Go package that manages a [litestream](https://litestream.io) child process as a sidecar, providing idempotent database restore and continuous replication for SQLite databases.

## Features

- **RestoreIfNeeded** — runs `litestream restore` to recover the database from a replica, but only if the local DB file doesn't already exist. Safe to run on every boot, including the very first one when the replica is still empty.
- **Replicate** — starts `litestream replicate` as a long-running subprocess and streams its output to a structured logger.
- **Graceful Shutdown** — cancelling the `Replicate` context sends SIGTERM first, then falls back to SIGKILL after `ShutdownTimeout`.
- **Two Modes**:
  - **URL mode**: driven by the `LITESTREAM_REPLICA_URL` environment variable — replicate a single database directly without a config file.
  - **Config mode**: uses a `litestream.yml` config file for full multi-database setups.
- **Zero external dependencies** — stdlib only (package itself; integration tests use `sqlite3` CLI).

## Project Structure

```
.
├── cmd/example/       # Usage example (main package)
├── litestream.go       # Package documentation
├── sidecar.go          # Sidecar management
├── logger.go           # Logger interface & default
├── signal_unix.go      # Unix signal handling
├── signal_windows.go   # Windows signal handling
├── go.mod
├── go.sum
└── README.md
```

## Installation

```bash
go get github.com/dilipk/litestream-sidecar
```

**Prerequisites**: The `litestream` binary must be installed on the system PATH, or set `BinaryPath` on the `Sidecar` struct.

## Quick Start

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	litestream "github.com/dilipk/litestream-sidecar"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPath := getenv("DB_PATH", "/data/app.db")

	// exited receives litestream's exit status. It stays nil (blocks
	// forever in select) when the sidecar is disabled.
	var exited chan error

	if os.Getenv("LITESTREAM_REPLICA_URL") != "" {
		side := &litestream.Sidecar{
			DBPath:          dbPath,
			ConfigPath:      getenv("LITESTREAM_CONFIG", "/etc/litestream.yml"),
			Logger:          logger,
			ShutdownTimeout: 10 * time.Second,
		}

		if err := side.RestoreIfNeeded(ctx); err != nil {
			logger.Error("litestream restore failed", "err", err.Error())
			os.Exit(1)
		}

		// Cancelling ctx sends SIGTERM to litestream so it can flush
		// pending changes; it is killed if still running after
		// ShutdownTimeout.
		cmd, err := side.Replicate(ctx)
		if err != nil {
			logger.Error("litestream replicate start failed", "err", err.Error())
			os.Exit(1)
		}
		exited = make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		logger.Info("litestream sidecar started", "db", dbPath)
	} else {
		logger.Info("litestream sidecar disabled (LITESTREAM_REPLICA_URL not set)")
	}

	// Application runs here...
	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-exited:
		logger.Error("litestream exited unexpectedly", "err", err)
		os.Exit(1)
	}

	if exited != nil {
		// Wait reports ctx.Err() when litestream exits cleanly after
		// cancellation; anything else is a real failure.
		if err := <-exited; err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("litestream shutdown failed", "err", err.Error())
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

## API

### Type `Sidecar`

| Field | Type | Description |
|-------|------|-------------|
| `DBPath` | `string` | Path to the SQLite database file (required). |
| `ConfigPath` | `string` | Path to `litestream.yml` (used in config mode). |
| `ReplicaURL` | `string` | Replica URL (e.g. `s3://bucket/db`). Takes priority over `LITESTREAM_REPLICA_URL` env var. |
| `BinaryPath` | `string` | Optional override path to the litestream binary. Falls back to `exec.LookPath("litestream")`. |
| `Logger` | `Logger` | Structured logger. If nil, a no-op logger is used internally and litestream output goes straight to stdout/stderr. |
| `ShutdownTimeout` | `time.Duration` | Grace period after the `Replicate` context is cancelled (SIGTERM) before the process is killed. Defaults to `DefaultShutdownTimeout` (10s). |

### Type `Logger`

```go
type Logger interface {
    Info(msg string, args ...any)
    Error(msg string, args ...any)
}
```

Compatible with `*slog.Logger` — pass `slog.Default()` directly.

### Methods

#### `func (s *Sidecar) RestoreIfNeeded(ctx context.Context) error`

Runs `litestream restore` with the `-if-db-not-exists` and `-if-replica-exists` flags. Idempotent — if the DB file already exists, or the replica has no backups yet, litestream exits 0 without restoring.

#### `func (s *Sidecar) Restore(ctx context.Context) error`

Runs `litestream restore` with the `-force` flag, unconditionally overwriting the local database from the replica. **Destructive** — use `RestoreIfNeeded` for idempotent startup recovery.

#### `func (s *Sidecar) Replicate(ctx context.Context) (*exec.Cmd, error)`

Starts `litestream replicate` as a long-running subprocess. Returns the `*exec.Cmd` so the caller can monitor it via `cmd.Wait()`. Cancelling the context sends SIGTERM, then SIGKILL if the process is still running after `ShutdownTimeout`. When litestream exits cleanly because of cancellation, `cmd.Wait()` returns `ctx.Err()`. On Windows, cancellation kills immediately.

#### `func Shutdown(cmd *exec.Cmd, timeout time.Duration) error`

Alternative to context cancellation. Gracefully stops the subprocess: SIGTERM → wait → SIGKILL on timeout, and returns once the process has been reaped (with an error if it had to be killed). Pass 0 to skip SIGTERM and kill immediately. Shutdown calls `cmd.Wait()` itself, so do not use it while another goroutine is blocked in `cmd.Wait()`.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `LITESTREAM_REPLICA_URL` | Replica URL (e.g., `s3://bucket/db`). When set, URL mode is used. |
| `LITESTREAM_CONFIG` | Path to litestream config file (default: `/etc/litestream.yml`). |

## Testing

```bash
# Unit tests (no external deps needed)
go test ./...

# Integration tests (requires litestream + sqlite3 CLIs in PATH)
go test -tags=integration -v -count=1 ./...
```

## License

MIT
