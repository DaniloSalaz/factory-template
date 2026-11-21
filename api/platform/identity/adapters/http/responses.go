package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	identityapi "chaskicode.dev/saas/gen/openapi/identity"
	"chaskicode.dev/saas/modules/identity/app"
	"chaskicode.dev/saas/platform/httpx"
)

// The generated responses carry one value per header, and these set several cookies,
// so they implement the generated Visit methods themselves.

// redirect is a 302 that sets or clears cookies.
type redirect struct {
	location string
	cookies  []*http.Cookie
}

func (resp redirect) visit(w http.ResponseWriter) error {
	for _, c := range resp.cookies {
		http.SetCookie(w, c)
	}
	w.Header().Set("Location", resp.location)
	w.WriteHeader(http.StatusFound)
	return nil
}

func (resp redirect) VisitAuthLoginResponse(w http.ResponseWriter) error    { return resp.visit(w) }
func (resp redirect) VisitAuthCallbackResponse(w http.ResponseWriter) error { return resp.visit(w) }

// problem answers an error with httpx.WriteProblem but still sets cookies: a failed
// callback must delete its intent all the same.
type problem struct {
	err     error
	cookies []*http.Cookie
	r       *http.Request
	logger  *slog.Logger
}

func (resp problem) VisitAuthCallbackResponse(w http.ResponseWriter) error {
	for _, c := range resp.cookies {
		http.SetCookie(w, c)
	}
	httpx.WriteProblem(w, resp.r, resp.err, resp.logger)
	return nil
}

// loggedOut clears the session cookie and returns the provider's logout URL.
type loggedOut struct {
	logoutURL string
}

func (resp loggedOut) visit(w http.ResponseWriter) error {
	http.SetCookie(w, clearSessionCookie())
	body := identityapi.Logout{}
	if resp.logoutURL != "" {
		body.LogoutUrl = &resp.logoutURL
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		return fmt.Errorf("encode logout response: %w", err)
	}
	return nil
}

func (resp loggedOut) VisitAuthLogoutResponse(w http.ResponseWriter) error    { return resp.visit(w) }
func (resp loggedOut) VisitAuthLogoutAllResponse(w http.ResponseWriter) error { return resp.visit(w) }

// verified sets the cookie of the session that replaced the verified one.
type verified struct {
	rotated app.Rotated
}

func (resp verified) VisitVerifyTOTPResponse(w http.ResponseWriter) error {
	http.SetCookie(w, sessionCookie(resp.rotated.Token, resp.rotated.ExpiresAt))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(identityapi.MFAVerified{MfaVerifiedAt: resp.rotated.MFAAuthTime}); err != nil {
		return fmt.Errorf("encode verify response: %w", err)
	}
	return nil
}
