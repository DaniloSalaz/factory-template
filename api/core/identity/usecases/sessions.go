package usecases

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"chaskicode.dev/saas/core/identity/domain"
	"chaskicode.dev/saas/core/kit/apperr"
)

// Sessions checks the session of each request (auth-y-tenancy §5.4.1) and ends sessions (auth-y-tenancy §5.5).
type Sessions struct {
	idp    IdentityProvider
	store  Store
	policy domain.SessionPolicy
	now    func() time.Time
}

// NewSessions builds Sessions. now is the clock; nil means time.Now.
func NewSessions(idp IdentityProvider, store Store, policy domain.SessionPolicy, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{idp: idp, store: store, policy: policy, now: now}
}

// Current is the session of a request.
type Current struct {
	ID          string
	UserID      string
	MFAAuthTime time.Time
	// providerSessionID stays inside the module: only logout needs it.
	providerSessionID string
	// tokenHash names the session to the MFA functions, which revalidate it.
	tokenHash []byte
}

// Authenticate returns the session of token, refreshing last_seen_at at most every
// TouchInterval. Any failure is the same auth.session_invalid.
func (s *Sessions) Authenticate(ctx context.Context, token string) (Current, error) {
	if !domain.WellFormed(token) {
		return Current{}, domain.ErrSessionInvalid()
	}
	now := s.now()
	var cur Current
	// An invalid session ends the transaction with its error; nothing was written.
	err := s.store.Anon(ctx, func(ctx context.Context, r Repo) error {
		sess, found, err := r.SessionByHash(ctx, domain.HashToken(token))
		if err != nil {
			return fmt.Errorf("find session: %w", err)
		}
		if !found {
			return domain.ErrSessionInvalid()
		}
		// Checked against the last_seen_at stored before this request.
		if err := s.policy.Check(sess.State, now); err != nil {
			return err
		}
		if domain.NeedsTouch(sess.State.LastSeenAt, now) {
			if err := r.TouchSession(ctx, sess.ID, now, now.Add(s.policy.IdleTTL)); err != nil {
				return fmt.Errorf("touch session: %w", err)
			}
		}
		cur = Current{
			ID: sess.ID, UserID: sess.UserID, MFAAuthTime: sess.MFAAuthTime,
			providerSessionID: sess.ProviderSessionID, tokenHash: domain.HashToken(token),
		}
		return nil
	})
	if err != nil {
		return Current{}, fmt.Errorf("authenticate session: %w", err)
	}
	return cur, nil
}

// Logout revokes the current session and returns the provider's logout URL, or "" if
// the session has no provider session.
func (s *Sessions) Logout(ctx context.Context, cur Current) (string, error) {
	err := s.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		if err := r.RevokeSession(ctx, cur.ID); err != nil {
			return fmt.Errorf("revoke session: %w", err)
		}
		return r.RecordAudit(ctx, AuditEvent{Action: AuditLogout, TargetType: "session", TargetID: cur.ID,
			Metadata: map[string]string{"scope": "session"}})
	})
	if err != nil {
		return "", fmt.Errorf("logout: %w", err)
	}
	return s.providerLogoutURL(cur)
}

// LogoutAll revokes every session of the current user, this one included.
func (s *Sessions) LogoutAll(ctx context.Context, cur Current) (string, error) {
	err := s.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		n, err := r.RevokeUserSessions(ctx, cur.UserID)
		if err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		return r.RecordAudit(ctx, AuditEvent{Action: AuditLogout, TargetType: "user", TargetID: cur.UserID,
			Metadata: map[string]string{"scope": "all", "sessions": strconv.FormatInt(n, 10)}})
	})
	if err != nil {
		return "", fmt.Errorf("logout all: %w", err)
	}
	return s.providerLogoutURL(cur)
}

func (s *Sessions) providerLogoutURL(cur Current) (string, error) {
	if cur.providerSessionID == "" {
		return "", nil
	}
	u, err := s.idp.LogoutURL(cur.providerSessionID)
	if err != nil {
		// The session is already revoked here; only the provider's one stays open.
		return "", apperr.Wrap(err, apperr.KindInternal, "auth.logout_url", "internal error")
	}
	return u, nil
}

type currentKey struct{}

// WithCurrent returns a context carrying the request's session.
func WithCurrent(ctx context.Context, cur Current) context.Context {
	return context.WithValue(ctx, currentKey{}, cur)
}

// CurrentFrom returns the request's session, if the session middleware set one.
func CurrentFrom(ctx context.Context) (Current, bool) {
	cur, ok := ctx.Value(currentKey{}).(Current)
	return cur, ok && cur.ID != ""
}
