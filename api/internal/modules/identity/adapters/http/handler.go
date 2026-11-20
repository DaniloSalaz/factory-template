// Package http exposes the identity module over HTTP: the generated /auth/* server and
// the session middleware.
package http

import (
	"context"
	"log/slog"
	"net/http"

	identityapi "github.com/DaniloSalaz/factory-template/api/gen/openapi/identity"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/clientip"
)

var _ identityapi.StrictServerInterface = (*Handler)(nil)

// Handler implements the generated strict server interface.
type Handler struct {
	login    *app.Login
	sessions *app.Sessions
	logger   *slog.Logger
}

// NewHandler builds a Handler.
func NewHandler(login *app.Login, sessions *app.Sessions, logger *slog.Logger) *Handler {
	return &Handler{login: login, sessions: sessions, logger: logger}
}

// AuthLogin starts a login (design, 5.1.1 and 5.1.2).
func (h *Handler) AuthLogin(ctx context.Context, req identityapi.AuthLoginRequestObject) (identityapi.AuthLoginResponseObject, error) {
	r := requestFrom(ctx)
	started, err := h.login.Start(deref(req.Params.ReturnTo), storedIntents(r))
	if err != nil {
		return nil, err
	}
	cookies := []*http.Cookie{intentCookie(started.State, started.Value)}
	for _, state := range started.Discard {
		cookies = append(cookies, clearIntentCookie(state))
	}
	return redirect{location: started.RedirectURL, cookies: cookies}, nil
}

// AuthCallback completes the login named by state (design, 5.1.3 to 5.1.10).
func (h *Handler) AuthCallback(ctx context.Context, req identityapi.AuthCallbackRequestObject) (identityapi.AuthCallbackResponseObject, error) {
	r := requestFrom(ctx)
	state := deref(req.Params.State)
	var cookies []*http.Cookie
	cb := app.Callback{
		State: state, Code: deref(req.Params.Code), ProviderError: deref(req.Params.Error),
		PreviousToken: cookieValue(r, SessionCookie), UserAgent: r.UserAgent(),
	}
	// A malformed state names no cookie; a well-formed one is deleted whatever happens.
	if domain.WellFormed(state) {
		cb.IntentValue = cookieValue(r, IntentPrefix+state)
		cookies = append(cookies, clearIntentCookie(state))
	}
	if p, ok := clientip.PrefixFrom(ctx); ok {
		cb.IPPrefix = p
	}

	done, err := h.login.Complete(ctx, cb)
	if err != nil {
		// The problem response answers the error and still clears the intent cookie.
		return problem{err: err, cookies: cookies, r: r, logger: h.logger}, nil //nolint:nilerr // answered by the response
	}
	cookies = append(cookies, sessionCookie(done.Token, done.ExpiresAt))
	return redirect{location: done.ReturnTo, cookies: cookies}, nil
}

// AuthLogout ends the current session (design, 5.5).
func (h *Handler) AuthLogout(ctx context.Context, _ identityapi.AuthLogoutRequestObject) (identityapi.AuthLogoutResponseObject, error) {
	cur, ok := app.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	logoutURL, err := h.sessions.Logout(ctx, cur)
	if err != nil {
		return nil, err
	}
	return loggedOut{logoutURL: logoutURL}, nil
}

// AuthLogoutAll ends every session of the current user.
func (h *Handler) AuthLogoutAll(ctx context.Context, _ identityapi.AuthLogoutAllRequestObject) (identityapi.AuthLogoutAllResponseObject, error) {
	cur, ok := app.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	logoutURL, err := h.sessions.LogoutAll(ctx, cur)
	if err != nil {
		return nil, err
	}
	return loggedOut{logoutURL: logoutURL}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type requestKey struct{}

// CaptureRequest is a strict middleware that hands the raw request to the handler: the
// intent cookie's name depends on the state, so it cannot be a declared parameter.
func CaptureRequest(f identityapi.StrictHandlerFunc, _ string) identityapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		return f(context.WithValue(ctx, requestKey{}, r), w, r, request)
	}
}

// requestFrom returns the request stored by CaptureRequest. Without it (a wiring bug)
// it returns an empty request, so no cookie is ever read from elsewhere.
func requestFrom(ctx context.Context) *http.Request {
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		return r
	}
	return (&http.Request{Header: http.Header{}}).WithContext(ctx)
}
