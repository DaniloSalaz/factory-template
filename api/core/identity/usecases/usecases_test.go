package usecases_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"chaskicode.dev/saas/core/identity/adapters/fakeidp"
	"chaskicode.dev/saas/core/identity/domain"
	"chaskicode.dev/saas/core/identity/usecases"
	"chaskicode.dev/saas/core/kit/apperr"
	"chaskicode.dev/saas/core/kit/authctx"
	"chaskicode.dev/saas/core/kit/authz"
	"chaskicode.dev/saas/core/kit/pii"
)

// memStore is an in-memory usecases.Store. A transaction works on a copy of the state that
// replaces it only on success, so a failed transaction leaves no writes behind.
type memStore struct {
	now func() time.Time
	mu  sync.Mutex
	st  state
	// raceWinner, when set, makes the next CreateUser lose a race: the winner's user and
	// identity are committed as if by a concurrent login, and CreateUser fails with
	// usecases.ErrAlreadyExists, aborting its transaction.
	raceWinner *usecases.Authentication
	committed  *usecases.Authentication
}

type state struct {
	users      map[string]memUser
	identities map[string]string // provider|subject -> user id
	sessions   map[string]memSession
	audit      []memAudit
	nextID     int
	rolesInUse []string
	// memberships by tenant id, then user id.
	memberships map[string]map[string]usecases.Membership
	factors     map[string]memFactor
	challenges  map[string]memChallenge
}

type memFactor struct {
	user, providerID string
	verifiedAt       time.Time
	lastCode         string
	lastAt           time.Time
	createdAt        time.Time
}

type memChallenge struct {
	factor, session, providerID string
	expiresAt                   time.Time
	consumed, completed         bool
}

type memUser struct {
	email     pii.Email
	verified  bool
	disabled  bool
	lastLogin bool
}

type memSession struct {
	usecases.NewSession
	revoked bool
	mfaAt   time.Time
}

type memAudit struct {
	actor  string
	tenant string
	usecases.AuditEvent
}

func newMemStore() *memStore {
	return &memStore{now: time.Now, st: state{
		users: map[string]memUser{}, identities: map[string]string{}, sessions: map[string]memSession{},
		factors: map[string]memFactor{}, challenges: map[string]memChallenge{},
	}}
}

func (s state) clone() state {
	return state{
		users: maps.Clone(s.users), identities: maps.Clone(s.identities), sessions: maps.Clone(s.sessions),
		audit: slices.Clone(s.audit), nextID: s.nextID, rolesInUse: slices.Clone(s.rolesInUse),
		memberships: s.memberships, // read-only in these tests
		factors:     maps.Clone(s.factors), challenges: maps.Clone(s.challenges),
	}
}

func (m *memStore) Anon(ctx context.Context, fn func(context.Context, usecases.Repo) error) error {
	return m.tx(ctx, "", fn)
}

func (m *memStore) AsUser(ctx context.Context, userID string, fn func(context.Context, usecases.Repo) error) error {
	return m.tx(ctx, userID, fn)
}

func (m *memStore) InTenant(ctx context.Context, p authctx.Principal, fn func(context.Context, usecases.Repo) error) error {
	return m.tx(ctx, p.UserID(), func(ctx context.Context, r usecases.Repo) error {
		mr, _ := r.(*memRepo)
		mr.tenant = p.TenantID()
		return fn(ctx, mr)
	})
}

func (m *memStore) tx(ctx context.Context, actor string, fn func(context.Context, usecases.Repo) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &memRepo{st: m.st.clone(), actor: actor, store: m}
	if err := fn(ctx, r); err != nil {
		if w := m.committed; w != nil {
			m.committed = nil
			id := fmt.Sprintf("user-winner-%s", w.Subject)
			m.st.users[id] = memUser{email: w.Email, verified: true}
			m.st.identities["fake|"+w.Subject] = id
		}
		return err
	}
	m.st = r.st
	return nil
}

func (m *memStore) snapshot() state {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.clone()
}

