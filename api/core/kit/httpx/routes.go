package httpx

import (
	"log/slog"
	"net/http"

	"chaskicode.dev/saas/core/kit/apperr"
)

// WithRouteFallback serves mux but answers unmatched routes with problem+json: 404
// route_not_found, or 405 method_not_allowed keeping the Allow header.
//
// mux.Handler reports an empty pattern exactly when ServeMux would answer 404 or 405
// itself, so its own decision is reused instead of being re-implemented. A catch-all "/"
// route would not work: it also matches other methods and turns every 405 into a 404.
func WithRouteFallback(mux *http.ServeMux, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}

		// Run ServeMux's own 404/405 handler against a throwaway writer to learn which one
		// it is and, for 405, the methods it would allow.
		probe := &probeWriter{header: http.Header{}}
		h.ServeHTTP(probe, r)
		if probe.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", probe.header.Get("Allow"))
			WriteProblem(w, r, &methodNotAllowedError{method: r.Method}, logger)
			return
		}
		WriteProblem(w, r, apperr.NotFound("route_not_found", "No route matches the request path."), logger)
	})
}

// methodNotAllowedError is a transport-level error: the resource exists but not for this
// method. It is not an apperr Kind because only HTTP routing can produce it.
type methodNotAllowedError struct {
	method string
}

func (e *methodNotAllowedError) Error() string { return "method " + e.method + " not allowed" }

// probeWriter records the status and headers a handler writes and discards the body.
type probeWriter struct {
	header http.Header
	status int
}

func (p *probeWriter) Header() http.Header         { return p.header }
func (p *probeWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *probeWriter) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}
