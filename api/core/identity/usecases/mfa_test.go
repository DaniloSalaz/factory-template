package usecases_test

import (
	"errors"
	"testing"
	"time"

	"chaskicode.dev/saas/core/identity/domain"
	"chaskicode.dev/saas/core/identity/usecases"
	"chaskicode.dev/saas/core/kit/apperr"
	"chaskicode.dev/saas/core/kit/authctx"
	"chaskicode.dev/saas/core/kit/authz"
	"chaskicode.dev/saas/core/kit/ratelimit"
)

// mfaFixture is a logged-in user with the MFA use cases over the fakes.
type mfaFixture struct {
	*fixture
	mfa   *usecases.MFA
	token string
	user  string
}

func newMFAFixture(t *testing.T, role string) *mfaFixture {
	t.Helper()
	f := newFixture(t)
	done, err := f.complete(t, person(1), "")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions(), RequiresMFA: true, MFAMaxAge: 12 * time.Hour},
		authz.Role{Name: "member", Permissions: []authctx.Permission{authctx.PermScopeTenant}},
	)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return f.now }
	m := &mfaFixture{
		fixture: f,
		mfa:     usecases.NewMFA(f.idp, f.idp.Name(), f.store, catalog, ratelimit.NewMemory(5, 15*time.Minute, clock), nil, clock),
		token:   done.Token,
	}
	m.user = m.current(t).UserID
	if role != "" {
		f.store.st.memberships = map[string]map[string]usecases.Membership{"tenant-1": {m.user: {Role: role, TenantActive: true}}}
	}
	return m
}

// current is the session of the fixture's token, as the session middleware reads it.
func (m *mfaFixture) current(t *testing.T) usecases.Current {
	t.Helper()
	cur, err := m.sessions.Authenticate(t.Context(), m.token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return cur
}

// enrollAndVerify enrolls a factor and verifies its first code; the fixture's token
// becomes the rotated one. It returns the factor id.
func (m *mfaFixture) enrollAndVerify(t *testing.T, code string) string {
	t.Helper()
	e, err := m.mfa.Enroll(t.Context(), m.current(t))
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	m.idp.SetCode(m.store.st.factors[e.Challenge.FactorID].providerID, code)
	rotated, err := m.mfa.Verify(t.Context(), m.current(t), e.Challenge.ID, code)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	m.token = rotated.Token
	return e.Challenge.FactorID
}

func (m *mfaFixture) audits(action string) int {
	n := 0
	for _, a := range m.store.snapshot().audit {
		if a.Action == action {
			n++
		}
	}
	return n
}

func TestMFA_EnrollAndVerifyRotatesTheSession(t *testing.T) {
	t.Parallel()
	m := newMFAFixture(t, "")
	before := m.token

	e, err := m.mfa.Enroll(t.Context(), m.current(t))
	if err != nil {
		t.Fatal(err)
	}
	if e.Secret.Reveal() == "" || e.QRCode.Reveal() == "" || e.Challenge.ExpiresAt.Sub(m.now) != 5*time.Minute {
		t.Fatalf("enrollment = %+v", e)
	}
	m.idp.SetCode(m.store.st.factors[e.Challenge.FactorID].providerID, "123456")
	rotated, err := m.mfa.Verify(t.Context(), m.current(t), e.Challenge.ID, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Token == before || rotated.MFAAuthTime.IsZero() {
		t.Fatalf("rotated = %+v", rotated)
	}
	if _, err := m.sessions.Authenticate(t.Context(), before); !errors.Is(err, domain.ErrSessionInvalid()) {
		t.Fatalf("the token before the verification: %v, want auth.session_invalid", err)
	}
	m.token = rotated.Token
	if cur := m.current(t); !cur.MFAAuthTime.Equal(rotated.MFAAuthTime) {
		t.Fatalf("the new session's MFA = %v", cur.MFAAuthTime)
	}
	if m.audits(usecases.AuditMFAEnrolled) != 1 || m.audits(usecases.AuditMFAVerified) != 1 {
		t.Fatalf("audit = %+v", m.store.snapshot().audit)
	}
}

func TestMFA_ChallengeIsSingleUseAndExpires(t *testing.T) {
	t.Parallel()
	m := newMFAFixture(t, "")
	factor := m.enrollAndVerify(t, "111111")
	m.idp.SetCode(m.store.st.factors[factor].providerID, "222222")

	ch, err := m.mfa.Challenge(t.Context(), m.current(t), factor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.mfa.Verify(t.Context(), m.current(t), ch.ID, "999999"); !errors.Is(err, domain.ErrMFACodeInvalid()) {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := m.mfa.Verify(t.Context(), m.current(t), ch.ID, "222222"); apperrCode(err) != "mfa.challenge_used" {
		t.Fatalf("second attempt on the same challenge: %v, want mfa.challenge_used", err)
	}
	if m.audits(usecases.AuditMFAVerifyFailed) != 2 {
		t.Fatalf("failures audited = %d, want 2", m.audits(usecases.AuditMFAVerifyFailed))
	}

	ch, err = m.mfa.Challenge(t.Context(), m.current(t), factor)
	if err != nil {
		t.Fatal(err)
	}
	m.now = m.now.Add(5 * time.Minute)
	if _, err := m.mfa.Verify(t.Context(), m.current(t), ch.ID, "222222"); apperrCode(err) != "mfa.challenge_expired" {
		t.Fatalf("after 5 minutes: %v, want mfa.challenge_expired", err)
	}
}

func TestMFA_ACodeIsNeverAcceptedTwice(t *testing.T) {
	t.Parallel()
	m := newMFAFixture(t, "")
	factor := m.enrollAndVerify(t, "333333")

	// The provider still accepts the same code (the fake, like WorkOS, has no replay
	// protection): only the app's record of the last code refuses it.
	ch, err := m.mfa.Challenge(t.Context(), m.current(t), factor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.mfa.Verify(t.Context(), m.current(t), ch.ID, "333333"); apperrCode(err) != "mfa.code_reused" {
		t.Fatalf("reused code: %v, want mfa.code_reused", err)
	}
}

func TestMFA_VerificationIsRateLimited(t *testing.T) {
	t.Parallel()
	m := newMFAFixture(t, "")
	cur := m.current(t)
	for i := range 5 {
		if _, err := m.mfa.Verify(t.Context(), cur, "no-such-challenge", "000000"); apperrCode(err) == "mfa.rate_limited" {
			t.Fatalf("attempt %d already limited", i+1)
		}
	}
	_, err := m.mfa.Verify(t.Context(), cur, "no-such-challenge", "000000")
	if !errors.Is(err, domain.ErrMFARateLimited(0)) || apperr.KindOf(err) != apperr.KindRateLimited {
		t.Fatalf("6th attempt: %v, want 429 mfa.rate_limited", err)
	}
	if m.audits(usecases.AuditMFAVerifyLocked) != 1 || m.audits(usecases.AuditMFAVerifyFailed) != 5 {
		t.Fatalf("locked = %d, failed = %d", m.audits(usecases.AuditMFAVerifyLocked), m.audits(usecases.AuditMFAVerifyFailed))
	}
}

// Once the user has a verified factor, changing factors needs an MFA from the last 10
// minutes: a stolen session alone cannot swap the factor.
func TestMFA_FactorChangesNeedRecentMFA(t *testing.T) {
	t.Parallel()
	m := newMFAFixture(t, "")
	factor := m.enrollAndVerify(t, "444444")

	m.now = m.now.Add(domain.FactorChangeMFAMaxAge + time.Second)
	cur := m.current(t)
	if _, err := m.mfa.Enroll(t.Context(), cur); !errors.Is(err, domain.ErrMFAVerificationRequired()) {
		t.Fatalf("enroll with an old MFA: %v, want auth.mfa_required", err)
	}
	if err := m.mfa.Remove(t.Context(), cur, factor); !errors.Is(err, domain.ErrMFAVerificationRequired()) {
		t.Fatalf("remove with an old MFA: %v, want auth.mfa_required", err)
	}

	m.now = m.now.Add(-2 * time.Second)
	if _, err := m.mfa.Enroll(t.Context(), m.current(t)); err != nil {
		t.Fatalf("enroll with an MFA under 10 minutes: %v", err)
	}
}

func TestMFA_LastFactorOfARoleThatRequiresMFA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role string
		want error
	}{
		{authz.RoleOwner, domain.ErrMFALastFactor()},
		{"member", nil},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			t.Parallel()
			m := newMFAFixture(t, tc.role)
			factor := m.enrollAndVerify(t, "555555")
			err := m.mfa.Remove(t.Context(), m.current(t), factor)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Remove = %v, want %v", err, tc.want)
			}
			if gone := m.idp.Factors() == 0; gone != (tc.want == nil) {
				t.Fatalf("factor removed at the provider = %v", gone)
			}
		})
	}
}

