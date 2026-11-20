package httpx

import "net/http"

// responseWriter records the status code and body size written through it.
type responseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

// wrapResponseWriter reuses w when it is already a *responseWriter, so stacked
// middlewares share one record of what was sent.
func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	if rw, ok := w.(*responseWriter); ok {
		return rw
	}
	return &responseWriter{ResponseWriter: w}
}

// WriteHeader forwards the first final status and ignores later ones, which net/http
// would otherwise log as "superfluous WriteHeader". 1xx informational codes pass through.
func (w *responseWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err //nolint:wrapcheck // a ResponseWriter decorator must stay transparent.
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush, deadlines...).
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Status is the status sent so far, or 200 if the handler wrote nothing (net/http's default).
func (w *responseWriter) Status() int {
	if !w.wroteHeader {
		return http.StatusOK
	}
	return w.status
}

// headerWritten reports whether the status line has been sent.
func (w *responseWriter) headerWritten() bool { return w.wroteHeader }
