package app

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
)

// LoginConfig configures the login use cases.
type LoginConfig struct {
	FrontendOrigin string
	Policy         domain.SessionPolicy
	// ProviderEnforcesMFA: MFA is "Required" for every user at the provider.
	ProviderEnforcesMFA bool
}

// Login implements the login of the design (5.1): the redirect to the provider and the
// callback that turns its answer into a session.
type Login struct {
	idp   IdentityProvider
	store Store
	codec domain.IntentCodec
	cfg   LoginConfig
	now   func() time.Time
}

// NewLogin builds Login. now is the clock; nil means time.Now.
func NewLogin(idp IdentityProvider, store Store, codec domain.IntentCodec, cfg LoginConfig, now func() time.Time) *Login {
	if now == nil {
		now = time.Now
	}
	return &Login{idp: idp, store: store, codec: codec, cfg: cfg, now: now}
}

// Started is a login on its way to the provider: the browser stores Value under State
// and is sent to RedirectURL. Discard lists the states of older intents to delete.
type Started struct {
	RedirectURL string
	State       string
	Value       string
	Discard     []string
}

// Start validates return_to and creates a login intent (design, 5.1.1 and 5.1.2).
func (l *Login) Start(rawReturnTo string, stored []domain.StoredIntent) (Started, error) {
	returnTo, err := domain.ResolveReturnTo(l.cfg.FrontendOrigin, rawReturnTo)
	if err != nil {
		return Started{}, err
	}
	state, err := domain.NewRandom()
	if err != nil {
		return Started{}, fmt.Errorf("new state: %w", err)
	}
	verifier, err := domain.NewRandom()
	if err != nil {
		return Started{}, fmt.Errorf("new pkce verifier: %w", err)
	}
	now := l.now()
	value, err := l.codec.Seal(domain.Intent{
		State: state, Verifier: verifier, ReturnTo: returnTo, Purpose: domain.PurposeLogin, StartedAt: now,
	})
	if err != nil {
		return Started{}, fmt.Errorf("seal intent: %w", err)
	}
	redirect, err := l.idp.AuthorizationURL(state, domain.CodeChallenge(verifier))
	if err != nil {
		return Started{}, fmt.Errorf("authorization url: %w", err)
	}
	return Started{RedirectURL: redirect, State: state, Value: value, Discard: l.codec.IntentsToDiscard(stored, now)}, nil
}

// Callback is what the provider's redirect brings back, with the browser's context.
type Callback struct {
	State string
	// IntentValue is the stored intent for State; empty if the browser has none.
	IntentValue   string
	Code          string
	ProviderError string
	// PreviousToken is the browser's current session token, if any: it is revoked.
	PreviousToken string
	IPPrefix      netip.Prefix
	UserAgent     string
}

// Completed is a new session: the browser gets Token and goes to ReturnTo.
type Completed struct {
	Token     string
	ExpiresAt time.Time
	ReturnTo  string
}

// Complete runs the callback (design, 5.1.3 to 5.1.10). Nothing is written until every
// check has passed, except the audit event of a rejection.
func (l *Login) Complete(ctx context.Context, cb Callback) (Completed, error) {
	intent, err := l.codec.Open(cb.State, cb.IntentValue, l.now())
	if err != nil {
		return Completed{}, err
	}
	// The intent decides what the callback is for; only login exists.
	if intent.Purpose != domain.PurposeLogin {
		return Completed{}, domain.ErrIntentInvalid()
	}
	if cb.ProviderError != "" || cb.Code == "" {
		return Completed{}, apperr.BadRequest("auth.provider_error", "The identity provider did not complete the login.")
	}

	authn, err := l.idp.ExchangeCode(ctx, cb.Code, intent.Verifier)
	if err != nil {
		return Completed{}, fmt.Errorf("exchange code: %w", err)
	}

	// Before anything else: an impersonated login never reaches the user's account.
	if authn.Impersonated {
		return Completed{}, l.reject(ctx, cb, "impersonation", domain.ErrImpersonationRefused())
	}
	if !authn.EmailVerified {
		return Completed{}, l.reject(ctx, cb, "email_unverified", domain.ErrEmailUnverified())
	}
	mfaTime, _, err := domain.MFAAuthTime(authn.MFA, l.cfg.ProviderEnforcesMFA)
	if err != nil {
		return Completed{}, l.reject(ctx, cb, "mfa_missing", err)
	}

	userID, err := l.resolveUser(ctx, cb, authn)
	if err != nil {
		return Completed{}, err
	}

	token, err := domain.NewRandom()
	if err != nil {
		return Completed{}, fmt.Errorf("new session token: %w", err)
	}
	now := l.now()
	idle, absolute := l.cfg.Policy.Expiries(now)
	err = l.store.AsUser(ctx, userID, func(ctx context.Context, r Repo) error {
		// A login replaces the browser's session, whoever it belonged to, so a session
		// planted before the login is never upgraded (session fixation, 5.1.8).
		if domain.WellFormed(cb.PreviousToken) {
			if err := r.RevokeSessionByHash(ctx, domain.HashToken(cb.PreviousToken)); err != nil {
				return fmt.Errorf("revoke previous session: %w", err)
			}
		}
		sessionID, err := r.CreateSession(ctx, NewSession{
			TokenHash: domain.HashToken(token), UserID: userID,
			AuthTime: authn.MFA.AuthTime, MFAAuthTime: mfaTime,
			Now: now, IdleExpiresAt: idle, AbsoluteExpiresAt: absolute,
			IPPrefix: cb.IPPrefix, UserAgent: cb.UserAgent, ProviderSessionID: authn.SessionID,
		})
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		if err := r.SetLastLogin(ctx, userID); err != nil {
			return fmt.Errorf("set last login: %w", err)
		}
		return r.RecordAudit(ctx, AuditEvent{
			Action: AuditLogin, TargetType: "session", TargetID: sessionID, IPPrefix: cb.IPPrefix,
			Metadata: map[string]string{"method": authn.Method, "mfa": fmt.Sprint(!mfaTime.IsZero())},
		})
	})
	if err != nil {
		return Completed{}, fmt.Errorf("start session: %w", err)
	}
	return Completed{Token: token, ExpiresAt: absolute, ReturnTo: intent.ReturnTo}, nil
}

