// Package observability sets up logging (and, later, metrics and tracing).
package observability

import (
	"io"
	"log/slog"
)

// NewLogger returns a JSON slog logger writing to w at the given level.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
