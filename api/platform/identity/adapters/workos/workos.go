// Package workos implements usecases.IdentityProvider with WorkOS AuthKit, and usecases.MFAProvider
// with its MFA API. It is the only package that imports the WorkOS SDK; none of its
// types leave it.
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

	"go.opentelemetry.io/otel/trace"

	sdk "github.com/workos/workos-go/v7"

	"chaskicode.dev/saas/modules/identity/app"
	"chaskicode.dev/saas/platform/apperr"
	"chaskicode.dev/saas/platform/pii"
	"chaskicode.dev/saas/platform/telemetry"
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
	// Tracers traces each call as a client span (peer.service workos), without URL,
	// parameters or body; nil traces nothing.
	Tracers trace.TracerProvider
}

// Provider is the WorkOS AuthKit identity provider.
type Provider struct {
	um          *sdk.UserManagementService
	mfa         *sdk.MultiFactorAuthService
	redirectURI string
	tracers     trace.TracerProvider
	logger      *slog.Logger
}

var (
	_ usecases.IdentityProvider = (*Provider)(nil)
	_ usecases.MFAProvider      = (*Provider)(nil)
)

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
	return &Provider{um: client.UserManagement(), mfa: client.MultiFactorAuth(), redirectURI: cfg.RedirectURI, tracers: cfg.Tracers, logger: logger}
}

// Name implements usecases.IdentityProvider.
func (*Provider) Name() string { return ProviderName }

// AuthorizationURL implements usecases.IdentityProvider. max_age=0 forces a fresh
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

// ExchangeCode implements usecases.IdentityProvider.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (_ usecases.Authentication, err error) {
	ctx, end := telemetry.ClientSpan(ctx, p.tracers, "workos", "authenticate_with_code")
	defer func() { end(err) }()
	res, err := p.um.AuthenticateWithCode(ctx, &sdk.UserManagementAuthenticateWithCodeParams{
		Code: code, CodeVerifier: &codeVerifier,
	})
	if err != nil {
		return usecases.Authentication{}, classify(err)
	}
	if res.User == nil || res.User.ID == "" || res.User.Email == "" {
		return usecases.Authentication{}, unavailable(errors.New("workos: authentication response without a user"))
	}

	claims, names, err := decodeClaims(res.AccessToken)
	if err != nil {
		return usecases.Authentication{}, unavailable(err)
	}
	// Only the names, never the values: whether WorkOS ever starts sending an MFA claim
	// (design, section 13) shows here.
	p.logger.DebugContext(ctx, "workos access token claims", slog.Any("claims", names))
	if claims.Subject != res.User.ID {
		return usecases.Authentication{}, unavailable(errors.New("workos: access token sub does not match the user"))
	}
	if claims.AuthTime == nil || *claims.AuthTime <= 0 {
		return usecases.Authentication{}, unavailable(errors.New("workos: access token without auth_time"))
	}

	method := ""
	if res.AuthenticationMethod != nil {
		method = string(*res.AuthenticationMethod)
	}
	return usecases.Authentication{
		Subject:       res.User.ID,
		Email:         pii.Email(res.User.Email),
		EmailVerified: res.User.EmailVerified,
		DisplayName:   displayName(res.User.FirstName, res.User.LastName),
		SessionID:     claims.SessionID,
		Method:        method,
		Impersonated:  res.Impersonator != nil,
		AuthTime:      time.Unix(*claims.AuthTime, 0).UTC(),
	}, nil
}

// LogoutURL implements usecases.IdentityProvider. Without return_to, WorkOS sends the
// browser to the dashboard's default Sign-out URI.
func (p *Provider) LogoutURL(providerSessionID string) (string, error) {
	if providerSessionID == "" {
		return "", errors.New("workos: empty session id")
	}
	return p.um.GetLogoutURL(&sdk.UserManagementGetLogoutURLParams{SessionID: providerSessionID}), nil
}

// EnrollTOTP implements usecases.MFAProvider. Issuer and account name are left to WorkOS: its
// team name and the user's email.
func (p *Provider) EnrollTOTP(ctx context.Context, subject string) (_ usecases.TOTPEnrollment, err error) {
	ctx, end := telemetry.ClientSpan(ctx, p.tracers, "workos", "create_auth_factor")
	defer func() { end(err) }()
	res, err := p.mfa.CreateUserAuthFactor(ctx, subject, &sdk.MultiFactorAuthCreateUserAuthFactorParams{Type: "totp"})
	if err != nil {
		return usecases.TOTPEnrollment{}, classifyMFA(err)
	}
	f, c := res.AuthenticationFactor, res.AuthenticationChallenge
	if f == nil || f.ID == "" || f.TOTP == nil || c == nil || c.ID == "" {
		return usecases.TOTPEnrollment{}, unavailable(errors.New("workos: enrollment response without a factor or challenge"))
	}
	return usecases.TOTPEnrollment{
		FactorID: f.ID, ChallengeID: c.ID,
		QRCode: pii.Secret(f.TOTP.QrCode), URI: pii.Secret(f.TOTP.URI), Secret: pii.Secret(f.TOTP.Secret),
	}, nil
}