type memRepo struct {
	st     state
	actor  string
	tenant string // set by InTenant
	store  *memStore
}

func (r *memRepo) id(prefix string) string {
	r.st.nextID++
	return fmt.Sprintf("%s-%d", prefix, r.st.nextID)
}

func (r *memRepo) UserByIdentity(_ context.Context, provider, subject string) (usecases.User, bool, error) {
	id, ok := r.st.identities[provider+"|"+subject]
	if !ok {
		return usecases.User{}, false, nil
	}
	u := r.st.users[id]
	return usecases.User{ID: id, Email: u.email, Disabled: u.disabled}, true, nil
}

func (r *memRepo) UserByEmail(_ context.Context, email pii.Email) (usecases.User, bool, error) {
	for id, u := range r.st.users {
		if strings.EqualFold(u.email.Reveal(), email.Reveal()) {
			return usecases.User{ID: id, Email: u.email, Disabled: u.disabled}, true, nil
		}
	}
	return usecases.User{}, false, nil
}

func (r *memRepo) CreateUser(_ context.Context, email pii.Email, verified bool, _ string) (string, error) {
	if w := r.store.raceWinner; w != nil {
		r.store.raceWinner, r.store.committed = nil, w
		return "", fmt.Errorf("create user: %w", usecases.ErrAlreadyExists())
	}
	id := r.id("user")
	r.st.users[id] = memUser{email: email, verified: verified}
	return id, nil
}

func (r *memRepo) CreateIdentity(_ context.Context, userID, provider, subject string, _ pii.Email) error {
	r.st.identities[provider+"|"+subject] = userID
	return nil
}

func (r *memRepo) UpdateUserEmail(_ context.Context, userID string, email pii.Email, verified bool) error {
	u := r.st.users[userID]
	u.email, u.verified = email, verified
	r.st.users[userID] = u
	return nil
}

func (r *memRepo) SetLastLogin(_ context.Context, userID string) error {
	u := r.st.users[userID]
	u.lastLogin = true
	r.st.users[userID] = u
	return nil
}

func (r *memRepo) CreateSession(_ context.Context, s usecases.NewSession) (string, error) {
	id := r.id("session")
	r.st.sessions[id] = memSession{NewSession: s}
	return id, nil
}

func (r *memRepo) SessionByHash(_ context.Context, hash []byte) (usecases.Session, bool, error) {
	for id, s := range r.st.sessions {
		if string(s.TokenHash) == string(hash) {
			return usecases.Session{
				ID: id, UserID: s.UserID, MFAAuthTime: s.mfaAt, ProviderSessionID: s.ProviderSessionID,
				State: domain.SessionState{
					LastSeenAt: s.Now, IdleExpiresAt: s.IdleExpiresAt, AbsoluteExpiresAt: s.AbsoluteExpiresAt,
					Revoked: s.revoked, UserDisabled: r.st.users[s.UserID].disabled,
				},
			}, true, nil
		}
	}
	return usecases.Session{}, false, nil
}

func (r *memRepo) TouchSession(_ context.Context, id string, now, idle time.Time) error {
	s := r.st.sessions[id]
	s.Now, s.IdleExpiresAt = now, idle
	r.st.sessions[id] = s
	return nil
}

func (r *memRepo) RevokeSessionByHash(_ context.Context, hash []byte) error {
	for id, s := range r.st.sessions {
		if string(s.TokenHash) == string(hash) {
			s.revoked = true
			r.st.sessions[id] = s
		}
	}
	return nil
}

func (r *memRepo) RevokeSession(_ context.Context, id string) error {
	s := r.st.sessions[id]
	s.revoked = true
	r.st.sessions[id] = s
	return nil
}

func (r *memRepo) RevokeUserSessions(_ context.Context, userID string) (int64, error) {
	var n int64
	for id, s := range r.st.sessions {
		if s.UserID == userID && !s.revoked {
			s.revoked = true
			r.st.sessions[id] = s
			n++
		}
	}
	return n, nil
}

