package http

import (
	"log/slog"
	"net/http"
	"slices"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
)

// SessionMiddleware requires a valid session on every path except publicPaths (design,
// 5.4.1): sessions are required by default, so a new route cannot forget it. It puts
// the authctx.User and the session in the context, and user_id in the logs, the
// access record included.
func SessionMiddleware(sessions *app.Sessions, publicPaths []string, logger *slog.Logger) httpx.Middleware {
	public := slices.Clone(publicPaths)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(public, r.URL.Path) {
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
			ctx = app.WithCurrent(ctx, cur)
			ctx = logx.With(ctx, slog.String("user_id", user.ID()))
			logx.AddAccessAttrs(ctx, slog.String("user_id", user.ID()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
