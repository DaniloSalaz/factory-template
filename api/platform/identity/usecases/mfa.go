package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authz"
	"chaskicode.dev/saas/platform/kit/clientip"
	"chaskicode.dev/saas/platform/kit/pii"
	"chaskicode.dev/saas/platform/kit/ratelimit"
	"chaskicode.dev/saas/platform/kit/telemetry"
)

// MFA is the second factor the app verifies itself (design, 5.2; ADR 0002): enrolling a
// TOTP factor, challenging it, verifying a code, and removing a factor. A verified code
// replaces the session with a new one, with a new token and its MFA time set.
type MFA struct {
	idp      MFAProvider
	provider string
	store    Store
	catalog  *authz.Catalog
	limiter  ratelimit.Limiter
	metrics  *telemetry.Metrics
	now      func() time.Time
}

// NewMFA builds MFA. provider is the name stored with each factor (the identity
// provider's); limiter caps verification attempts and challenges per user and per
// session; metrics counts verifications (mfa.verify; nil records nothing); now is the
// clock (nil: time.Now).
func NewMFA(idp MFAProvider, provider string, store Store, catalog *authz.Catalog, limiter ratelimit.Limiter,
	metrics *telemetry.Metrics, now func() time.Time,
) *MFA {
	if now == nil {
		now = time.Now
	}
	return &MFA{idp: idp, provider: provider, store: store, catalog: catalog, limiter: limiter, metrics: metrics, now: now}
}

// Enrollment is a TOTP factor just enrolled, waiting for its first code.
type Enrollment struct {
	Challenge Challenge
	QRCode    pii.Secret
	URI       pii.Secret
	Secret    pii.Secret
}

// Rotated is the session that replaced the verified one: the browser gets Token.
type Rotated struct {
	Token       string
	ExpiresAt   time.Time
	MFAAuthTime time.Time
}

// Enroll creates a TOTP factor at the provider and its first challenge. If the user
// already has a verified factor, the session's MFA must be recent (FactorChangeMFAMaxAge).
func (m *MFA) Enroll(ctx context.Context, cur Current) (Enrollment, error) {
	if err := m.allow(ctx, cur, "start:user:"+cur.UserID); err != nil {
		return Enrollment{}, err
	}
	var subject string
	err := m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		factors, err := r.MFAFactors(ctx)
		if err != nil {
			return fmt.Errorf("mfa factors: %w", err)
		}
		if err := m.requireRecentMFA(cur, factors); err != nil {
			return err
		}
		s, found, err := r.IdentitySubject(ctx, cur.UserID, m.provider)
		if err != nil {
			return fmt.Errorf("identity subject: %w", err)
		}
		if !found {
			return apperr.Wrap(errors.New("the user has no identity at the provider"), apperr.KindInternal,
				"mfa.no_identity", "internal error")
		}
		subject = s
		return nil
	})
	if err != nil {
		return Enrollment{}, fmt.Errorf("enroll: %w", err)
	}

	enrolled, err := m.idp.EnrollTOTP(ctx, subject)
	if err != nil {
		return Enrollment{}, fmt.Errorf("enroll at the provider: %w", err)
	}
	var ch Challenge
	err = m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		var err error
		ch, err = r.RegisterMFAFactor(ctx, cur.tokenHash, m.provider, enrolled.FactorID, enrolled.ChallengeID)
		if err != nil {
			return err
		}
		return r.RecordAudit(ctx, m.event(ctx, AuditMFAEnrolled, "mfa_factor", ch.FactorID, nil))
	})
	if err != nil {
		// Best effort: the factor would only ever be an orphan at the provider.
		_ = m.idp.DeleteFactor(context.WithoutCancel(ctx), enrolled.FactorID)
		return Enrollment{}, fmt.Errorf("register factor: %w", err)
	}
	return Enrollment{Challenge: ch, QRCode: enrolled.QRCode, URI: enrolled.URI, Secret: enrolled.Secret}, nil
}

