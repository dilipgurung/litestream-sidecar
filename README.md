# litestream-sidecar

A Go package that runs [litestream](https://litestream.io) as a child process, giving a SQLite database idempotent restore on boot and continuous replication.

## Installation

```bash
go get github.com/dilipgurung/litestream-sidecar@latest
```

```go
import litestream "github.com/dilipgurung/litestream-sidecar"
```

Requires Go 1.24+ and the [`litestream`](https://litestream.io/install/) binary (tested with 0.5.x) on `PATH`, or set `Sidecar.BinaryPath`.

## Usage

```go
side := &litestream.Sidecar{
	DBPath:     "/data/app.db",
	ConfigPath: "/etc/litestream.yml", // config mode; omit in URL mode
	Logger:     slog.Default(),
}

// Restores from the replica only if the local DB is missing. Safe on every boot.
if err := side.RestoreIfNeeded(ctx); err != nil {
	return err
}

// Cancelling ctx sends SIGTERM, then SIGKILL after ShutdownTimeout (default 10s).
cmd, err := side.Replicate(ctx)
if err != nil {
	return err
}
go func() { _ = cmd.Wait() }() // returns ctx.Err() after a clean cancel
```

See [`cmd/example`](cmd/example/main.go) for a complete program with signal handling and shutdown.

## Modes

- **URL mode**: set `Sidecar.ReplicaURL` or the `LITESTREAM_REPLICA_URL` env var (e.g. `s3://bucket/db`) to replicate a single database without a config file. The field takes priority over the env var.
- **Config mode**: otherwise, `ConfigPath` points at a `litestream.yml` for full multi-database setups.

## API

Full reference on [pkg.go.dev](https://pkg.go.dev/github.com/dilipgurung/litestream-sidecar).

## License

[MIT](LICENSE)
