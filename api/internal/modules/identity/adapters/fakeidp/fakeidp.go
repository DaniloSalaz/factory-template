// Package fakeidp is an in-memory IdentityProvider for tests: each code maps to the
// authentication it yields, and the PKCE verifier must match the challenge sent to
// AuthorizationURL, as with the real provider.
package fakeidp

import (
	"context"
	"net/url"
	"sync"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
)

// AuthorizeBase is the fake provider's authorization endpoint.
const AuthorizeBase = "https://idp.test/authorize"

// Provider is the fake. The zero value is not usable; call New.
type Provider struct {
	mu         sync.Mutex
	challenges map[string]string // state -> challenge
	codes      map[string]pending
}

type pending struct {
	state string
	authn app.Authentication
}

var _ app.IdentityProvider = (*Provider)(nil)

// New returns an empty fake provider.
func New() *Provider {
	return &Provider{challenges: map[string]string{}, codes: map[string]pending{}}
}

// Name implements app.IdentityProvider.
func (*Provider) Name() string { return "fake" }

// AuthorizationURL implements app.IdentityProvider. The URL carries the state, the
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
func (p *Provider) Authenticate(state, code string, authn app.Authentication) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes[code] = pending{state: state, authn: authn}
}

// ExchangeCode implements app.IdentityProvider. Codes are single-use.
func (p *Provider) ExchangeCode(_ context.Context, code, codeVerifier string) (app.Authentication, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pend, ok := p.codes[code]
	delete(p.codes, code)
	if !ok || p.challenges[pend.state] != domain.CodeChallenge(codeVerifier) {
		return app.Authentication{}, apperr.BadRequest("auth.code_invalid", "The authorization code is invalid or expired.")
	}
	return pend.authn, nil
}

// LogoutURL implements app.IdentityProvider.
func (*Provider) LogoutURL(providerSessionID string) (string, error) {
	return "https://idp.test/logout?" + url.Values{"session_id": {providerSessionID}}.Encode(), nil
}