func (r *memRepo) RecordAudit(_ context.Context, e usecases.AuditEvent) error {
	r.st.audit = append(r.st.audit, memAudit{actor: r.actor, AuditEvent: e})
	return nil
}

// ------------------------------------------------------------------------------------

const front = "https://front.test"

// The MFA methods follow the database functions of migration 00004, with their codes.

func (r *memRepo) IdentitySubject(_ context.Context, userID, provider string) (string, bool, error) {
	for key, uid := range r.st.identities {
		if p, subject, _ := strings.Cut(key, "|"); uid == userID && p == provider {
			return subject, true, nil
		}
	}
	return "", false, nil
}

func (r *memRepo) MFAFactors(context.Context) ([]usecases.MFAFactor, error) {
	var out []usecases.MFAFactor
	for _, id := range slices.Sorted(maps.Keys(r.st.factors)) {
		if f := r.st.factors[id]; f.user == r.actor {
			out = append(out, usecases.MFAFactor{ID: id, Provider: "fake", ProviderFactorID: f.providerID, VerifiedAt: f.verifiedAt, CreatedAt: f.createdAt})
		}
	}
	return out, nil
}

// liveSession is the session of tokenHash if it is the actor's and usable.
func (r *memRepo) liveSession(tokenHash []byte) (string, memSession, error) {
	for id, sess := range r.st.sessions {
		if string(sess.TokenHash) == string(tokenHash) && sess.UserID == r.actor && !sess.revoked {
			return id, sess, nil
		}
	}
	return "", memSession{}, apperr.Unauthorized("auth.session_invalid", "invalid session")
}

func (r *memRepo) addChallenge(sessionID, factorID, providerID string) usecases.Challenge {
	id := r.id("challenge")
	exp := r.store.now().Add(5 * time.Minute)
	r.st.challenges[id] = memChallenge{factor: factorID, session: sessionID, providerID: providerID, expiresAt: exp}
	return usecases.Challenge{ID: id, FactorID: factorID, ExpiresAt: exp}
}

func (r *memRepo) RegisterMFAFactor(_ context.Context, tokenHash []byte, _, providerFactorID, providerChallengeID string) (usecases.Challenge, error) {
	sid, _, err := r.liveSession(tokenHash)
	if err != nil {
		return usecases.Challenge{}, err
	}
	pending := 0
	for _, f := range r.st.factors {
		if f.user == r.actor && f.verifiedAt.IsZero() {
			pending++
		}
	}
	if pending >= 3 {
		return usecases.Challenge{}, apperr.Conflict("mfa.too_many_pending", "too many")
	}
	fid := r.id("factor")
	r.st.factors[fid] = memFactor{user: r.actor, providerID: providerFactorID, createdAt: r.store.now()}
	return r.addChallenge(sid, fid, providerChallengeID), nil
}

func (r *memRepo) CreateMFAChallenge(_ context.Context, tokenHash []byte, factorID, providerChallengeID string) (usecases.Challenge, error) {
	sid, _, err := r.liveSession(tokenHash)
	if err != nil {
		return usecases.Challenge{}, err
	}
	if f, ok := r.st.factors[factorID]; !ok || f.user != r.actor {
		return usecases.Challenge{}, apperr.NotFound("mfa.factor_not_found", "not found")
	}
	return r.addChallenge(sid, factorID, providerChallengeID), nil
}

func (r *memRepo) ConsumeMFAChallenge(_ context.Context, tokenHash []byte, challengeID string) (usecases.ConsumedChallenge, error) {
	sid, _, err := r.liveSession(tokenHash)
	if err != nil {
		return usecases.ConsumedChallenge{}, apperr.NotFound("mfa.challenge_not_found", "not found")
	}
	c, ok := r.st.challenges[challengeID]
	if !ok || c.session != sid {
		return usecases.ConsumedChallenge{}, apperr.NotFound("mfa.challenge_not_found", "not found")
	}
	if c.consumed {
		return usecases.ConsumedChallenge{}, apperr.Conflict("mfa.challenge_used", "used")
	}
	if !r.store.now().Before(c.expiresAt) {
		return usecases.ConsumedChallenge{}, apperr.Conflict("mfa.challenge_expired", "expired")
	}
	c.consumed = true
	r.st.challenges[challengeID] = c
	return usecases.ConsumedChallenge{ProviderChallengeID: c.providerID, FactorID: c.factor, FactorVerified: !r.st.factors[c.factor].verifiedAt.IsZero()}, nil
}

