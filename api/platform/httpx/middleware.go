package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"time"

	"chaskicode.dev/saas/platform/logx"
)

// Middleware wraps a handler with cross-cutting behaviour.
type Middleware = func(http.Handler) http.Handler

// Chain wraps h with mws. The first middleware is the outermost: it sees the request
// first and the response last.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RequestIDHeader carries the request ID in requests and responses.
const RequestIDHeader = "X-Request-ID"

const maxRequestIDLen = 64

// RequestID reuses a well-formed incoming X-Request-ID or generates one, stores it with
// logx.WithRequestID and echoes it in the response.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID(id) {
				id = newRequestID()
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(logx.WithRequestID(r.Context(), id)))
		})
	}
}

// validRequestID accepts only short [A-Za-z0-9_-] values, so a client cannot inject
// log-breaking or oversized content through the header.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range []byte(id) {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func newRequestID() string {
	b := make([]byte, 16)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AccessLog writes one Info record per request, 5xx included: failures with their cause
// are logged once by WriteProblem or Recover, so access records never double-count them.
// Inner middlewares add attributes to the record with logx.AddAccessAttrs.
// Successful requests to quietPaths (typically probes) are not logged. Only r.URL.Path is
// logged, never the query string, which often carries tokens or personal data.
func AccessLog(logger *slog.Logger, quietPaths ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := wrapResponseWriter(w)
			ctx, added := logx.WithAccessAttrs(r.Context())
			req := r.WithContext(ctx)
			next.ServeHTTP(rw, req)

			status := rw.Status()
			if status < http.StatusMultipleChoices && slices.Contains(quietPaths, r.URL.Path) {
				return
			}
			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			}
			// ServeMux sets Pattern on this same *Request while routing it, unless an inner
			// middleware passed a copy on; then RouteSecurity adds it to the access attrs.
			extra := added()
			if req.Pattern != "" && !slices.ContainsFunc(extra, func(a slog.Attr) bool { return a.Key == "pattern" }) {
				attrs = append(attrs, slog.String("pattern", req.Pattern))
			}
			attrs = append(attrs,
				slog.Int("status", status),
				slog.Int64("bytes", rw.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			)
			// What inner middlewares learnt about the request: user_id, tenant_id.
			attrs = append(attrs, extra...)
			logger.LogAttrs(ctx, slog.LevelInfo, "http request", attrs...)
		})
	}
}

// Recover turns a panic into a logged error with its stack and a 500 problem response.
// If the response has already started it can only log. http.ErrAbortHandler is
// re-panicked because net/http uses it to abort a response on purpose.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := wrapResponseWriter(w)
			defer recoverPanic(rw, r, logger)
			next.ServeHTTP(rw, r)
		})
	}
}

// recoverPanic must be deferred directly so that recover sees the panic.
func recoverPanic(rw *responseWriter, r *http.Request, logger *slog.Logger) {
	v := recover()
	if v == nil {
		return
	}
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v)
	}
	perr := &panicError{value: v, stack: debug.Stack()}
	if rw.headerWritten() {
		if rep, ok := r.Context().Value(reporterKey{}).(ErrorReporter); ok {
			rep.ReportError(r.Context(), perr)
		}
		logger.ErrorContext(r.Context(), "panic after response started",
			slog.String("path", r.URL.Path), slog.Any("error", perr))
		return
	}
	WriteProblem(rw, r, perr, logger)
}

// panicError carries a recovered panic. As a slog.LogValuer it logs the stack alongside
// the message, so WriteProblem's single error record includes it.
type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string { return fmt.Sprintf("panic: %v", e.value) }

func (e *panicError) LogValue() slog.Value {
	return slog.GroupValue(slog.String("message", e.Error()), slog.String("stack", string(e.stack)))
}

// SecurityHeaders sets conservative defaults for an API; handlers may override them.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps the request body at limit bytes. Reading past it fails with
// *http.MaxBytesError, which WriteProblem answers with 413.
func BodyLimit(limit int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				// The root writer lets net/http close the connection once the limit is hit.
				r.Body = http.MaxBytesReader(rootResponseWriter(w), r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rootResponseWriter follows Unwrap to the writer net/http created.
func rootResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}
