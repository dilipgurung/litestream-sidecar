package litestream

import "log/slog"

var _ Logger = (*slog.Logger)(nil)
