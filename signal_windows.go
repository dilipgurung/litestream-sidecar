//go:build windows

package litestream

import "os"

// Windows has no SIGTERM, so "graceful" shutdown kills immediately.
var sigTerm = os.Kill
