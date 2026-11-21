// Package logx builds the process logger on log/slog and carries log attributes and the
// request ID through context.Context. It is named logx to avoid clashing with "log".
package logx

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"unicode"

	"go.opentelemetry.io/otel/trace"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// RequestIDKey is the attribute key under which the request ID is logged.
const RequestIDKey = "request_id"

// Keys of the trace and span of a record, when its context carries a span: they join
// the log line to its trace.
const (
	TraceIDKey = "trace_id"
	SpanIDKey  = "span_id"
)

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
	accessKey
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

// The access record is written by the outermost middleware with the request's original
// context, so attributes that inner middlewares learn later (user_id, tenant_id) reach
// it through a mutable container that the outer one creates and reads.
type accessAttrs struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

// WithAccessAttrs returns a context carrying an empty container for the attributes of
// the request's access record, and a function that returns what was added to it.
func WithAccessAttrs(ctx context.Context) (context.Context, func() []slog.Attr) {
	c := &accessAttrs{}
	return context.WithValue(ctx, accessKey, c), func() []slog.Attr {
		c.mu.Lock()
		defer c.mu.Unlock()
		return slices.Clone(c.attrs)
	}
}

// AddAccessAttrs adds attrs to the access record of the request, if ctx carries a
// container from WithAccessAttrs; otherwise it does nothing. It does not affect other
// records: use With for those.
func AddAccessAttrs(ctx context.Context, attrs ...slog.Attr) {
	c, ok := ctx.Value(accessKey).(*accessAttrs)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attrs = append(c.attrs, attrs...)
}

func attrsFrom(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(attrsKey).([]slog.Attr)
	return attrs
}

// contextHandler adds the context's request ID, trace, span and attributes to every
// record.
type contextHandler struct {
	inner slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := attrsFrom(ctx)
	id, hasID := RequestIDFrom(ctx)
	sc := trace.SpanContextFromContext(ctx)
	if len(attrs) > 0 || hasID || sc.IsValid() {
		r = r.Clone()
		if hasID {
			r.AddAttrs(slog.String(RequestIDKey, id))
		}
		if sc.IsValid() {
			r.AddAttrs(slog.String(TraceIDKey, sc.TraceID().String()), slog.String(SpanIDKey, sc.SpanID().String()))
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

// IsSensitiveKey reports whether values logged under key are redacted. The key is split
// into words (camelCase, "-" and "_" are boundaries, case is ignored) and it matches when
// any run of consecutive words is a sensitive term: "X-Api-Key" matches "api_key",
// "userPassword" matches "password", while "api_version" matches nothing.
func IsSensitiveKey(key string) bool {
	words := keyWords(key)
	for i := range words {
		for j := i + 1; j <= len(words); j++ {
			if isSensitiveTerm(strings.Join(words[i:j], "_")) {
				return true
			}
		}
	}
	return false
}

func isSensitiveTerm(term string) bool {
	switch term {
	case "password", "passwd", "secret", "token", "authorization", "cookie", "session",
		"apikey", "api_key", "private_key", "database_url", "dsn", "card_number", "cvv",
		"email", "phone", "dni", "nie", "nif", "iban", "address":
		return true
	default:
		return false
	}
}

// keyWords lower-cases key and splits it into words. An upper-case letter starts a word
// after a lower-case letter or digit, or when it ends an acronym ("APIKey" -> api, key).
func keyWords(key string) []string {
	runes := []rune(key)
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			flush()
			continue
		case unicode.IsUpper(r) && i > 0:
			prev := runes[i-1]
			nextIsLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextIsLower) {
				flush()
			}
		}
		cur = append(cur, unicode.ToLower(r))
	}
	flush()
	return words
}

func redact(_ []string, a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, pii.Redacted)
	}
	return a
}
