// Command example demonstrates how to use the litestream sidecar package.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	litestream "github.com/dilipgurung/litestream-sidecar"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPath := getenv("DB_PATH", "/data/app.db")

	// Stays nil when the sidecar is disabled, so the select never picks it.
	var exited chan error

	configPath := os.Getenv("LITESTREAM_CONFIG")

	if os.Getenv("LITESTREAM_REPLICA_URL") != "" || configPath != "" {
		side := &litestream.Sidecar{
			DBPath:          dbPath,
			ConfigPath:      configPath,
			Logger:          logger,
			ShutdownTimeout: 10 * time.Second,
		}

		if err := side.RestoreIfNeeded(ctx); err != nil {
			logger.Error("litestream restore failed", "err", err.Error())
			os.Exit(1)
		}

		// Cancelling ctx stops litestream gracefully.
		cmd, err := side.Replicate(ctx)
		if err != nil {
			logger.Error("litestream replicate start failed", "err", err.Error())
			os.Exit(1)
		}
		exited = make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		logger.Info("litestream sidecar started", "db", dbPath)
	} else {
		logger.Info("litestream sidecar disabled (neither LITESTREAM_REPLICA_URL nor LITESTREAM_CONFIG is set)")
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
