// Package logx builds the process logger on log/slog and carries log attributes and the
// request ID through context.Context. It is named logx to avoid clashing with "log".
package logx

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// RequestIDKey is the attribute key under which the request ID is logged.
const RequestIDKey = "request_id"

// New returns a logger writing to w in the configured format and level. Records pick up
// attributes stored in their context with With and WithRequestID, and values under
// sensitive keys (see IsSensitiveKey) are redacted.
func New(w io.Writer, cfg config.Log) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level, ReplaceAttr: redact}

	var inner slog.Handler
	if cfg.Format == config.LogFormatText {
		inner = slog.NewTextHandler(w, opts)
	} else {
		inner = slog.NewJSONHandler(w, opts)
	}
	return slog.New(&contextHandler{inner: inner})
}

type ctxKey int

const (
	attrsKey ctxKey = iota
	requestIDKey
)

// With returns a context whose log records will include attrs, after any added earlier.
// Only records logged with the *Context methods (InfoContext, ...) see them.
func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	return context.WithValue(ctx, attrsKey, slices.Concat(attrsFrom(ctx), attrs))
}

// WithRequestID returns a context carrying the request ID. It is the only way to set it.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request ID stored in ctx, if any. It is the only way to read it.
func RequestIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok && id != ""
}

func attrsFrom(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(attrsKey).([]slog.Attr)
	return attrs
}

// contextHandler adds the context's request ID and attributes to every record.
type contextHandler struct {
	inner slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := attrsFrom(ctx)
	id, hasID := RequestIDFrom(ctx)
	if len(attrs) > 0 || hasID {
		r = r.Clone()
		if hasID {
			r.AddAttrs(slog.String(RequestIDKey, id))
		}
		r.AddAttrs(attrs...)
	}
	return h.inner.Handle(ctx, r) //nolint:wrapcheck // a handler decorator must stay transparent.
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}

// IsSensitiveKey reports whether values logged under key are redacted. Matching is exact
// after lower-casing and treating "-" as "_", so it is predictable rather than clever.
func IsSensitiveKey(key string) bool {
	switch strings.ReplaceAll(strings.ToLower(key), "-", "_") {
	case "password", "passwd", "secret", "client_secret",
		"token", "access_token", "refresh_token", "id_token", "api_key", "apikey",
		"authorization", "cookie", "set_cookie", "session", "private_key",
		"database_url", "dsn", "card_number", "cvv":
		return true
	default:
		return false
	}
}

func redact(_ []string, a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, pii.Redacted)
	}
	return a
}
