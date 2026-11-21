// Package http exposes the identity module over HTTP: the generated /auth/* server and
// the session middleware.
package http

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	identityapi "chaskicode.dev/saas/gen/openapi/identity"
	"chaskicode.dev/saas/modules/identity/app"
	"chaskicode.dev/saas/modules/identity/domain"
	"chaskicode.dev/saas/platform/clientip"
)

var _ identityapi.StrictServerInterface = (*Handler)(nil)

// Handler implements the generated strict server interface.
type Handler struct {
	login       *usecases.Login
	sessions    *usecases.Sessions
	accounts    *usecases.Accounts
	mfa         *usecases.MFA
	tenants     *usecases.Tenants
	members     *usecases.Members
	invitations *usecases.Invitations
	logger      *slog.Logger
}

// UseCases are the use cases the handler exposes.
type UseCases struct {
	Login       *usecases.Login
	Sessions    *usecases.Sessions
	Accounts    *usecases.Accounts
	MFA         *usecases.MFA
	Tenants     *usecases.Tenants
	Members     *usecases.Members
	Invitations *usecases.Invitations
}

// NewHandler builds a Handler.
func NewHandler(uc UseCases, logger *slog.Logger) *Handler {
	return &Handler{
		login: uc.Login, sessions: uc.Sessions, accounts: uc.Accounts, mfa: uc.MFA,
		tenants: uc.Tenants, members: uc.Members, invitations: uc.Invitations, logger: logger,
	}
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
	cb := usecases.Callback{
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
	cur, ok := usecases.CurrentFrom(ctx)
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
	cur, ok := usecases.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	logoutURL, err := h.sessions.LogoutAll(ctx, cur)
	if err != nil {
		return nil, err
	}
	return loggedOut{logoutURL: logoutURL}, nil
}

// GetMe returns the current user, their memberships and their MFA (design, section 10).
func (h *Handler) GetMe(ctx context.Context, _ identityapi.GetMeRequestObject) (identityapi.GetMeResponseObject, error) {
	cur, ok := usecases.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	me, err := h.accounts.Me(ctx, cur)
	if err != nil {
		return nil, err
	}
	userID, err := uuid(me.User.ID)
	if err != nil {
		return nil, err
	}
	out := identityapi.GetMe200JSONResponse{
		// The user's own email, to the user: the API boundary is where pii is revealed.
		User:        identityapi.CurrentUser{Id: userID, Email: me.User.Email.Reveal(), DisplayName: me.User.DisplayName},
		Memberships: make([]identityapi.Membership, 0, len(me.Memberships)),
		MfaEnrolled: me.MFAEnrolled(),
		MfaFactors:  make([]identityapi.MFAFactor, 0, len(me.Factors)),
	}
	if !me.SessionMFAAt.IsZero() {
		at := me.SessionMFAAt
		out.SessionMfaAt = &at
	}
	for _, m := range me.Memberships {
		tenantID, err := uuid(m.TenantID)
		if err != nil {
			return nil, err
		}
		out.Memberships = append(out.Memberships, identityapi.Membership{
			TenantId: tenantID, TenantSlug: m.TenantSlug, TenantName: m.TenantName,
			TenantStatus: identityapi.MembershipTenantStatus(m.TenantStatus), Role: m.Role, MfaRequired: m.MFARequired,
		})
	}
	for _, f := range me.Factors {
		id, err := uuid(f.ID)
		if err != nil {
			return nil, err
		}
		out.MfaFactors = append(out.MfaFactors, identityapi.MFAFactor{
			Id: id, Type: identityapi.Totp, Verified: !f.VerifiedAt.IsZero(), CreatedAt: f.CreatedAt,
		})
	}
	return out, nil
}

// EnrollTOTP enrolls a TOTP factor (design, 5.2).
func (h *Handler) EnrollTOTP(ctx context.Context, _ identityapi.EnrollTOTPRequestObject) (identityapi.EnrollTOTPResponseObject, error) {
	cur, ok := usecases.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	e, err := h.mfa.Enroll(ctx, cur)
	if err != nil {
		return nil, err
	}
	factorID, err := uuid(e.Challenge.FactorID)
	if err != nil {
		return nil, err
	}
	challengeID, err := uuid(e.Challenge.ID)
	if err != nil {
		return nil, err
	}
	// Shown to the user once, at the API boundary; never logged or stored.
	return identityapi.EnrollTOTP201JSONResponse{
		FactorId: factorID, ChallengeId: challengeID, ExpiresAt: e.Challenge.ExpiresAt,
		QrCode: e.QRCode.Reveal(), Secret: e.Secret.Reveal(), Uri: e.URI.Reveal(),
	}, nil
}

// CreateMFAChallenge challenges one of the user's factors.
func (h *Handler) CreateMFAChallenge(ctx context.Context, req identityapi.CreateMFAChallengeRequestObject) (identityapi.CreateMFAChallengeResponseObject, error) {
	cur, ok := usecases.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	ch, err := h.mfa.Challenge(ctx, cur, req.Body.FactorId.String())
	if err != nil {
		return nil, err
	}
	challengeID, err := uuid(ch.ID)
	if err != nil {
		return nil, err
	}
	return identityapi.CreateMFAChallenge201JSONResponse{
		ChallengeId: challengeID, FactorId: req.Body.FactorId, ExpiresAt: ch.ExpiresAt,
	}, nil
}

// VerifyTOTP answers a challenge; on success the session is replaced and the browser
// gets the new cookie.
func (h *Handler) VerifyTOTP(ctx context.Context, req identityapi.VerifyTOTPRequestObject) (identityapi.VerifyTOTPResponseObject, error) {
	cur, ok := usecases.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	rotated, err := h.mfa.Verify(ctx, cur, req.Body.ChallengeId.String(), req.Body.Code)
	if err != nil {
		return nil, err
	}
	return verified{rotated: rotated}, nil
}

// RemoveTOTP removes one of the user's factors.
func (h *Handler) RemoveTOTP(ctx context.Context, req identityapi.RemoveTOTPRequestObject) (identityapi.RemoveTOTPResponseObject, error) {
	cur, ok := usecases.CurrentFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	if err := h.mfa.Remove(ctx, cur, req.FactorId.String()); err != nil {
		return nil, err
	}
	return identityapi.RemoveTOTP204Response{}, nil
}

// GetCurrentTenant returns the tenant of X-Tenant-ID with the caller's role and
// permissions in it.
func (h *Handler) GetCurrentTenant(ctx context.Context, _ identityapi.GetCurrentTenantRequestObject) (identityapi.GetCurrentTenantResponseObject, error) {
	cur, err := h.accounts.CurrentTenant(ctx)
	if err != nil {
		return nil, err
	}
	id, err := uuid(cur.Tenant.ID)
	if err != nil {
		return nil, err
	}
	p := cur.Principal
	perms := make([]string, 0, len(p.Permissions()))
	for _, perm := range p.Permissions() {
		perms = append(perms, string(perm))
	}
	return identityapi.GetCurrentTenant200JSONResponse{
		Id: id, Slug: cur.Tenant.Slug, Name: cur.Tenant.Name, Role: p.Role(),
		Scope: identityapi.CurrentTenantScope(p.Scope()), Permissions: perms,
	}, nil
}

func uuid(s string) (openapi_types.UUID, error) {
	var id openapi_types.UUID
	if err := id.UnmarshalText([]byte(s)); err != nil {
		return id, fmt.Errorf("parse uuid: %w", err)
	}
	return id, nil
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
