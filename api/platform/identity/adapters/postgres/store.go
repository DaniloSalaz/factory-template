// Package postgres implements the identity module's Store with sqlc, inside the
// transactions of platform/kit/db.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	identitydb "chaskicode.dev/saas/gen/sqlc/identity"
	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/identity/usecases"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/db"
	"chaskicode.dev/saas/platform/kit/pii"
)

type Store struct {
	db *db.DB
}

var _ usecases.Store = (*Store)(nil)

// NewStore builds a Store over a database that connects as app_runtime.
func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) Anon(ctx context.Context, fn func(context.Context, usecases.Repo) error) error {
	err := s.db.WithAnonTx(ctx, func(ctx context.Context, q db.Querier) error {
		return fn(ctx, repo{q: identitydb.New(q)})
	})
	return db.MapError(err)
}

func (s *Store) AsUser(ctx context.Context, userID string, fn func(context.Context, usecases.Repo) error) error {
	ctx, err := asUser(ctx, userID)
	if err != nil {
		return err
	}
	err = s.db.WithUserTx(ctx, func(ctx context.Context, q db.Querier) error {
		return fn(ctx, repo{q: identitydb.New(q)})
	})
	return db.MapError(err)
}

func (s *Store) InTenant(ctx context.Context, p authctx.Principal, fn func(context.Context, usecases.Repo) error) error {
	err := s.db.WithTenantTx(inTenant(ctx, p), func(ctx context.Context, q db.Querier) error {
		return fn(ctx, repo{q: identitydb.New(q)})
	})
	return db.MapError(err)
}

// asUser returns ctx with userID as its authctx.User, which db.WithUserTx reads.
func asUser(ctx context.Context, userID string) (context.Context, error) {
	if u, ok := authctx.UserFrom(ctx); ok && u.ID() == userID {
		return ctx, nil
	}
	u, err := authctx.NewUser(userID, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("transaction user: %w", err)
	}
	return authctx.WithUser(ctx, u), nil
}

// inTenant returns ctx with p as its authctx.Principal, which db.WithTenantTx reads.
func inTenant(ctx context.Context, p authctx.Principal) context.Context {
	if cur, ok := authctx.PrincipalFrom(ctx); ok && cur.UserID() == p.UserID() && cur.TenantID() == p.TenantID() &&
		cur.Role() == p.Role() {
		return ctx
	}
	return authctx.WithPrincipal(ctx, p)
}

type repo struct {
	q *identitydb.Queries
}

func (r repo) UserByIdentity(ctx context.Context, provider, subject string) (usecases.User, bool, error) {
	row, err := r.q.UserByIdentity(ctx, identitydb.UserByIdentityParams{Provider: provider, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.User{}, false, nil
	}
	if err != nil {
		return usecases.User{}, false, fmt.Errorf("user by identity: %w", err)
	}
	return usecases.User{ID: row.ID.String(), Email: pii.Email(row.Email), Disabled: row.Disabled}, true, nil
}

func (r repo) UserByEmail(ctx context.Context, email pii.Email) (usecases.User, bool, error) {
	row, err := r.q.UserByEmail(ctx, email.Reveal())
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.User{}, false, nil
	}
	if err != nil {
		return usecases.User{}, false, fmt.Errorf("user by email: %w", err)
	}
	return usecases.User{ID: row.ID.String(), Email: pii.Email(row.Email), Disabled: row.Disabled}, true, nil
}

func (r repo) CreateUser(ctx context.Context, email pii.Email, emailVerified bool, displayName string) (string, error) {
	id, err := r.q.CreateUser(ctx, identitydb.CreateUserParams{
		Email: email.Reveal(), EmailVerified: emailVerified, DisplayName: displayName,
	})
	if err != nil {
		return "", fmt.Errorf("create user: %w", alreadyExists(err))
	}
	return id.String(), nil
}

func (r repo) CreateIdentity(ctx context.Context, userID, provider, subject string, email pii.Email) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.q.CreateIdentity(ctx, identitydb.CreateIdentityParams{
		UserID: uid, Provider: provider, Subject: subject, Email: email.Reveal(),
	}); err != nil {
		return fmt.Errorf("create identity: %w", alreadyExists(err))
	}
	return nil
}