// Challenge creates a challenge of one of the user's factors, bound to this session. A
// factor still unverified can only be challenged under the same rule as enrolling.
func (m *MFA) Challenge(ctx context.Context, cur Current, factorID string) (Challenge, error) {
	if err := m.allow(ctx, cur, "start:user:"+cur.UserID); err != nil {
		return Challenge{}, err
	}
	var factor MFAFactor
	err := m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		factors, err := r.MFAFactors(ctx)
		if err != nil {
			return fmt.Errorf("mfa factors: %w", err)
		}
		f, ok := findFactor(factors, factorID)
		if !ok {
			return errFactorNotFound()
		}
		if f.VerifiedAt.IsZero() {
			if err := m.requireRecentMFA(cur, factors); err != nil {
				return err
			}
		}
		factor = f
		return nil
	})
	if err != nil {
		return Challenge{}, fmt.Errorf("challenge: %w", err)
	}

	providerChallengeID, err := m.idp.Challenge(ctx, factor.ProviderFactorID)
	if err != nil {
		return Challenge{}, fmt.Errorf("challenge at the provider: %w", err)
	}
	var ch Challenge
	err = m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		var err error
		ch, err = r.CreateMFAChallenge(ctx, cur.tokenHash, factor.ID, providerChallengeID)
		return err
	})
	if err != nil {
		return Challenge{}, fmt.Errorf("record challenge: %w", err)
	}
	return ch, nil
}

// Verify checks code against a challenge of this session. The challenge is used up by
// the attempt, right or wrong. Only when the provider answers valid, and the code is
// not the last one accepted with the factor, the session is replaced by a new one with
// its MFA verified now; the old token stops working. Failures and lockouts are audited.
func (m *MFA) Verify(ctx context.Context, cur Current, challengeID, code string) (Rotated, error) {
	out, err := m.verify(ctx, cur, challengeID, code)
	m.metrics.MFAVerify(ctx, telemetry.ResultOf(err))
	return out, err
}

func (m *MFA) verify(ctx context.Context, cur Current, challengeID, code string) (Rotated, error) {
	if err := m.allow(ctx, cur, "verify:user:"+cur.UserID, "verify:session:"+cur.ID); err != nil {
		return Rotated{}, err
	}
	if !domain.ValidTOTPCode(code) {
		m.recordFailure(ctx, cur, challengeID, "malformed_code")
		return Rotated{}, domain.ErrMFACodeInvalid()
	}

	var consumed ConsumedChallenge
	err := m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		var err error
		consumed, err = r.ConsumeMFAChallenge(ctx, cur.tokenHash, challengeID)
		return err
	})
	if err != nil {
		m.recordFailure(ctx, cur, challengeID, reason(err))
		return Rotated{}, fmt.Errorf("consume challenge: %w", err)
	}
	if !consumed.FactorVerified {
		// The first code of a new factor gives the session MFA, so a stolen session must
		// not be able to verify a factor of its own while the user has a real one.
		var factors []MFAFactor
		if err := m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
			var err error
			factors, err = r.MFAFactors(ctx)
			return err
		}); err != nil {
			return Rotated{}, fmt.Errorf("mfa factors: %w", err)
		}
		if err := m.requireRecentMFA(cur, factors); err != nil {
			m.recordFailure(ctx, cur, challengeID, "mfa_required")
			return Rotated{}, err
		}
	}

	valid, err := m.idp.Verify(ctx, consumed.ProviderChallengeID, code)
	if err != nil {
		return Rotated{}, fmt.Errorf("verify at the provider: %w", err)
	}
	if !valid {
		m.recordFailure(ctx, cur, challengeID, "invalid_code")
		return Rotated{}, domain.ErrMFACodeInvalid()
	}

	token, err := domain.NewRandom()
	if err != nil {
		return Rotated{}, fmt.Errorf("new session token: %w", err)
	}
	var marked MarkedSession
	err = m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		var err error
		marked, err = r.MarkSessionMFA(ctx, cur.tokenHash, challengeID, domain.HashCode(code), domain.HashToken(token))
		if err != nil {
			return err
		}
		return r.RecordAudit(ctx, m.event(ctx, AuditMFAVerified, "session", marked.SessionID, map[string]string{
			"factor_id": marked.FactorID, "first": fmt.Sprint(marked.FirstVerification), "replaced_session": cur.ID,
		}))
	})
	if err != nil {
		m.recordFailure(ctx, cur, challengeID, reason(err))
		return Rotated{}, fmt.Errorf("mark session: %w", err)
	}
	return Rotated{Token: token, ExpiresAt: marked.AbsoluteExpiresAt, MFAAuthTime: marked.MFAAuthTime}, nil
}

