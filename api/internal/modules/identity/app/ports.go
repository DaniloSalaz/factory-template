// Package app holds the identity module's use cases (login, session check, logout) and
// the ports they need. No provider or database type crosses these ports.
package app

import (
	"context"
	"net/netip"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
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

// MFAProvider is the provider's MFA API (WorkOS today). The provider reports nothing
// about MFA in a login, so the app challenges the user's factor and verifies the code
// itself after the login (design, 5.2).
type MFAProvider interface {
	// EnrollTOTP creates a TOTP factor for the provider's user and its first challenge.
	EnrollTOTP(ctx context.Context, subject string) (TOTPEnrollment, error)
	// Challenge creates a challenge of a factor.
	Challenge(ctx context.Context, providerFactorID string) (providerChallengeID string, err error)
	// Verify checks a code against a challenge; valid is the provider's answer.
	Verify(ctx context.Context, providerChallengeID, code string) (valid bool, err error)
	// DeleteFactor removes a factor; one that no longer exists is not an error.
	DeleteFactor(ctx context.Context, providerFactorID string) error
}

// TOTPEnrollment is a factor just created at the provider, with what the user needs to
// add it to an authenticator app. The secret is shown once and never stored.
type TOTPEnrollment struct {
	FactorID    string
	ChallengeID string
	QRCode      pii.Secret // a PNG data: URI; it encodes the secret too
	URI         pii.Secret
	Secret      pii.Secret
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
	// AuthTime is when the user actively authenticated, per the provider. It says
	// nothing about MFA.
	AuthTime time.Time
}

// Store runs the module's transactions.
type Store interface {
	// Anon runs fn without a user: login and session lookups (db.WithAnonTx).
	Anon(ctx context.Context, fn func(ctx context.Context, r Repo) error) error
	// AsUser runs fn as userID (db.WithUserTx), so audit events name it as the actor.
	AsUser(ctx context.Context, userID string, fn func(ctx context.Context, r Repo) error) error
	// InTenant runs fn as p in its tenant (db.WithTenantTx).
	InTenant(ctx context.Context, p authctx.Principal, fn func(ctx context.Context, r Repo) error) error
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
	// RecordTenantAudit records an event of the transaction's tenant; only InTenant can.
	RecordTenantAudit(ctx context.Context, tenantID string, e AuditEvent) error

	// RolesInUse returns the distinct roles stored in any membership.
	RolesInUse(ctx context.Context) ([]string, error)
	// Membership returns the transaction user's membership in tenantID (a UUID); found
	// is false without one or without such a tenant.
	Membership(ctx context.Context, tenantID string) (m Membership, found bool, err error)
	// UserByID returns a user's profile.
	UserByID(ctx context.Context, id string) (p Profile, found bool, err error)
	// MembershipsForUser returns the transaction user's memberships, and only theirs.
	MembershipsForUser(ctx context.Context) ([]TenantMembership, error)
	// TenantByID returns a tenant; under RLS, only the transaction's own (InTenant).
	TenantByID(ctx context.Context, id string) (t Tenant, found bool, err error)

	// IdentitySubject returns the user's subject at provider.
	IdentitySubject(ctx context.Context, userID, provider string) (subject string, found bool, err error)
	// MFAFactors returns the transaction user's factors, and only theirs.
	MFAFactors(ctx context.Context) ([]MFAFactor, error)
	// RegisterMFAFactor records a factor just enrolled and its first challenge, bound to
	// the session of tokenHash.
	RegisterMFAFactor(ctx context.Context, tokenHash []byte, provider, providerFactorID, providerChallengeID string) (Challenge, error)
	// CreateMFAChallenge records a challenge of one of the user's factors, bound to the
	// session of tokenHash.
	CreateMFAChallenge(ctx context.Context, tokenHash []byte, factorID, providerChallengeID string) (Challenge, error)
	// ConsumeMFAChallenge uses up a challenge of the session of tokenHash.
	ConsumeMFAChallenge(ctx context.Context, tokenHash []byte, challengeID string) (ConsumedChallenge, error)
	// MarkSessionMFA replaces the session of tokenHash by a new one, with the token
	// newTokenHash and its MFA verified now.
	MarkSessionMFA(ctx context.Context, tokenHash []byte, challengeID string, codeHash, newTokenHash []byte) (MarkedSession, error)
	// RemoveMFAFactor deletes one of the user's factors and returns its provider id.
	RemoveMFAFactor(ctx context.Context, factorID string) (providerFactorID string, err error)
}

// TenantStore runs the transactions of tenant and member management (design, 5.5 and
// 5.7). It is apart from Store so that each use case sees only what it needs.
type TenantStore interface {
	// AsUser runs fn as userID, without a tenant (db.WithUserTx).
	AsUser(ctx context.Context, userID string, fn func(ctx context.Context, r TenantRepo) error) error
	// InTenant runs fn as p in its tenant (db.WithTenantTx).
	InTenant(ctx context.Context, p authctx.Principal, fn func(ctx context.Context, r TenantRepo) error) error
}

// TenantRepo is what tenant and member management reads and writes. The writes that
// change who is a member, or with which role, are the database functions that keep an
// owner in every tenant; they act only on the transaction's tenant.
type TenantRepo interface {
	// CreateTenant creates a tenant with the transaction's user as owner, unless they
	// have already created maxTenants.
	CreateTenant(ctx context.Context, name, slug string, maxTenants int, ip netip.Prefix) (tenantID string, err error)
	// RenameTenant renames the transaction's tenant and returns its previous name.
	RenameTenant(ctx context.Context, tenantID, name string) (previous string, t Tenant, err error)
	RecordTenantAudit(ctx context.Context, tenantID string, e AuditEvent) error

	// ListMembers returns up to limit members of the tenant after the membership after
	// ("" for the first page), in membership order.
	ListMembers(ctx context.Context, tenantID, after string, limit int) ([]Member, error)
	// MemberByUser returns the tenant's member userID; found is false if they are not one.
	MemberByUser(ctx context.Context, tenantID, userID string) (m Member, found bool, err error)
	// ChangeMemberRole and RemoveMember refuse (membership.changed) if the actor's or the
	// member's role is no longer the expected one, and never leave the tenant without an
	// owner (membership.last_owner).
	ChangeMemberRole(ctx context.Context, userID, role, expectedActorRole, expectedMemberRole string, ip netip.Prefix) error
	RemoveMember(ctx context.Context, userID, expectedActorRole, expectedMemberRole string, ip netip.Prefix) error
	// LeaveTenant removes the transaction user's own membership.
	LeaveTenant(ctx context.Context, ip netip.Prefix) error
}

// Member is a member of a tenant, as other members see them.
type Member struct {
	MembershipID string
	UserID       string
	Email        pii.Email
	DisplayName  string
	Role         string
	JoinedAt     time.Time
}

// MFAFactor is one of the user's factors.
type MFAFactor struct {
	ID               string
	Provider         string
	ProviderFactorID string
	VerifiedAt       time.Time // zero: enrolled, its first code not verified yet
	CreatedAt        time.Time
}

// Challenge is a challenge the user can answer once, before ExpiresAt.
type Challenge struct {
	ID        string
	FactorID  string
	ExpiresAt time.Time
}

// ConsumedChallenge is a challenge just used up by a verification attempt.
type ConsumedChallenge struct {
	ProviderChallengeID string
	FactorID            string
	FactorVerified      bool
}

// MarkedSession is the session that replaced the verified one.
type MarkedSession struct {
	SessionID         string
	MFAAuthTime       time.Time
	AbsoluteExpiresAt time.Time
	FactorID          string
	FirstVerification bool
}

// Tenant is a tenant as its members see it.
type Tenant struct {
	ID   string
	Slug string
	Name string
}

// Profile is what a user may see of themselves.
type Profile struct {
	ID          string
	Email       pii.Email
	DisplayName string
}

// TenantMembership is one of the acting user's tenants, with their role in it.
type TenantMembership struct {
	TenantID     string
	TenantSlug   string
	TenantName   string
	TenantStatus string
	Role         string
	// MFARequired: the role requires MFA in the catalog (set by Accounts, not the Repo).
	MFARequired bool
}

// Membership is the acting user's membership in a tenant, with the state of both.
type Membership struct {
	Role         string
	TenantActive bool
	UserDisabled bool
}

// ErrAlreadyExists is what a Repo returns when a create hits a unique constraint: a
// concurrent login created the same user or identity first.
func ErrAlreadyExists() error {
	return apperr.Conflict("identity.already_exists", "The user or identity already exists.")
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

// AuditEvent is an audit event; the actor is the transaction's user, if any.
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

	AuditMFAEnrolled     = "mfa.factor_enrolled"
	AuditMFAVerified     = "mfa.verified"
	AuditMFAVerifyFailed = "mfa.verify_failed"
	AuditMFAVerifyLocked = "mfa.verify_locked"
	// mfa.factor_removed is recorded by a database trigger, whoever removes the factor.

	AuditTenantUpdated = "tenant.updated"
	// tenant.created, membership.created, membership.role_changed, membership.removed
	// and membership.left are recorded by the database functions that make the change.
)