func (r *memRepo) MarkSessionMFA(_ context.Context, tokenHash []byte, challengeID string, codeHash, newTokenHash []byte) (usecases.MarkedSession, error) {
	c, ok := r.st.challenges[challengeID]
	if !ok || !c.consumed {
		return usecases.MarkedSession{}, apperr.NotFound("mfa.challenge_not_found", "not found")
	}
	if c.completed {
		return usecases.MarkedSession{}, apperr.Conflict("mfa.challenge_used", "used")
	}
	sid, sess, err := r.liveSession(tokenHash)
	if err != nil || sid != c.session {
		return usecases.MarkedSession{}, apperr.Unauthorized("auth.session_invalid", "invalid session")
	}
	now := r.store.now()
	f := r.st.factors[c.factor]
	if f.lastCode == string(codeHash) && now.Sub(f.lastAt) < 10*time.Minute {
		return usecases.MarkedSession{}, apperr.Conflict("mfa.code_reused", "reused")
	}
	first := f.verifiedAt.IsZero()
	if first {
		f.verifiedAt = now
	}
	f.lastCode, f.lastAt = string(codeHash), now
	r.st.factors[c.factor] = f
	c.completed = true
	r.st.challenges[challengeID] = c
	sess.revoked = true
	r.st.sessions[sid] = sess
	next := memSession{NewSession: sess.NewSession, mfaAt: now}
	next.TokenHash = newTokenHash
	newID := r.id("session")
	r.st.sessions[newID] = next
	return usecases.MarkedSession{SessionID: newID, MFAAuthTime: now, AbsoluteExpiresAt: sess.AbsoluteExpiresAt, FactorID: c.factor, FirstVerification: first}, nil
}

func (r *memRepo) RemoveMFAFactor(_ context.Context, factorID string) (string, error) {
	f, ok := r.st.factors[factorID]
	if !ok || f.user != r.actor {
		return "", apperr.NotFound("mfa.factor_not_found", "not found")
	}
	delete(r.st.factors, factorID)
	return f.providerID, nil
}

func (r *memRepo) RecordTenantAudit(_ context.Context, tenantID string, e usecases.AuditEvent) error {
	if r.tenant == "" || tenantID != r.tenant {
		return errors.New("record_audit: TN030/TN031")
	}
	r.st.audit = append(r.st.audit, memAudit{actor: r.actor, tenant: tenantID, AuditEvent: e})
	return nil
}

func (r *memRepo) RolesInUse(context.Context) ([]string, error) {
	return slices.Clone(r.st.rolesInUse), nil
}

func (r *memRepo) UserByID(_ context.Context, id string) (usecases.Profile, bool, error) {
	u, ok := r.st.users[id]
	if !ok {
		return usecases.Profile{}, false, nil
	}
	return usecases.Profile{ID: id, Email: u.email}, true, nil
}

func (r *memRepo) MembershipsForUser(context.Context) ([]usecases.TenantMembership, error) {
	var out []usecases.TenantMembership
	for _, tenant := range slices.Sorted(maps.Keys(r.st.memberships)) {
		if m, ok := r.st.memberships[tenant][r.actor]; ok {
			status := "active"
			if !m.TenantActive {
				status = "suspended"
			}
			out = append(out, usecases.TenantMembership{TenantID: tenant, TenantStatus: status, Role: m.Role})
		}
	}
	return out, nil
}

