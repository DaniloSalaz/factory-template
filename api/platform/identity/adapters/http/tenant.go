package http

import (
	"log/slog"
	"net/http"

	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/identity/usecases"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/httpx"
	"chaskicode.dev/saas/platform/kit/logx"
	"chaskicode.dev/saas/platform/kit/telemetry"
)

// TenantHeader names the tenant a request acts in (design, section 2).
const TenantHeader = "X-Tenant-ID"

// TenantMiddleware requires X-Tenant-ID, and the right to act in that tenant, on every
// route whose contract security asks for a tenant, and on every route when
// httpx.RouteSecurity did not run (a wiring bug). It runs after SessionMiddleware and
// puts the authctx.Principal in the context and tenant_id in the logs, the access
// record included (design, 5.4.2 to 5.4.4).
func TenantMiddleware(tenancy *usecases.Tenancy, metrics *telemetry.Metrics, logger *slog.Logger) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sec, ok := httpx.SecurityFrom(r.Context()); ok && !sec.Tenant {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			user, ok := authctx.UserFrom(ctx)
			if !ok {
				httpx.WriteProblem(w, r, domain.ErrSessionInvalid(), logger)
				return
			}
			raw := r.Header.Get(TenantHeader)
			entered, err := tenancy.Enter(ctx, user, raw)
			if err != nil {
				if e, ok := apperr.As(err); ok && e.Code == "auth.tenant_forbidden" {
					// No tenant on purpose: the requested id is the prober's (ADR 0004).
					metrics.TenantForbidden(ctx)
				}
				logDenied(r, err, raw, logger)
				httpx.WriteProblem(w, r, err, logger)
				return
			}
			p := entered.Principal
			if entered.UnknownRole {
				logger.ErrorContext(ctx, "the member's role is not in the catalog: acting with no permissions",
					slog.String("tenant_id", p.TenantID()), slog.String("role", p.Role()))
			}
			telemetry.TenantEntered(ctx, p.TenantID())
			ctx = authctx.WithPrincipal(ctx, p)
			ctx = logx.With(ctx, slog.String("tenant_id", p.TenantID()))
			logx.AddAccessAttrs(ctx, slog.String("tenant_id", p.TenantID()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// logDenied logs a refused tenant at Warn. It is not audited (design, 5.4.2): anyone
// with a session could fill the trail with made-up tenant ids. The requested id is
// logged only when it is a UUID, so the header cannot inject anything into the logs.
func logDenied(r *http.Request, err error, raw string, logger *slog.Logger) {
	e, ok := apperr.As(err)
	if !ok || e.Kind != apperr.KindForbidden {
		return // a 5xx, which WriteProblem logs with its cause
	}
	attrs := []slog.Attr{slog.String("code", e.Code), slog.Any("error", err)}
	if id, perr := authctx.ParseID(raw); perr == nil {
		attrs = append(attrs, slog.String("requested_tenant_id", id))
	}
	logger.LogAttrs(r.Context(), slog.LevelWarn, "tenant refused", attrs...)
}
