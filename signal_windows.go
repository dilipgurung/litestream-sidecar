//go:build windows

package litestream

import "os"

// sigTerm is the OS-specific signal used for graceful shutdown.
// On Windows, SIGTERM is not supported, so sigTerm is set to os.Kill
// (which always works). Code using sigTerm should handle the error
// from Process.Signal and fall back to Kill when the signal fails.
var sigTerm = os.Kill
