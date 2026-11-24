package http

import (
	"log/slog"
	"net/http"

	"chaskicode.dev/saas/core/identity/usecases"
	"chaskicode.dev/saas/core/kit/apperr"
	"chaskicode.dev/saas/core/kit/authctx"
	"chaskicode.dev/saas/core/kit/httpx"
	"chaskicode.dev/saas/core/kit/logx"
)

// SessionMiddleware requires a valid session (auth-y-tenancy §5.4.1) unless the route's security,
// resolved by httpx.RouteSecurity from the contract, says it is public. Without that
// security (a wiring bug) it requires one: a new route cannot forget it. It puts the
// authctx.User and the session in the context, and user_id in the logs, the access
// record included.
func SessionMiddleware(sessions *usecases.Sessions, logger *slog.Logger) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sec, ok := httpx.SecurityFrom(r.Context()); ok && !sec.Session {
				next.ServeHTTP(w, r)
				return
			}
			cur, err := sessions.Authenticate(r.Context(), cookieValue(r, SessionCookie))
			if err != nil {
				if apperr.KindOf(err) == apperr.KindUnauthorized {
					http.SetCookie(w, clearSessionCookie())
				}
				httpx.WriteProblem(w, r, err, logger)
				return
			}
			user, err := authctx.NewUser(cur.UserID, cur.MFAAuthTime)
			if err != nil {
				httpx.WriteProblem(w, r, err, logger)
				return
			}
			ctx := authctx.WithUser(r.Context(), user)
			ctx = usecases.WithCurrent(ctx, cur)
			ctx = logx.With(ctx, slog.String("user_id", user.ID()))
			logx.AddAccessAttrs(ctx, slog.String("user_id", user.ID()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
