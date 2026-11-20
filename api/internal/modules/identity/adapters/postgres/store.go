// Package postgres implements the identity module's Store with sqlc, inside the
// transactions of platform/db.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	identitydb "github.com/DaniloSalaz/factory-template/api/gen/sqlc/identity"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// Store implements app.Store.
type Store struct {
	db *db.DB
}

var _ app.Store = (*Store)(nil)

// NewStore builds a Store over a database that connects as app_runtime.
func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

// Anon implements app.Store with db.WithAnonTx.
func (s *Store) Anon(ctx context.Context, fn func(context.Context, app.Repo) error) error {
	err := s.db.WithAnonTx(ctx, func(ctx context.Context, q db.Querier) error {
		return fn(ctx, repo{q: identitydb.New(q)})
	})
	return db.MapError(err)
}

// AsUser implements app.Store with db.WithUserTx.
func (s *Store) AsUser(ctx context.Context, userID string, fn func(context.Context, app.Repo) error) error {
	if u, ok := authctx.UserFrom(ctx); !ok || u.ID() != userID {
		u, err := authctx.NewUser(userID, time.Time{})
		if err != nil {
			return fmt.Errorf("transaction user: %w", err)
		}
		ctx = authctx.WithUser(ctx, u)
	}
	err := s.db.WithUserTx(ctx, func(ctx context.Context, q db.Querier) error {
		return fn(ctx, repo{q: identitydb.New(q)})
	})
	return db.MapError(err)
}

type repo struct {
	q *identitydb.Queries
}

func (r repo) UserByIdentity(ctx context.Context, provider, subject string) (app.User, bool, error) {
	row, err := r.q.UserByIdentity(ctx, identitydb.UserByIdentityParams{Provider: provider, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.User{}, false, nil
	}
	if err != nil {
		return app.User{}, false, fmt.Errorf("user by identity: %w", err)
	}
	return app.User{ID: row.ID.String(), Email: pii.Email(row.Email), Disabled: row.Disabled}, true, nil
}

func (r repo) UserByEmail(ctx context.Context, email pii.Email) (app.User, bool, error) {
	row, err := r.q.UserByEmail(ctx, email.Reveal())
	if errors.Is(err, pgx.ErrNoRows) {
		return app.User{}, false, nil
	}
	if err != nil {
		return app.User{}, false, fmt.Errorf("user by email: %w", err)
	}
	return app.User{ID: row.ID.String(), Email: pii.Email(row.Email), Disabled: row.Disabled}, true, nil
}

func (r repo) CreateUser(ctx context.Context, email pii.Email, emailVerified bool, displayName string) (string, error) {
	id, err := r.q.CreateUser(ctx, identitydb.CreateUserParams{
		Email: email.Reveal(), EmailVerified: emailVerified, DisplayName: displayName,
	})
	if err != nil {
		return "", fmt.Errorf("create user: %w", err)
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
		return fmt.Errorf("create identity: %w", err)
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

func (r repo) CreateSession(ctx context.Context, s app.NewSession) (string, error) {
	uid, err := parseUUID(s.UserID)
	if err != nil {
		return "", err
	}
	id, err := r.q.CreateSession(ctx, identitydb.CreateSessionParams{
		TokenHash: s.TokenHash, UserID: uid,
		AuthTime: ts(s.AuthTime), MfaAuthTime: ts(s.MFAAuthTime), Now: ts(s.Now),
		IdleExpiresAt: ts(s.IdleExpiresAt), AbsoluteExpiresAt: ts(s.AbsoluteExpiresAt),
		IpPrefix: prefix(s.IPPrefix), UserAgent: text(s.UserAgent), ProviderSessionID: text(s.ProviderSessionID),
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return id.String(), nil
}

func (r repo) SessionByHash(ctx context.Context, tokenHash []byte) (app.Session, bool, error) {
	row, err := r.q.SessionByHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Session{}, false, nil
	}
	if err != nil {
		return app.Session{}, false, fmt.Errorf("session by hash: %w", err)
	}
	return app.Session{
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

func (r repo) RecordAudit(ctx context.Context, e app.AuditEvent) error {
	meta := e.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	if err := r.q.RecordAudit(ctx, identitydb.RecordAuditParams{
		Action: e.Action, TargetType: text(e.TargetType), TargetID: text(e.TargetID),
		Metadata: b, IpPrefix: prefix(e.IPPrefix),
	}); err != nil {
		return fmt.Errorf("record audit %s: %w", e.Action, err)
	}
	return nil
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
