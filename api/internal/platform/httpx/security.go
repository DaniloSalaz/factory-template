package httpx

import (
	"context"
	"log/slog"
	"maps"
	"net/http"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
)

// Security is what a route requires before its handler runs: a session, and a tenant
// (X-Tenant-ID) the session's user may act in. It comes from the security declared for
// the operation in the OpenAPI contract (gen/routes).
type Security struct {
	Session bool
	Tenant  bool
}

// RouteSecurity resolves the route of each request on mux and stores its Security in
// the context, where the session and tenant middlewares read it (SecurityFrom). table
// maps ServeMux patterns ("GET /me") to their security. It also adds the route pattern
// to the access record: inner middlewares pass a copy of the request on, so AccessLog
// never sees the Pattern that ServeMux sets.
func RouteSecurity(mux *http.ServeMux, table map[string]Security) Middleware {
	routes := maps.Clone(table)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Fail closed. A request that matches no route still needs a session, so it
			// cannot probe which routes exist, but no tenant, so an authenticated caller
			// gets the 404. A pattern registered on the mux but missing from the contract
			// was written by hand, so it needs everything.
			sec := Security{Session: true}
			if _, pattern := mux.Handler(r); pattern != "" {
				logx.AddAccessAttrs(r.Context(), slog.String("pattern", pattern))
				s, ok := routes[pattern]
				if !ok {
					s = Security{Session: true, Tenant: true}
				}
				sec = s
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), securityKey{}, sec)))
		})
	}
}

type securityKey struct{}

// SecurityFrom returns the security RouteSecurity resolved for the request. Without it
// (a wiring bug) callers must require everything.
func SecurityFrom(ctx context.Context) (Security, bool) {
	s, ok := ctx.Value(securityKey{}).(Security)
	return s, ok
}
