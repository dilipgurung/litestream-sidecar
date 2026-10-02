# AGENTS.md

Go library (package `litestream`) that runs the `litestream` CLI as a child process: `RestoreIfNeeded` / `Restore` on boot, `Replicate` as a long-running subprocess, `Shutdown` for SIGTERM → SIGKILL. `cmd/example` is a runnable consumer, not part of the library.

## Commands

- `make all` is the pre-PR check (vet, tidy-check, test, race).
- `make test-integration` needs the `litestream` and `sqlite3` CLIs on `PATH`; it runs files tagged `//go:build integration`.
- `make help` lists the rest.

## Conventions

- The package is stdlib only. `go.mod` stays dependency-free.
- OS-specific signal handling lives in `signal_unix.go` / `signal_windows.go` behind build tags. On Windows `sigTerm` is `os.Kill`, so shutdown there is always immediate.
- Unit tests fake litestream with a `#!/bin/sh` script written into `t.TempDir()` and passed as `BinaryPath`. Use the same fake for new unit tests, and save the real binary for integration tests.
- A nil `Logger` becomes `stdioLogger`, which `Replicate` detects to pipe litestream output straight to stdout/stderr. Keep that type check in place when you change logging.
- The package reads only `LITESTREAM_REPLICA_URL`. `LITESTREAM_CONFIG` and `DB_PATH` are conventions of `cmd/example` only.
- The README is user-facing and minimal: install, a short usage snippet, modes, license. API detail goes in godoc. When the public API changes, update godoc, `cmd/example/main.go`, and the README snippet together.
