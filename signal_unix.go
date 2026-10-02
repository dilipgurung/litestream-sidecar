//go:build !windows

package litestream

import "syscall"

// sigTerm is the OS-specific signal used for graceful shutdown.
// On Unix systems this is SIGTERM.
var sigTerm = syscall.SIGTERM
