// Package workos implements app.IdentityProvider with WorkOS AuthKit. It is the only
// package that imports the WorkOS SDK; none of its types leave it.
package workos

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	sdk "github.com/workos/workos-go/v7"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// ProviderName is stored in user_identities.provider.
const ProviderName = "workos"

const requestTimeout = 10 * time.Second

// Config configures the adapter.
type Config struct {
	ClientID    string
	APIKey      pii.Secret
	RedirectURI string
	// BaseURL overrides the API host; empty means WorkOS. Tests point it at a fake.
	BaseURL string
}

// Provider is the WorkOS AuthKit identity provider.
type Provider struct {
	um          *sdk.UserManagementService
	redirectURI string
	logger      *slog.Logger
}

var _ app.IdentityProvider = (*Provider)(nil)

// New builds the provider.
func New(cfg Config, logger *slog.Logger) *Provider {
	opts := []sdk.ClientOption{
		sdk.WithClientID(cfg.ClientID),
		sdk.WithHTTPClient(&http.Client{Timeout: requestTimeout}),
		// An authorization code is single-use: retrying its exchange cannot succeed where
		// the first attempt reached WorkOS, and only delays the error.
		sdk.WithMaxRetries(0),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, sdk.WithBaseURL(cfg.BaseURL))
	}
	client := sdk.NewClient(cfg.APIKey.Reveal(), opts...)
	return &Provider{um: client.UserManagement(), redirectURI: cfg.RedirectURI, logger: logger}
}

// Name implements app.IdentityProvider.
func (*Provider) Name() string { return ProviderName }

// AuthorizationURL implements app.IdentityProvider. max_age=0 forces a fresh
// authentication even when AuthKit has a session, so auth_time is always the time of
// this login (design, 5.2 and 13); WorkOS supports it only with provider=authkit, and
// SDK v7 has no field for it.
func (p *Provider) AuthorizationURL(state, codeChallenge string) (string, error) {
	method := "S256"
	provider := sdk.UserManagementAuthenticationProviderAuthkit
	raw := p.um.GetAuthorizationURL(&sdk.UserManagementGetAuthorizationURLParams{
		RedirectURI: p.redirectURI, State: &state, Provider: &provider,
		CodeChallenge: &codeChallenge, CodeChallengeMethod: &method,
	})
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse authorization url: %w", err)
	}
	q := u.Query()
	q.Set("max_age", "0")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExchangeCode implements app.IdentityProvider.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (app.Authentication, error) {
	res, err := p.um.AuthenticateWithCode(ctx, &sdk.UserManagementAuthenticateWithCodeParams{
		Code: code, CodeVerifier: &codeVerifier,
	})
	if err != nil {
		return app.Authentication{}, classify(err)
	}
	if res.User == nil || res.User.ID == "" || res.User.Email == "" {
		return app.Authentication{}, unavailable(errors.New("workos: authentication response without a user"))
	}

	claims, names, err := decodeClaims(res.AccessToken)
	if err != nil {
		return app.Authentication{}, unavailable(err)
	}
	// Only the names, never the values: this is how the runbook checks whether WorkOS
	// started sending an MFA claim (docs/runbooks/auth.md).
	p.logger.DebugContext(ctx, "workos access token claims", slog.Any("claims", names))
	if claims.Subject != res.User.ID {
		return app.Authentication{}, unavailable(errors.New("workos: access token sub does not match the user"))
	}
	if claims.AuthTime == nil || *claims.AuthTime <= 0 {
		return app.Authentication{}, unavailable(errors.New("workos: access token without auth_time"))
	}

	method := ""
	if res.AuthenticationMethod != nil {
		method = string(*res.AuthenticationMethod)
	}
	return app.Authentication{
		Subject:       res.User.ID,
		Email:         pii.Email(res.User.Email),
		EmailVerified: res.User.EmailVerified,
		DisplayName:   displayName(res.User.FirstName, res.User.LastName),
		SessionID:     claims.SessionID,
		Method:        method,
		Impersonated:  res.Impersonator != nil,
		MFA: domain.ProviderMFA{
			AuthTime:        time.Unix(*claims.AuthTime, 0).UTC(),
			CoveredByPolicy: coveredByMFAPolicy(method),
			AMR:             claims.AMR,
			HasAMR:          claims.AMR != nil,
		},
	}, nil
}