// Remove deletes one of the user's factors here and at the provider. If the user has a
// verified factor, the session's MFA must be recent; and the last verified factor
// cannot go while a role of the user, in an active tenant, requires MFA.
func (m *MFA) Remove(ctx context.Context, cur Current, factorID string) error {
	err := m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
		factors, err := r.MFAFactors(ctx)
		if err != nil {
			return fmt.Errorf("mfa factors: %w", err)
		}
		f, ok := findFactor(factors, factorID)
		if !ok {
			return errFactorNotFound()
		}
		if err := m.requireRecentMFA(cur, factors); err != nil {
			return err
		}
		if !f.VerifiedAt.IsZero() && countVerified(factors) == 1 {
			needed, err := m.roleRequiresMFA(ctx, r)
			if err != nil {
				return err
			}
			if needed {
				return domain.ErrMFALastFactor()
			}
		}
		providerFactorID, err := r.RemoveMFAFactor(ctx, f.ID)
		if err != nil {
			return err
		}
		// Inside the transaction: if the provider fails, the factor stays on both sides.
		return m.idp.DeleteFactor(ctx, providerFactorID)
	})
	if err != nil {
		return fmt.Errorf("remove factor: %w", err)
	}
	return nil
}

// requireRecentMFA is the rule of every factor change: free while the user has no
// verified factor, and only with a recent MFA once they do.
func (m *MFA) requireRecentMFA(cur Current, factors []MFAFactor) error {
	if !hasVerified(factors) || domain.MFASatisfied(cur.MFAAuthTime, domain.FactorChangeMFAMaxAge, m.now()) {
		return nil
	}
	return domain.ErrMFAVerificationRequired()
}

func (m *MFA) roleRequiresMFA(ctx context.Context, r Repo) (bool, error) {
	ms, err := r.MembershipsForUser(ctx)
	if err != nil {
		return false, fmt.Errorf("memberships: %w", err)
	}
	for _, ms := range ms {
		if role, ok := m.catalog.Role(ms.Role); ok && role.RequiresMFA && ms.TenantStatus == "active" {
			return true, nil
		}
	}
	return false, nil
}

// allow counts one attempt against every key; any key over its quota refuses it, and
// the refusal is audited.
func (m *MFA) allow(ctx context.Context, cur Current, keys ...string) error {
	for _, k := range keys {
		d, err := m.limiter.Allow(ctx, "mfa:"+k)
		if err != nil {
			return apperr.Wrap(err, apperr.KindUnavailable, "rate_limit.unavailable", "The service is temporarily unavailable.")
		}
		if !d.Allowed {
			_ = m.store.AsUser(ctx, cur.UserID, func(ctx context.Context, r Repo) error {
				return r.RecordAudit(ctx, m.event(ctx, AuditMFAVerifyLocked, "session", cur.ID, map[string]string{"limit": k}))
			})
			return domain.ErrMFARateLimited(d.RetryAfter)
		}
	}
	return nil
}

// recordFailure audits a failed verification in its own transaction: the attempt's
// transaction, if any, rolled back.
func (m *MFA) recordFailure(ctx context.Context, cur Current, challengeID, why string) {
	_ = m.store.AsUser(context.WithoutCancel(ctx), cur.UserID, func(ctx context.Context, r Repo) error {
		return r.RecordAudit(ctx, m.event(ctx, AuditMFAVerifyFailed, "mfa_challenge", challengeID,
			map[string]string{"reason": why, "session": cur.ID}))
	})
}

func (m *MFA) event(ctx context.Context, action, targetType, targetID string, meta map[string]string) AuditEvent {
	e := AuditEvent{Action: action, TargetType: targetType, TargetID: targetID, Metadata: meta}
	if p, ok := clientip.PrefixFrom(ctx); ok {
		e.IPPrefix = p
	}
	return e
}

// reason is the audit reason of a failed attempt: the stable code of an app error.
func reason(err error) string {
	if e, ok := apperr.As(err); ok && e.Kind != apperr.KindInternal {
		return e.Code
	}
	return "error"
}

func errFactorNotFound() error {
	return apperr.NotFound("mfa.factor_not_found", "MFA factor not found.")
}

func findFactor(factors []MFAFactor, id string) (MFAFactor, bool) {
	for _, f := range factors {
		if f.ID == id {
			return f, true
		}
	}
	return MFAFactor{}, false
}

func hasVerified(factors []MFAFactor) bool { return countVerified(factors) > 0 }

func countVerified(factors []MFAFactor) int {
	n := 0
	for _, f := range factors {
		if !f.VerifiedAt.IsZero() {
			n++
		}
	}
	return n
}
