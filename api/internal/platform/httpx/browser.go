package httpx

import (
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/clientip"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/ratelimit"
)

// ClientIP stores the request's client address (see clientip.Resolver) in its context,
// where rate limiting and the audit trail read it.
func ClientIP(resolver clientip.Resolver) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if addr, ok := resolver.Addr(r); ok {
				r = r.WithContext(clientip.WithAddr(r.Context(), addr))
			}
			next.ServeHTTP(w, r)
		})
	}
}

const corsMaxAgeSeconds = 600

// The methods and request headers a cross-origin caller may use.
const (
	corsMethods = "GET, POST, PUT, PATCH, DELETE"
	corsHeaders = "Content-Type, X-Request-ID, X-Tenant-ID"
)

// CORS lets exactly the allowed origins call the API with credentials (design, section
// 6). There are no wildcards: the response names the caller's origin only when it is in
// the list. Preflights are answered here and never reach the routes or the session.
func CORS(allowedOrigins []string) Middleware {
	allowed := slices.Clone(allowedOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			ok := origin != "" && slices.Contains(allowed, origin)
			if ok {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if ok {
					h.Set("Access-Control-Allow-Methods", corsMethods)
					h.Set("Access-Control-Allow-Headers", corsHeaders)
					h.Set("Access-Control-Max-Age", strconv.Itoa(corsMaxAgeSeconds))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRF refuses, with 403, any request with a method that can modify data unless its
// Origin header is one of the allowed origins (design, section 6). A missing Origin is
// refused as well: browsers always send it on those methods.
func CSRF(allowedOrigins []string, logger *slog.Logger) Middleware {
	allowed := slices.Clone(allowedOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			default:
				if origin := r.Header.Get("Origin"); origin == "" || !slices.Contains(allowed, origin) {
					WriteProblem(w, r, apperr.Forbidden("csrf.origin_rejected",
						"The request origin is not allowed."), logger)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit answers 429 once the client address exceeds the limiter's quota. Requests
// without a known address share one quota, so they cannot evade it.
func RateLimit(limiter ratelimit.Limiter, logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "unknown"
			if addr, ok := clientip.AddrFrom(r.Context()); ok {
				key = addr.String()
			}
			ok, err := limiter.Allow(r.Context(), key)
			if err != nil {
				WriteProblem(w, r, apperr.Wrap(err, apperr.KindUnavailable, "rate_limit.unavailable",
					"The service is temporarily unavailable."), logger)
				return
			}
			if !ok {
				WriteProblem(w, r, apperr.RateLimited("rate_limit.exceeded",
					"Too many requests; try again later."), logger)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