// LogoutURL implements app.IdentityProvider. Without return_to, WorkOS sends the
// browser to the dashboard's default Sign-out URI.
func (p *Provider) LogoutURL(providerSessionID string) (string, error) {
	if providerSessionID == "" {
		return "", errors.New("workos: empty session id")
	}
	return p.um.GetLogoutURL(&sdk.UserManagementGetLogoutURLParams{SessionID: providerSessionID}), nil
}

// coveredByMFAPolicy is the allowlist of authentication methods confirmed, with a test in
// staging, to make AuthKit ask for the second factor when MFA is Required (design, 5.2).
// Every other method, unknown ones included, never counts as MFA: WorkOS documents that
// its MFA does not apply to SSO and says nothing about Magic Auth, passkeys or OAuth.
// Adding one follows docs/runbooks/auth.md.
func coveredByMFAPolicy(method string) bool {
	switch sdk.AuthenticateResponseAuthenticationMethod(method) { //nolint:exhaustive // an allowlist: every other method is not covered
	case sdk.AuthenticateResponseAuthenticationMethodPassword:
		return true
	default:
		return false
	}
}

type accessClaims struct {
	Subject   string   `json:"sub"`
	SessionID string   `json:"sid"`
	AuthTime  *int64   `json:"auth_time"`
	AMR       []string `json:"amr"`
}

// decodeClaims reads the access token's payload without verifying its signature. That
// is sound only because the token comes straight from WorkOS in the response to a
// request authenticated with the API key over TLS.
//
// It must NEVER be given a token that came from the browser (a cookie, a header, a
// query parameter or a body): anyone can write a JWT with any sub, sid, auth_time or
// amr, and without the signature check this function would accept it. The BFF never
// hands provider tokens to the browser; a flow that needs to accept one must verify it
// against WorkOS's JWKS (issuer, audience, expiry) instead of calling this.
func decodeClaims(token string) (accessClaims, []string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return accessClaims{}, nil, errors.New("workos: access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return accessClaims{}, nil, fmt.Errorf("workos: decode access token payload: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return accessClaims{}, nil, fmt.Errorf("workos: parse access token claims: %w", err)
	}
	var c accessClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return accessClaims{}, nil, fmt.Errorf("workos: parse access token claims: %w", err)
	}
	return c, slices.Sorted(maps.Keys(raw)), nil
}

func displayName(first, last *string) string {
	var parts []string
	for _, p := range []*string{first, last} {
		if p != nil && strings.TrimSpace(*p) != "" {
			parts = append(parts, strings.TrimSpace(*p))
		}
	}
	return strings.Join(parts, " ")
}

// classify maps SDK errors to apperr. The SDK error is replaced by a summary built from
// its typed fields: its message can quote the response body, so it is never kept.
func classify(err error) error {
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		summary := fmt.Errorf("workos: status %d, code %q, request %s", apiErr.StatusCode, apiErr.ErrorCode+apiErr.Code, apiErr.RequestID)
		if apiErr.StatusCode >= http.StatusBadRequest && apiErr.StatusCode < http.StatusInternalServerError &&
			apiErr.StatusCode != http.StatusTooManyRequests && apiErr.StatusCode != http.StatusUnauthorized {
			return apperr.Wrap(summary, apperr.KindBadRequest, "auth.code_invalid", "The authorization code is invalid or expired.")
		}
		return unavailable(summary)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return unavailable(errors.New("workos: request cancelled or timed out"))
	}
	return unavailable(errors.New("workos: request failed"))
}

func unavailable(cause error) error {
	return apperr.Wrap(cause, apperr.KindUnavailable, "auth.provider_unavailable",
		"The identity provider is unavailable; try again later.")
}