func (r *memRepo) TenantByID(_ context.Context, id string) (usecases.Tenant, bool, error) {
	// Like RLS: only the transaction's own tenant is visible.
	if r.tenant == "" || id != r.tenant {
		return usecases.Tenant{}, false, nil
	}
	return usecases.Tenant{ID: id, Slug: "slug-" + id[:4], Name: "Tenant " + id[:4]}, true, nil
}

func (r *memRepo) Membership(_ context.Context, tenantID string) (usecases.Membership, bool, error) {
	m, ok := r.st.memberships[tenantID][r.actor]
	return m, ok, nil
}

type fixture struct {
	idp      *fakeidp.Provider
	store    *memStore
	login    *usecases.Login
	sessions *usecases.Sessions
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	codec, err := domain.NewIntentCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{idp: fakeidp.New(), store: newMemStore(), now: time.Unix(1_800_000_000, 0)}
	clock := func() time.Time { return f.now }
	f.store.now = clock
	policy := domain.SessionPolicy{IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour}
	f.login = usecases.NewLogin(f.idp, f.store, codec, usecases.LoginConfig{FrontendOrigin: front, Policy: policy}, clock)
	f.sessions = usecases.NewSessions(f.idp, f.store, policy, clock)
	return f
}

func person(n int) usecases.Authentication {
	return usecases.Authentication{
		Subject: fmt.Sprintf("sub-%d", n), Email: pii.Email(fmt.Sprintf("p%d@example.test", n)), EmailVerified: true,
		SessionID: fmt.Sprintf("sid-%d", n), Method: "Password",
		AuthTime: time.Unix(1_799_999_990, 0),
	}
}

// complete runs Start and Complete for authn, with previous as the browser's token.
func (f *fixture) complete(t *testing.T, authn usecases.Authentication, previous string) (usecases.Completed, error) {
	t.Helper()
	started, err := f.login.Start("/x", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.idp.Authenticate(started.State, "code-"+started.State, authn)
	return f.login.Complete(t.Context(), usecases.Callback{
		State: started.State, IntentValue: started.Value, Code: "code-" + started.State, PreviousToken: previous,
	})
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if e, ok := apperr.As(err); !ok || e.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestComplete_NewUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	done, err := f.complete(t, person(1), "")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if done.ReturnTo != front+"/x" || !done.ExpiresAt.Equal(f.now.Add(24*time.Hour)) || !domain.WellFormed(done.Token) {
		t.Fatalf("completed = %+v", done)
	}
	st := f.store.snapshot()
	if len(st.users) != 1 || len(st.sessions) != 1 {
		t.Fatalf("state = %+v", st)
	}
	for _, s := range st.sessions {
		if string(s.TokenHash) != string(domain.HashToken(done.Token)) || !s.mfaAt.IsZero() || s.ProviderSessionID != "sid-1" {
			t.Fatalf("session = %+v", s)
		}
	}
	last := st.audit[len(st.audit)-1]
	if last.Action != usecases.AuditLogin || last.actor == "" {
		t.Fatalf("audit = %+v, want auth.login by the user", last)
	}
}

func TestComplete_RejectionsWriteOnlyTheAudit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(t *testing.T, f *fixture)
		authn  func() usecases.Authentication
		code   string
		action string
	}{
		{
			name:   "impersonation",
			authn:  func() usecases.Authentication { a := person(1); a.Impersonated = true; return a },
			code:   "auth.impersonation_refused",
			action: usecases.AuditLoginFailed,
		},
		{
			name:   "authentication older than the attempt",
			authn:  func() usecases.Authentication { a := person(1); a.AuthTime = time.Unix(1_800_000_000-61, 0); return a },
			code:   "auth.stale_authentication",
			action: usecases.AuditLoginFailed,
		},
		{
			name:   "unverified email",
			authn:  func() usecases.Authentication { a := person(1); a.EmailVerified = false; return a },
			code:   "auth.email_unverified",
			action: usecases.AuditLoginFailed,
		},
		{
			name: "email already used by another user",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				if _, err := f.complete(t, person(9), ""); err != nil {
					t.Fatal(err)
				}
			},
			authn:  func() usecases.Authentication { a := person(1); a.Email = person(9).Email; return a },
			code:   "auth.email_in_use",
			action: usecases.AuditEmailInUse,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			before := f.store.snapshot()
			_, err := f.complete(t, tc.authn(), "")
			wantCode(t, err, tc.code)

			after := f.store.snapshot()
			if len(after.users) != len(before.users) || len(after.identities) != len(before.identities) || len(after.sessions) != len(before.sessions) {
				t.Fatal("a rejected login wrote users, identities or sessions")
			}
			if len(after.audit) != len(before.audit)+1 || after.audit[len(after.audit)-1].Action != tc.action {
				t.Fatalf("audit = %+v, want one %s", after.audit[len(before.audit):], tc.action)
			}
		})
	}
}