// A stolen session of a user who has a verified factor cannot verify a factor of its own:
// the first code of a new factor gives the session MFA.
func TestMFA_NewFactorNeedsRecentMFAWhenOneIsVerified(t *testing.T) {
	t.Parallel()
	m := newMFAFixture(t, "")
	m.enrollAndVerify(t, "666666")
	// Enrolled 8 minutes after the MFA (allowed), verified 3 minutes later: the
	// challenge is still alive, the MFA no longer recent.
	m.now = m.now.Add(8 * time.Minute)
	e, err := m.mfa.Enroll(t.Context(), m.current(t))
	if err != nil {
		t.Fatal(err)
	}
	m.now = m.now.Add(3 * time.Minute)
	m.idp.SetCode(m.store.st.factors[e.Challenge.FactorID].providerID, "777777")
	if _, err := m.mfa.Verify(t.Context(), m.current(t), e.Challenge.ID, "777777"); !errors.Is(err, domain.ErrMFAVerificationRequired()) {
		t.Fatalf("verifying a new factor with an old MFA: %v, want auth.mfa_required", err)
	}
}

func TestMFA_MalformedCodeNeverReachesTheProvider(t *testing.T) {
	t.Parallel()
	m := newMFAFixture(t, "")
	m.idp.FailNextMFACall() // a call would fail as unavailable instead
	if _, err := m.mfa.Verify(t.Context(), m.current(t), "x", "12ab56"); !errors.Is(err, domain.ErrMFACodeInvalid()) {
		t.Fatalf("Verify = %v, want mfa.code_invalid", err)
	}
}

func apperrCode(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Code
	}
	return ""
}
