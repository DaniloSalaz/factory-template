// Package fakeidp is an in-memory IdentityProvider and MFAProvider for tests: each code
// maps to the authentication it yields, and the PKCE verifier must match the challenge
// sent to AuthorizationURL, as with the real provider. Each TOTP factor accepts the code
// the test sets with SetCode, as often as it is sent: like WorkOS, the fake does not
// refuse a reused code, so the app's own protection is what the tests exercise.
package fakeidp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"chaskicode.dev/saas/core/identity/domain"
	"chaskicode.dev/saas/core/identity/usecases"
	"chaskicode.dev/saas/core/kit/apperr"
)

// AuthorizeBase is the fake provider's authorization endpoint.
const AuthorizeBase = "https://idp.test/authorize"

// Provider is the fake. The zero value is not usable; call New.
type Provider struct {
	mu         sync.Mutex
	challenges map[string]string // state -> challenge
	codes      map[string]pending

	nextID        int
	factors       map[string]string // factor id -> subject
	factorCode    map[string]string // factor id -> the code it accepts
	mfaChallenges map[string]string // challenge id -> factor id
	failNext      bool              // see FailNextMFACall
}

var (
	_ usecases.IdentityProvider = (*Provider)(nil)
	_ usecases.MFAProvider      = (*Provider)(nil)
)

type pending struct {
	state string
	authn usecases.Authentication
}

var (
	_ usecases.IdentityProvider = (*Provider)(nil)
	_ usecases.MFAProvider      = (*Provider)(nil)
)

// New returns an empty fake provider.
func New() *Provider {
	return &Provider{
		challenges: map[string]string{}, codes: map[string]pending{},
		factors: map[string]string{}, factorCode: map[string]string{}, mfaChallenges: map[string]string{},
	}
}

// errUnavailable is what a provider outage looks like to the app.
func errUnavailable() error {
	return apperr.Wrap(errors.New("fakeidp: down"), apperr.KindUnavailable, "auth.provider_unavailable",
		"The identity provider is unavailable; try again later.")
}

// SetCode sets the code factorID accepts from now on.
func (p *Provider) SetCode(factorID, code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.factorCode[factorID] = code
}

// FailNextMFACall makes the next MFA call fail as unavailable.
func (p *Provider) FailNextMFACall() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext = true
}

// Factors returns how many factors the fake holds.
func (p *Provider) Factors() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.factors)
}

func (p *Provider) failing() bool {
	f := p.failNext
	p.failNext = false
	return f
}

// id is unique across fakes too: tests that share a database each have their own fake.
func (p *Provider) id(prefix string) string {
	p.nextID++
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%d_%s", prefix, p.nextID, hex.EncodeToString(b))
}

// The new factor accepts "000000" until SetCode.
func (p *Provider) EnrollTOTP(_ context.Context, subject string) (usecases.TOTPEnrollment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing() {
		return usecases.TOTPEnrollment{}, errUnavailable()
	}
	f, c := p.id("auth_factor"), p.id("auth_challenge")
	p.factors[f], p.factorCode[f], p.mfaChallenges[c] = subject, "000000", f
	return usecases.TOTPEnrollment{
		FactorID: f, ChallengeID: c, QRCode: "data:image/png;base64,QR", URI: "otpauth://totp/fake?secret=FAKE",
		Secret: "FAKESECRET",
	}, nil
}

func (p *Provider) Challenge(_ context.Context, providerFactorID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing() {
		return "", errUnavailable()
	}
	if _, ok := p.factors[providerFactorID]; !ok {
		return "", apperr.Conflict("mfa.provider_rejected", "The identity provider rejected the request; start again.")
	}
	c := p.id("auth_challenge")
	p.mfaChallenges[c] = providerFactorID
	return c, nil
}

func (p *Provider) Verify(_ context.Context, providerChallengeID, code string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing() {
		return false, errUnavailable()
	}
	f, ok := p.mfaChallenges[providerChallengeID]
	if !ok {
		return false, apperr.Conflict("mfa.provider_rejected", "The identity provider rejected the request; start again.")
	}
	_, exists := p.factors[f]
	return exists && p.factorCode[f] == code, nil
}

func (p *Provider) DeleteFactor(_ context.Context, providerFactorID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing() {
		return errUnavailable()
	}
	delete(p.factors, providerFactorID)
	return nil
}

func (*Provider) Name() string { return "fake" }

// The URL carries the state, the
// challenge and max_age=0, like the real one.
func (p *Provider) AuthorizationURL(state, codeChallenge string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.challenges[state] = codeChallenge
	q := url.Values{"state": {state}, "code_challenge": {codeChallenge}, "code_challenge_method": {"S256"}, "max_age": {"0"}}
	return AuthorizeBase + "?" + q.Encode(), nil
}

// Authenticate simulates the user authenticating for state: it returns the code the
// provider would send to the callback.
func (p *Provider) Authenticate(state, code string, authn usecases.Authentication) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes[code] = pending{state: state, authn: authn}
}

// Codes are single-use.
func (p *Provider) ExchangeCode(_ context.Context, code, codeVerifier string) (usecases.Authentication, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pend, ok := p.codes[code]
	delete(p.codes, code)
	if !ok || p.challenges[pend.state] != domain.CodeChallenge(codeVerifier) {
		return usecases.Authentication{}, apperr.BadRequest("auth.code_invalid", "The authorization code is invalid or expired.")
	}
	return pend.authn, nil
}

func (*Provider) LogoutURL(providerSessionID string) (string, error) {
	return "https://idp.test/logout?" + url.Values{"session_id": {providerSessionID}}.Encode(), nil
}