// A concurrent first login of the same identity wins the race to create it: this one
// fails on the unique constraint, runs again and logs in as the winner's user.
func TestComplete_LosesTheRaceToCreateTheUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	who := person(1)
	f.store.raceWinner = &who

	if _, err := f.complete(t, who, ""); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	st := f.store.snapshot()
	if len(st.users) != 1 || len(st.sessions) != 1 {
		t.Fatalf("users = %d, sessions = %d; want the winner's user and one session", len(st.users), len(st.sessions))
	}
	for _, s := range st.sessions {
		if s.UserID != "user-winner-"+who.Subject {
			t.Fatalf("session of %s, want the winner's user", s.UserID)
		}
	}
}

func TestComplete_DisabledUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.complete(t, person(1), ""); err != nil {
		t.Fatal(err)
	}
	f.store.mu.Lock()
	for id, u := range f.store.st.users {
		u.disabled = true
		f.store.st.users[id] = u
	}
	f.store.mu.Unlock()

	before := f.store.snapshot()
	_, err := f.complete(t, person(1), "")
	wantCode(t, err, "auth.user_disabled")
	after := f.store.snapshot()
	if len(after.sessions) != len(before.sessions) || after.audit[len(after.audit)-1].Action != usecases.AuditLoginFailed {
		t.Fatalf("a disabled user must leave only auth.login_failed: %+v", after.audit[len(before.audit):])
	}
}

func TestComplete_EmailChange(t *testing.T) {
	t.Parallel()

	t.Run("takes the provider's verification", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if _, err := f.complete(t, person(1), ""); err != nil {
			t.Fatal(err)
		}
		f.store.mu.Lock()
		for id, u := range f.store.st.users {
			u.verified = false
			f.store.st.users[id] = u
		}
		f.store.mu.Unlock()

		changed := person(1)
		changed.Email = "new@example.test"
		if _, err := f.complete(t, changed, ""); err != nil {
			t.Fatal(err)
		}
		for _, u := range f.store.snapshot().users {
			if u.email != "new@example.test" || !u.verified {
				t.Fatalf("user = %+v, want the new email, verified", u)
			}
		}
	})

	t.Run("keeps the old one, unverified, if another user has it", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		for _, n := range []int{1, 2} {
			if _, err := f.complete(t, person(n), ""); err != nil {
				t.Fatal(err)
			}
		}
		changed := person(1)
		changed.Email = person(2).Email
		if _, err := f.complete(t, changed, ""); err != nil {
			t.Fatalf("the login itself must succeed: %v", err)
		}
		n := 0
		for _, u := range f.store.snapshot().users {
			switch u.email {
			case person(2).Email:
				n++
				if !u.verified {
					t.Fatal("the other user's email lost its verification")
				}
			case person(1).Email:
				if u.verified {
					t.Fatal("the old email kept its verification although the provider moved away from it")
				}
			default:
				t.Fatalf("unexpected email %s", u.email.Reveal())
			}
		}
		if n != 1 {
			t.Fatalf("%d users have the email", n)
		}
	})
}