func (r repo) UpdateUserEmail(ctx context.Context, userID string, email pii.Email, emailVerified bool) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.q.UpdateUserEmail(ctx, identitydb.UpdateUserEmailParams{
		ID: uid, Email: email.Reveal(), EmailVerified: emailVerified,
	}); err != nil {
		return fmt.Errorf("update user email: %w", err)
	}
	return nil
}

func (r repo) SetLastLogin(ctx context.Context, userID string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.q.SetLastLogin(ctx, uid); err != nil {
		return fmt.Errorf("set last login: %w", err)
	}
	return nil
}

func (r repo) CreateSession(ctx context.Context, s usecases.NewSession) (string, error) {
	uid, err := parseUUID(s.UserID)
	if err != nil {
		return "", err
	}
	id, err := r.q.CreateSession(ctx, identitydb.CreateSessionParams{
		TokenHash: s.TokenHash, UserID: uid,
		AuthTime: ts(s.AuthTime), Now: ts(s.Now),
		IdleExpiresAt: ts(s.IdleExpiresAt), AbsoluteExpiresAt: ts(s.AbsoluteExpiresAt),
		IpPrefix: prefix(s.IPPrefix), UserAgent: text(s.UserAgent), ProviderSessionID: text(s.ProviderSessionID),
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return id.String(), nil
}

func (r repo) SessionByHash(ctx context.Context, tokenHash []byte) (usecases.Session, bool, error) {
	row, err := r.q.SessionByHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.Session{}, false, nil
	}
	if err != nil {
		return usecases.Session{}, false, fmt.Errorf("session by hash: %w", err)
	}
	return usecases.Session{
		ID: row.ID.String(), UserID: row.UserID.String(),
		MFAAuthTime: row.MfaAuthTime.Time, ProviderSessionID: row.ProviderSessionID.String,
		State: domain.SessionState{
			LastSeenAt: row.LastSeenAt.Time, IdleExpiresAt: row.IdleExpiresAt.Time,
			AbsoluteExpiresAt: row.AbsoluteExpiresAt.Time, Revoked: row.RevokedAt.Valid, UserDisabled: row.UserDisabled,
		},
	}, true, nil
}

func (r repo) TouchSession(ctx context.Context, id string, now, idleExpiresAt time.Time) error {
	sid, err := parseUUID(id)
	if err != nil {
		return err
	}
	if err := r.q.TouchSession(ctx, identitydb.TouchSessionParams{ID: sid, Now: ts(now), IdleExpiresAt: ts(idleExpiresAt)}); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

func (r repo) RevokeSessionByHash(ctx context.Context, tokenHash []byte) error {
	if err := r.q.RevokeSessionByHash(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke session by hash: %w", err)
	}
	return nil
}

func (r repo) RevokeSession(ctx context.Context, id string) error {
	sid, err := parseUUID(id)
	if err != nil {
		return err
	}
	if err := r.q.RevokeSession(ctx, sid); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (r repo) RevokeUserSessions(ctx context.Context, userID string) (int64, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return 0, err
	}
	n, err := r.q.RevokeUserSessions(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	return n, nil
}

func (r repo) RecordAudit(ctx context.Context, e usecases.AuditEvent) error {
	b, err := auditMetadata(e)
	if err != nil {
		return err
	}
	if err := r.q.RecordAudit(ctx, identitydb.RecordAuditParams{
		Action: e.Action, TargetType: text(e.TargetType), TargetID: text(e.TargetID),
		Metadata: b, IpPrefix: prefix(e.IPPrefix),
	}); err != nil {
		return fmt.Errorf("record audit %s: %w", e.Action, err)
	}
	return nil
}

func (r repo) RolesInUse(ctx context.Context) ([]string, error) {
	roles, err := r.q.RolesInUse(ctx)
	if err != nil {
		return nil, fmt.Errorf("roles in use: %w", err)
	}
	return roles, nil
}

func (r repo) RecordTenantAudit(ctx context.Context, tenantID string, e usecases.AuditEvent) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return err
	}
	meta, err := auditMetadata(e)
	if err != nil {
		return err
	}
	if err := r.q.RecordTenantAudit(ctx, identitydb.RecordTenantAuditParams{
		TenantID: tid, Action: e.Action, TargetType: text(e.TargetType), TargetID: text(e.TargetID),
		Metadata: meta, IpPrefix: prefix(e.IPPrefix),
	}); err != nil {
		return fmt.Errorf("record tenant audit %s: %w", e.Action, err)
	}
	return nil
}

func (r repo) UserByID(ctx context.Context, id string) (usecases.Profile, bool, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return usecases.Profile{}, false, err
	}
	row, err := r.q.UserByID(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.Profile{}, false, nil
	}
	if err != nil {
		return usecases.Profile{}, false, fmt.Errorf("user by id: %w", err)
	}
	return usecases.Profile{ID: row.ID.String(), Email: pii.Email(row.Email), DisplayName: row.DisplayName}, true, nil
}

