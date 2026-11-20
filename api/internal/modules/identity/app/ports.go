// Package app holds the identity module's use cases (login, session check, logout) and
// the ports they need. No provider or database type crosses these ports.
package app

import (
	"context"
	"net/netip"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// IdentityProvider is the external login service (WorkOS AuthKit today).
type IdentityProvider interface {
	// Name is the provider stored in user_identities.provider.
	Name() string
	// AuthorizationURL returns where to send the browser to authenticate. It always
	// forces a fresh authentication, even if the provider has a session.
	AuthorizationURL(state, codeChallenge string) (string, error)
	// ExchangeCode redeems the callback's code with its PKCE verifier.
	ExchangeCode(ctx context.Context, code, codeVerifier string) (Authentication, error)
	// LogoutURL returns where to send the browser to end the provider's session.
	LogoutURL(providerSessionID string) (string, error)
}

// Authentication is what the provider reports about a completed authentication.
type Authentication struct {
	Subject       string
	Email         pii.Email
	EmailVerified bool
	DisplayName   string
	// SessionID is the provider's own session, ended at logout.
	SessionID string
	// Method names how the user authenticated, for the audit trail.
	Method string
	// Impersonated is true when someone else (a provider admin) signed in as the user.
	Impersonated bool
	MFA          domain.ProviderMFA
}

// Store runs the module's transactions.
type Store interface {
	// Anon runs fn without a user: login and session lookups (db.WithAnonTx).
	Anon(ctx context.Context, fn func(ctx context.Context, r Repo) error) error
	// AsUser runs fn as userID (db.WithUserTx), so audit events name it as the actor.
	AsUser(ctx context.Context, userID string, fn func(ctx context.Context, r Repo) error) error
}

// Repo is what the use cases read and write inside a transaction.
type Repo interface {
	UserByIdentity(ctx context.Context, provider, subject string) (User, bool, error)
	UserByEmail(ctx context.Context, email pii.Email) (User, bool, error)
	CreateUser(ctx context.Context, email pii.Email, emailVerified bool, displayName string) (string, error)
	CreateIdentity(ctx context.Context, userID, provider, subject string, email pii.Email) error
	UpdateUserEmail(ctx context.Context, userID string, email pii.Email, emailVerified bool) error
	SetLastLogin(ctx context.Context, userID string) error

	CreateSession(ctx context.Context, s NewSession) (string, error)
	SessionByHash(ctx context.Context, tokenHash []byte) (Session, bool, error)
	TouchSession(ctx context.Context, id string, now, idleExpiresAt time.Time) error
	RevokeSessionByHash(ctx context.Context, tokenHash []byte) error
	RevokeSession(ctx context.Context, id string) error
	RevokeUserSessions(ctx context.Context, userID string) (int64, error)

	RecordAudit(ctx context.Context, e AuditEvent) error
}

// User is a stored user, as the login sees it.
type User struct {
	ID       string
	Email    pii.Email
	Disabled bool
}

// NewSession is a session to create. Only the token's hash is stored.
type NewSession struct {
	TokenHash         []byte
	UserID            string
	AuthTime          time.Time
	MFAAuthTime       time.Time // zero: no MFA
	Now               time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	IPPrefix          netip.Prefix // invalid: unknown
	UserAgent         string
	ProviderSessionID string
}

// Session is a stored session with the state of its user.
type Session struct {
	ID                string
	UserID            string
	MFAAuthTime       time.Time
	ProviderSessionID string
	State             domain.SessionState
}

// AuditEvent is an event without a tenant; the actor is the transaction's user, if any.
// Metadata never holds personal data (design, section 9).
type AuditEvent struct {
	Action     string
	TargetType string
	TargetID   string
	Metadata   map[string]string
	IPPrefix   netip.Prefix
}

// Audit actions of the identity module (design, section 9).
const (
	AuditLogin       = "auth.login"
	AuditLoginFailed = "auth.login_failed"
	AuditEmailInUse  = "auth.email_in_use"
	AuditLogout      = "auth.logout"
)
