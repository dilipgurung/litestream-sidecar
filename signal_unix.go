//go:build !windows

package litestream

import "syscall"

var sigTerm = syscall.SIGTERM
