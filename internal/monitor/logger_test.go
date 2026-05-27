package monitor_test

import (
	"io"
	"log/slog"
)

// newDiscardLogger returns an slog.Logger that discards all output.
// Used in tests to suppress log noise.
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