// Challenge implements usecases.MFAProvider.
func (p *Provider) Challenge(ctx context.Context, providerFactorID string) (_ string, err error) {
	ctx, end := telemetry.ClientSpan(ctx, p.tracers, "workos", "challenge_factor")
	defer func() { end(err) }()
	c, err := p.mfa.ChallengeFactor(ctx, providerFactorID, &sdk.MultiFactorAuthChallengeFactorParams{})
	if err != nil {
		return "", classifyMFA(err)
	}
	if c == nil || c.ID == "" {
		return "", unavailable(errors.New("workos: challenge response without an id"))
	}
	return c.ID, nil
}

// Verify implements usecases.MFAProvider: only valid: true counts.
func (p *Provider) Verify(ctx context.Context, providerChallengeID, code string) (_ bool, err error) {
	ctx, end := telemetry.ClientSpan(ctx, p.tracers, "workos", "verify_challenge")
	defer func() { end(err) }()
	res, err := p.mfa.VerifyChallenge(ctx, providerChallengeID, &sdk.MultiFactorAuthVerifyChallengeParams{Code: code})
	if err != nil {
		return false, classifyMFA(err)
	}
	return res != nil && res.Valid, nil
}

// DeleteFactor implements usecases.MFAProvider. A factor WorkOS no longer has is already gone.
func (p *Provider) DeleteFactor(ctx context.Context, providerFactorID string) (err error) {
	ctx, end := telemetry.ClientSpan(ctx, p.tracers, "workos", "delete_factor")
	defer func() { end(err) }()
	err = p.mfa.DeleteFactor(ctx, providerFactorID)
	var notFound *sdk.NotFoundError
	if err == nil || errors.As(err, &notFound) {
		return nil
	}
	return classifyMFA(err)
}

// classifyMFA maps MFA API errors to apperr, keeping only the typed fields. A rejected
// request (4xx) means a factor or challenge WorkOS does not know: the user starts over.
func classifyMFA(err error) error {
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		summary := summarize(apiErr)
		if apiErr.StatusCode >= http.StatusBadRequest && apiErr.StatusCode < http.StatusInternalServerError &&
			apiErr.StatusCode != http.StatusTooManyRequests && apiErr.StatusCode != http.StatusUnauthorized {
			return apperr.Wrap(summary, apperr.KindConflict, "mfa.provider_rejected",
				"The identity provider rejected the request; start again.")
		}
		return unavailable(summary)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return unavailable(errors.New("workos: request cancelled or timed out"))
	}
	return unavailable(errors.New("workos: request failed"))
}

type accessClaims struct {
	Subject   string `json:"sub"`
	SessionID string `json:"sid"`
	AuthTime  *int64 `json:"auth_time"`
}

// decodeClaims reads the access token's payload without verifying its signature. That
// is sound only because the token comes straight from WorkOS in the response to a
// request authenticated with the API key over TLS.
//
// It must NEVER be given a token that came from the browser (a cookie, a header, a
// query parameter or a body): anyone can write a JWT with any sub, sid or auth_time,
// and without the signature check this function would accept it. The BFF never
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
	// The user still has a step to finish at WorkOS: tell them which, not "invalid code".
	var emailErr *sdk.EmailVerificationRequiredError
	if errors.As(err, &emailErr) {
		return apperr.Wrap(summarize(emailErr.APIError), apperr.KindForbidden, "auth.email_verification_required",
			"Verify your email address with the identity provider, then sign in again.")
	}
	var mfaErr *sdk.MFAEnrollmentError
	if errors.As(err, &mfaErr) {
		return apperr.Wrap(summarize(mfaErr.APIError), apperr.KindForbidden, "auth.mfa_enrollment_required",
			"Set up multi-factor authentication with the identity provider, then sign in again.")
	}

	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		summary := summarize(apiErr)
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

// summarize keeps only the typed fields of an SDK error, never its message or body.
func summarize(apiErr *sdk.APIError) error {
	if apiErr == nil {
		return errors.New("workos: error without details")
	}
	return fmt.Errorf("workos: status %d, code %q, request %s", apiErr.StatusCode, apiErr.ErrorCode+apiErr.Code, apiErr.RequestID)
}

func unavailable(cause error) error {
	return apperr.Wrap(cause, apperr.KindUnavailable, "auth.provider_unavailable",
		"The identity provider is unavailable; try again later.")
}