func (r repo) MembershipsForUser(ctx context.Context) ([]usecases.TenantMembership, error) {
	rows, err := r.q.MembershipsForUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("memberships for user: %w", err)
	}
	out := make([]usecases.TenantMembership, 0, len(rows))
	for _, row := range rows {
		out = append(out, usecases.TenantMembership{
			TenantID: row.TenantID.String(), TenantSlug: row.TenantSlug, TenantName: row.TenantName,
			TenantStatus: row.TenantStatus, Role: row.Role,
		})
	}
	return out, nil
}

func (r repo) TenantByID(ctx context.Context, id string) (usecases.Tenant, bool, error) {
	tid, err := parseUUID(id)
	if err != nil {
		return usecases.Tenant{}, false, err
	}
	row, err := r.q.TenantByID(ctx, tid)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.Tenant{}, false, nil
	}
	if err != nil {
		return usecases.Tenant{}, false, fmt.Errorf("tenant by id: %w", err)
	}
	return usecases.Tenant{ID: row.ID.String(), Slug: row.Slug, Name: row.Name}, true, nil
}

func (r repo) IdentitySubject(ctx context.Context, userID, provider string) (string, bool, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return "", false, err
	}
	subject, err := r.q.IdentitySubject(ctx, identitydb.IdentitySubjectParams{UserID: uid, Provider: provider})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("identity subject: %w", err)
	}
	return subject, true, nil
}

func (r repo) MFAFactors(ctx context.Context) ([]usecases.MFAFactor, error) {
	rows, err := r.q.MFAFactorsForUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("mfa factors: %w", err)
	}
	out := make([]usecases.MFAFactor, 0, len(rows))
	for _, row := range rows {
		out = append(out, usecases.MFAFactor{
			ID: row.ID.String(), Provider: row.Provider, ProviderFactorID: row.ProviderFactorID,
			VerifiedAt: row.VerifiedAt.Time, CreatedAt: row.CreatedAt.Time,
		})
	}
	return out, nil
}

func (r repo) RegisterMFAFactor(ctx context.Context, tokenHash []byte, provider, providerFactorID, providerChallengeID string) (usecases.Challenge, error) {
	row, err := r.q.RegisterMFAFactor(ctx, identitydb.RegisterMFAFactorParams{
		TokenHash: tokenHash, Provider: provider, ProviderFactorID: providerFactorID, ProviderChallengeID: providerChallengeID,
	})
	if err != nil {
		return usecases.Challenge{}, fmt.Errorf("register mfa factor: %w", err)
	}
	return usecases.Challenge{ID: row.ChallengeID.String(), FactorID: row.FactorID.String(), ExpiresAt: row.ExpiresAt.Time}, nil
}