// resolveUser finds or creates the user of authn (design, 5.1.5 to 5.1.7): reads first,
// and writes only once the identity is known to be usable.
func (l *Login) resolveUser(ctx context.Context, cb Callback, authn Authentication) (string, error) {
	provider := l.idp.Name()
	var userID string
	var rejected error
	err := l.store.Anon(ctx, func(ctx context.Context, r Repo) error {
		user, found, err := r.UserByIdentity(ctx, provider, authn.Subject)
		if err != nil {
			return fmt.Errorf("find identity: %w", err)
		}
		if !found {
			// No linking by email: an email already in use is a conflict, never a match.
			_, taken, err := r.UserByEmail(ctx, authn.Email)
			if err != nil {
				return fmt.Errorf("find user by email: %w", err)
			}
			if taken {
				rejected = domain.ErrEmailInUse()
				return r.RecordAudit(ctx, AuditEvent{Action: AuditEmailInUse, IPPrefix: cb.IPPrefix,
					Metadata: map[string]string{"provider": provider}})
			}
		}
		if found && user.Disabled {
			rejected = domain.ErrUserDisabled()
			return r.RecordAudit(ctx, AuditEvent{Action: AuditLoginFailed, TargetType: "user", TargetID: user.ID,
				IPPrefix: cb.IPPrefix, Metadata: map[string]string{"reason": "user_disabled"}})
		}

		// Every check has passed: from here on, writes.
		if !found {
			userID, err = r.CreateUser(ctx, authn.Email, authn.EmailVerified, authn.DisplayName)
			if err != nil {
				return fmt.Errorf("create user: %w", err)
			}
			return r.CreateIdentity(ctx, userID, provider, authn.Subject, authn.Email)
		}
		userID = user.ID
		return l.syncEmail(ctx, r, user, authn)
	})
	if err != nil {
		return "", fmt.Errorf("resolve user: %w", err)
	}
	if rejected != nil {
		return "", rejected
	}
	return userID, nil
}

// syncEmail stores the provider's email if it changed and no other user has it, with
// the provider's verification of that email (issue #4).
func (l *Login) syncEmail(ctx context.Context, r Repo, user User, authn Authentication) error {
	if equalFoldEmail(user.Email.Reveal(), authn.Email.Reveal()) {
		return nil
	}
	other, taken, err := r.UserByEmail(ctx, authn.Email)
	if err != nil {
		return fmt.Errorf("find user by new email: %w", err)
	}
	if taken && other.ID != user.ID {
		return nil
	}
	if err := r.UpdateUserEmail(ctx, user.ID, authn.Email, authn.EmailVerified); err != nil {
		return fmt.Errorf("update email: %w", err)
	}
	return nil
}

// reject records a failed login that happened before any user was resolved, and returns err.
func (l *Login) reject(ctx context.Context, cb Callback, reason string, err error) error {
	event := AuditEvent{Action: AuditLoginFailed, IPPrefix: cb.IPPrefix, Metadata: map[string]string{"reason": reason}}
	auditErr := l.store.Anon(ctx, func(ctx context.Context, r Repo) error { return r.RecordAudit(ctx, event) })
	if auditErr != nil {
		return errors.Join(err, fmt.Errorf("record %s: %w", AuditLoginFailed, auditErr))
	}
	return err
}

// equalFoldEmail compares emails the way the citext column does: ignoring case.
func equalFoldEmail(a, b string) bool { return strings.EqualFold(a, b) }