// A login never gives the session MFA: only verifying a factor does (auth-y-tenancy §5.2), so
// no method of the provider, however it authenticated, can stand in for it.
func TestComplete_NeverGivesMFA(t *testing.T) {
	t.Parallel()

	for _, method := range []string{"Password", "Passkey", "SSO", "MagicAuth", ""} {
		t.Run("method "+method, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			a := person(1)
			a.Method = method
			if _, err := f.complete(t, a, ""); err != nil {
				t.Fatal(err)
			}
			for _, s := range f.store.snapshot().sessions {
				if !s.mfaAt.IsZero() {
					t.Fatalf("a login set mfa_auth_time = %v", s.mfaAt)
				}
			}
		})
	}
}

func TestComplete_TruncatesTheUserAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	started, err := f.login.Start("/", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.idp.Authenticate(started.State, "c", person(1))
	if _, err := f.login.Complete(t.Context(), usecases.Callback{
		State: started.State, IntentValue: started.Value, Code: "c", UserAgent: strings.Repeat("x", 10_000),
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.store.snapshot().sessions {
		if len(s.UserAgent) != domain.MaxUserAgentLength {
			t.Fatalf("stored user agent has %d characters, want %d", len(s.UserAgent), domain.MaxUserAgentLength)
		}
	}
}

func TestComplete_IntentAndProvider(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	started, err := f.login.Start("/", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.login.Complete(t.Context(), usecases.Callback{State: started.State, IntentValue: "", Code: "c"})
	wantCode(t, err, "auth.intent_invalid")

	_, err = f.login.Complete(t.Context(), usecases.Callback{State: started.State, IntentValue: started.Value, ProviderError: "access_denied"})
	wantCode(t, err, "auth.provider_error")

	// A code issued for another state does not match this intent's PKCE verifier.
	other, _ := f.login.Start("/", nil)
	f.idp.Authenticate(other.State, "stolen", person(1))
	_, err = f.login.Complete(t.Context(), usecases.Callback{State: started.State, IntentValue: started.Value, Code: "stolen"})
	wantCode(t, err, "auth.code_invalid")

	if _, err := f.login.Start("//evil.com", nil); !errors.Is(err, domain.ErrReturnToInvalid()) {
		t.Fatalf("Start(//evil.com) = %v", err)
	}
}

func TestSessions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first, err := f.complete(t, person(1), "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.sessions.Authenticate(t.Context(), first.Token); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if _, err := f.sessions.Authenticate(t.Context(), "not-a-token"); !errors.Is(err, domain.ErrSessionInvalid()) {
		t.Fatalf("malformed token: %v", err)
	}

	// A new login in the same browser revokes the previous session.
	second, err := f.complete(t, person(1), first.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.Authenticate(t.Context(), first.Token); !errors.Is(err, domain.ErrSessionInvalid()) {
		t.Fatalf("previous session after a new login: %v", err)
	}

	cur, err := f.sessions.Authenticate(t.Context(), second.Token)
	if err != nil {
		t.Fatal(err)
	}
	logoutURL, err := f.sessions.Logout(t.Context(), cur)
	if err != nil || !strings.Contains(logoutURL, "sid-1") {
		t.Fatalf("Logout = %q, %v", logoutURL, err)
	}
	if _, err := f.sessions.Authenticate(t.Context(), second.Token); !errors.Is(err, domain.ErrSessionInvalid()) {
		t.Fatalf("after logout: %v", err)
	}
	if a := f.store.snapshot().audit; a[len(a)-1].Action != usecases.AuditLogout || a[len(a)-1].actor != cur.UserID {
		t.Fatalf("audit = %+v", a[len(a)-1])
	}
}

func TestUnknownRolesInUse(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	store.st.rolesInUse = []string{"admin", "ghost", "owner"}
	catalog, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.CorePermissions()},
		authz.Role{Name: "admin"},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := usecases.UnknownRolesInUse(t.Context(), store, catalog)
	if err != nil || !slices.Equal(got, []string{"ghost"}) {
		t.Fatalf("UnknownRolesInUse = %v, %v; want [ghost]", got, err)
	}
}
