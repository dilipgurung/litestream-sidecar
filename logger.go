package litestream

// Logger is the interface used by Sidecar for structured logging.
// It is a subset of *slog.Logger's Info and Error methods, so callers can
// pass a *slog.Logger directly without an adapter.
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

// stdioLogger is used when the caller does not provide a Logger.
// When Replicate detects this type it pipes subprocess output directly
// to os.Stdout / os.Stderr instead of through structured logging.
type stdioLogger struct{}

func (stdioLogger) Info(_ string, _ ...any)  {}
func (stdioLogger) Error(_ string, _ ...any) {}