func (r repo) CreateMFAChallenge(ctx context.Context, tokenHash []byte, factorID, providerChallengeID string) (usecases.Challenge, error) {
	fid, err := parseUUID(factorID)
	if err != nil {
		return usecases.Challenge{}, err
	}
	row, err := r.q.CreateMFAChallenge(ctx, identitydb.CreateMFAChallengeParams{
		TokenHash: tokenHash, FactorID: fid, ProviderChallengeID: providerChallengeID,
	})
	if err != nil {
		return usecases.Challenge{}, fmt.Errorf("create mfa challenge: %w", err)
	}
	return usecases.Challenge{ID: row.ChallengeID.String(), FactorID: factorID, ExpiresAt: row.ExpiresAt.Time}, nil
}

func (r repo) ConsumeMFAChallenge(ctx context.Context, tokenHash []byte, challengeID string) (usecases.ConsumedChallenge, error) {
	cid, err := parseUUID(challengeID)
	if err != nil {
		return usecases.ConsumedChallenge{}, err
	}
	row, err := r.q.ConsumeMFAChallenge(ctx, identitydb.ConsumeMFAChallengeParams{TokenHash: tokenHash, ChallengeID: cid})
	if err != nil {
		return usecases.ConsumedChallenge{}, fmt.Errorf("consume mfa challenge: %w", err)
	}
	return usecases.ConsumedChallenge{
		ProviderChallengeID: row.ProviderChallengeID, FactorID: row.FactorID.String(), FactorVerified: row.FactorVerified,
	}, nil
}

func (r repo) MarkSessionMFA(ctx context.Context, tokenHash []byte, challengeID string, codeHash, newTokenHash []byte) (usecases.MarkedSession, error) {
	cid, err := parseUUID(challengeID)
	if err != nil {
		return usecases.MarkedSession{}, err
	}
	row, err := r.q.MarkSessionMFA(ctx, identitydb.MarkSessionMFAParams{
		TokenHash: tokenHash, ChallengeID: cid, CodeHash: codeHash, NewTokenHash: newTokenHash,
	})
	if err != nil {
		return usecases.MarkedSession{}, fmt.Errorf("mark session mfa: %w", err)
	}
	return usecases.MarkedSession{
		SessionID: row.SessionID.String(), MFAAuthTime: row.MfaAuthTime.Time, AbsoluteExpiresAt: row.AbsoluteExpiresAt.Time,
		FactorID: row.FactorID.String(), FirstVerification: row.FirstVerification,
	}, nil
}

func (r repo) RemoveMFAFactor(ctx context.Context, factorID string) (string, error) {
	fid, err := parseUUID(factorID)
	if err != nil {
		return "", err
	}
	row, err := r.q.RemoveMFAFactor(ctx, fid)
	if err != nil {
		return "", fmt.Errorf("remove mfa factor: %w", err)
	}
	return row.ProviderFactorID, nil
}

// tenantActive is the tenants.status of a tenant that may be used.
const tenantActive = "active"

func (r repo) Membership(ctx context.Context, tenantID string) (usecases.Membership, bool, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return usecases.Membership{}, false, err
	}
	row, err := r.q.AuthMembership(ctx, tid)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.Membership{}, false, nil
	}
	if err != nil {
		return usecases.Membership{}, false, fmt.Errorf("auth membership: %w", err)
	}
	return usecases.Membership{
		Role: row.Role, TenantActive: row.TenantStatus == tenantActive, UserDisabled: row.UserDisabled,
	}, true, nil
}

func auditMetadata(e usecases.AuditEvent) ([]byte, error) {
	meta := e.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("encode audit metadata: %w", err)
	}
	return b, nil
}

// uniqueViolation is SQLSTATE unique_violation.
const uniqueViolation = "23505"

// alreadyExists turns a unique violation into usecases.ErrAlreadyExists, keeping the cause.
func alreadyExists(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		e, _ := apperr.As(usecases.ErrAlreadyExists())
		return apperr.Wrap(err, e.Kind, e.Code, e.Message)
	}
	return err
}

func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("parse uuid: %w", err)
	}
	return u, nil
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func prefix(p netip.Prefix) *netip.Prefix {
	if !p.IsValid() {
		return nil
	}
	return &p
}
