package httpx

import "net/http"

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
