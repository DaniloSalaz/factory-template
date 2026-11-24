//go:build integration

package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"chaskicode.dev/saas/core/identity/usecases"
	"chaskicode.dev/saas/core/kit/pii"
)

// Invitations over HTTP (auth-y-tenancy §5.6 and §11, migration 00006), with the real middlewares
// and database, and a sender that keeps the links instead of emailing them.

// captureSender records every link it is asked to send.
type captureSender struct {
	mu    sync.Mutex
	links map[string][]string // email -> links, oldest first
}

func (s *captureSender) Send(_ context.Context, msg usecases.InvitationMessage) error {
	email, link := msg.To, msg.Link
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.links == nil {
		s.links = map[string][]string{}
	}
	key := strings.ToLower(email.Reveal())
	s.links[key] = append(s.links[key], link.Reveal())
	return nil
}

// token returns the token of the last link sent to email.
func (e *env) token(email pii.Email) string {
	e.t.Helper()
	e.sender.mu.Lock()
	defer e.sender.mu.Unlock()
	links := e.sender.links[strings.ToLower(email.Reveal())]
	if len(links) == 0 {
		e.t.Fatalf("no link was sent to %s", email.Reveal())
	}
	last := links[len(links)-1]
	if !strings.HasPrefix(last, frontOrigin+"/invite#token=") {
		e.t.Fatalf("link = %q", last)
	}
	return strings.TrimPrefix(last, frontOrigin+"/invite#token=")
}

type invitationView struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	InvitedBy *string   `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
	Expired   bool      `json:"expired"`
}

// invite has b invite email to tenant with role and returns the invitation.
func (b *browser) invite(tenant string, email pii.Email, role string) invitationView {
	b.e.t.Helper()
	res := b.tenantJSON(tenant, http.MethodPost, "/invitations", map[string]string{"email": email.Reveal(), "role": role})
	if res.status != http.StatusCreated {
		b.e.t.Fatalf("invite: %d %s", res.status, res.body)
	}
	var v invitationView
	if err := json.Unmarshal(res.body, &v); err != nil {
		b.e.t.Fatal(err)
	}
	return v
}

// expire makes an invitation expired for both clocks: the test's, which the use cases
// read and which stands still, and the database's, which accept_invitation reads.
func (e *env) expire(id string) {
	e.t.Helper()
	e.exec(`UPDATE invitations SET expires_at = $2 WHERE id = $1`, id, e.clock.Now().Add(-time.Hour))
}

func (b *browser) accept(token string) response {
	b.e.t.Helper()
	return b.tenantJSON("", http.MethodPost, "/invitations/accept", map[string]string{"token": token})
}

// noSecretsLeaked fails if a token, or its hash, reached the logs or a response body.
func (e *env) noSecretsLeaked(token string, bodies ...response) {
	e.t.Helper()
	hash := hashHex(token)
	logs := e.logs.String()
	for _, s := range []string{token, hash} {
		if strings.Contains(logs, s) {
			e.t.Fatalf("%q reached the logs", s)
		}
		for _, b := range bodies {
			if strings.Contains(string(b.body), s) {
				e.t.Fatalf("%q reached a response: %s", s, b.body)
			}
		}
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE metadata::text LIKE '%' || $1 || '%' OR metadata::text LIKE '%' || $2 || '%'`, token, hash); n != 0 {
		e.t.Fatalf("%d audit events hold the token or its hash", n)
	}
}

func TestInvitations_InviteAndAccept(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	newcomer, nb := e.signedIn()

	res := s.ab.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": newcomer.Email.Reveal(), "role": "member"})
	if res.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", res.status, res.body)
	}
	var inv invitationView
	if err := json.Unmarshal(res.body, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Role != "member" || inv.InvitedBy == nil || *inv.InvitedBy != s.adminID || inv.Expired ||
		inv.ExpiresAt.Sub(e.clock.Now()) != 7*24*time.Hour {
		t.Fatalf("invitation = %+v", inv)
	}
	token := e.token(newcomer.Email)
	if got := e.scalar(`SELECT encode(token_hash, 'hex') FROM invitations WHERE id = $1`, inv.ID); got != hashHex(token) {
		t.Fatal("the stored hash is not the hash of the token sent")
	}

	accepted := nb.accept(token)
	if accepted.status != http.StatusOK || !strings.Contains(string(accepted.body), `"role":"member"`) ||
		!strings.Contains(string(accepted.body), s.tenant) {
		t.Fatalf("accept: %d %s", accepted.status, accepted.body)
	}
	if got := e.roleIn(s.tenant, newcomer); got != "member" {
		t.Fatalf("role = %s, want member", got)
	}
	if res := nb.tenantJSON(s.tenant, http.MethodGet, "/tenant", nil); res.status != http.StatusOK {
		t.Fatalf("the new member cannot enter: %d %s", res.status, res.body)
	}
	// One use only.
	wantProblem(t, nb.accept(token), http.StatusConflict, "invitation.already_accepted")

	uid := e.userID(newcomer)
	if got := e.auditOf(s.tenant, "invitation.created"); len(got) != 1 || !strings.HasPrefix(got[0], s.adminID+"|invitation|"+inv.ID+"|") {
		t.Fatalf("invitation.created = %v", got)
	}
	if got := e.auditOf(s.tenant, "invitation.accepted"); len(got) != 1 || !strings.HasPrefix(got[0], uid+"|invitation|"+inv.ID+"|") {
		t.Fatalf("invitation.accepted = %v", got)
	}
	if got := e.auditOf(s.tenant, "membership.created"); len(got) != 1 || !strings.HasPrefix(got[0], uid+"|user|"+uid+"|") {
		t.Fatalf("membership.created = %v", got)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND metadata::text ILIKE '%' || $2 || '%'`, s.tenant, newcomer.Email.Reveal()); n != 0 {
		t.Fatalf("%d audit events hold the invitee's email", n)
	}
	e.noSecretsLeaked(token, res, accepted)
}

func TestInvitations_AcceptIsRefused(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	invited := func(prepare func(inv invitationView, who usecases.Authentication)) (usecases.Authentication, *browser, string) {
		who, b := e.signedIn()
		inv := s.ob.invite(s.tenant, who.Email, "member")
		if prepare != nil {
			prepare(inv, who)
		}
		return who, b, e.token(who.Email)
	}

	t.Run("another user's invitation", func(t *testing.T) {
		_, _, token := invited(nil)
		_, other := e.signedIn()
		wantProblem(t, other.accept(token), http.StatusForbidden, "invitation.email_mismatch")
	})
	t.Run("an unverified email", func(t *testing.T) {
		_, b, token := invited(func(_ invitationView, who usecases.Authentication) {
			e.exec(`UPDATE users SET email_verified = false WHERE id = $1`, e.userID(who))
		})
		wantProblem(t, b.accept(token), http.StatusForbidden, "invitation.email_mismatch")
	})
	t.Run("expired", func(t *testing.T) {
		_, b, token := invited(func(inv invitationView, _ usecases.Authentication) {
			e.expire(inv.ID)
		})
		wantProblem(t, b.accept(token), http.StatusConflict, "invitation.expired")
	})
	t.Run("revoked", func(t *testing.T) {
		_, b, token := invited(func(inv invitationView, _ usecases.Authentication) {
			if res := s.ob.tenantJSON(s.tenant, http.MethodDelete, "/invitations/"+inv.ID, nil); res.status != http.StatusNoContent {
				t.Fatalf("revoke: %d %s", res.status, res.body)
			}
		})
		wantProblem(t, b.accept(token), http.StatusConflict, "invitation.revoked")
	})
	t.Run("suspended tenant", func(t *testing.T) {
		other := e.tenant()
		e.member(s.owner, other, "owner")
		who, b := e.signedIn()
		s.ob.invite(other, who.Email, "member")
		e.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, other)
		wantProblem(t, b.accept(e.token(who.Email)), http.StatusForbidden, "tenant.not_active")
	})
	t.Run("already a member", func(t *testing.T) {
		who, b, token := invited(nil)
		e.member(who, s.tenant, "member")
		wantProblem(t, b.accept(token), http.StatusConflict, "invitation.already_member")
	})
	t.Run("an unknown or malformed token", func(t *testing.T) {
		_, b := e.signedIn()
		wantProblem(t, b.accept(strings.Repeat("A", 43)), http.StatusNotFound, "invitation.not_found")
		wantProblem(t, b.accept("short"), http.StatusNotFound, "invitation.not_found")
	})
	t.Run("without a session", func(t *testing.T) {
		wantProblem(t, e.browser().accept("x"), http.StatusUnauthorized, "auth.session_invalid")
	})
}

func TestInvitations_CreateRules(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	fresh := func() pii.Email { return e.person().Email }

	wantProblem(t, s.ab.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": fresh().Reveal(), "role": "owner"}),
		http.StatusForbidden, "invitation.role_not_offerable")
	wantProblem(t, s.mb.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": fresh().Reveal(), "role": "member"}),
		http.StatusForbidden, "authz.denied")
	wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": fresh().Reveal(), "role": "ghost-role"}),
		http.StatusUnprocessableEntity, "invitation.unknown_role")
	wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": "not an email", "role": "member"}),
		http.StatusUnprocessableEntity, "invitation.email_invalid")
	wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": fresh().Reveal(), "role": "member", "invited_by": s.adminID}),
		http.StatusBadRequest, "unknown_field")
	wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": strings.ToUpper(s.admin.Email.Reveal()), "role": "member"}),
		http.StatusConflict, "invitation.already_member")

	// An owner offers owner.
	s.ob.invite(s.tenant, fresh(), "owner")

	// A pending invitation blocks another to the same email, in any case; once expired,
	// inviting again revokes it and creates a new one.
	email := fresh()
	first := s.ab.invite(s.tenant, email, "member")
	wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodPost, "/invitations", map[string]string{"email": strings.ToUpper(email.Reveal()), "role": "admin"}),
		http.StatusConflict, "invitation.pending")
	e.expire(first.ID)
	second := s.ab.invite(s.tenant, email, "admin")
	if second.ID == first.ID || e.scalar(`SELECT (revoked_at IS NOT NULL)::text FROM invitations WHERE id = $1`, first.ID) != "true" {
		t.Fatal("the expired invitation was not replaced")
	}
	if got := e.auditOf(s.tenant, "invitation.revoked"); len(got) != 1 || !strings.Contains(got[0], `"reason": "expired_replaced"`) {
		t.Fatalf("invitation.revoked = %v", got)
	}
}

func TestInvitations_ResendRevokeAndList(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	who, b := e.signedIn()
	inv := s.ab.invite(s.tenant, who.Email, "member")
	oldToken := e.token(who.Email)

	ownerInvite := s.ob.invite(s.tenant, e.person().Email, "owner")
	wantProblem(t, s.ab.tenantJSON(s.tenant, http.MethodPost, "/invitations/"+ownerInvite.ID+"/resend", nil), http.StatusForbidden, "invitation.role_not_offerable")
	wantProblem(t, s.ab.tenantJSON(s.tenant, http.MethodDelete, "/invitations/"+ownerInvite.ID, nil), http.StatusForbidden, "invitation.role_not_offerable")

	// Expired, it shows as such, and resending renews it with a new token.
	e.expire(inv.ID)
	res := s.ob.tenantJSON(s.tenant, http.MethodGet, "/invitations?limit=100", nil)
	if res.status != http.StatusOK || !strings.Contains(string(res.body), `"expired":true`) || !strings.Contains(string(res.body), inv.ID) {
		t.Fatalf("list: %d %s", res.status, res.body)
	}
	resent := s.ob.tenantJSON(s.tenant, http.MethodPost, "/invitations/"+inv.ID+"/resend", nil)
	if resent.status != http.StatusOK || !strings.Contains(string(resent.body), `"expired":false`) {
		t.Fatalf("resend: %d %s", resent.status, resent.body)
	}
	newToken := e.token(who.Email)
	if newToken == oldToken {
		t.Fatal("resending kept the token")
	}
	wantProblem(t, b.accept(oldToken), http.StatusNotFound, "invitation.not_found")
	if got := e.auditOf(s.tenant, "invitation.resent"); len(got) != 1 || !strings.HasPrefix(got[0], s.ownerID+"|invitation|"+inv.ID+"|") ||
		!strings.Contains(got[0], `"invited_by": "`+s.adminID+`"`) {
		t.Fatalf("invitation.resent = %v", got)
	}

	// Revoked: gone from the list, its link refused, and it cannot be revoked again.
	if res := s.ab.tenantJSON(s.tenant, http.MethodDelete, "/invitations/"+inv.ID, nil); res.status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", res.status, res.body)
	}
	wantProblem(t, b.accept(newToken), http.StatusConflict, "invitation.revoked")
	wantProblem(t, s.ab.tenantJSON(s.tenant, http.MethodDelete, "/invitations/"+inv.ID, nil), http.StatusConflict, "invitation.revoked")
	if res := s.ob.tenantJSON(s.tenant, http.MethodGet, "/invitations?limit=100", nil); strings.Contains(string(res.body), inv.ID) {
		t.Fatalf("a revoked invitation is listed: %s", res.body)
	}
	got := e.auditOf(s.tenant, "invitation.revoked")
	if len(got) != 1 || !strings.HasPrefix(got[0], s.adminID+"|invitation|"+inv.ID+"|") || !strings.Contains(got[0], `"reason": "revoked"`) {
		t.Fatalf("invitation.revoked = %v", got)
	}

	wantProblem(t, s.mb.tenantJSON(s.tenant, http.MethodGet, "/invitations", nil), http.StatusForbidden, "authz.denied")
	e.noSecretsLeaked(oldToken, resent, res)
	e.noSecretsLeaked(newToken, resent, res)
}

// With tenant A's context, an invitation of tenant B does not exist.
func TestInvitations_Isolation(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	other := e.tenant()
	_, ownerB := e.signedIn()
	e.exec(`INSERT INTO memberships (tenant_id, user_id, role) SELECT $1, user_id, 'owner' FROM sessions WHERE token_hash = decode($2, 'hex')`,
		other, hashHex(ownerB.sessionToken()))
	invB := ownerB.invite(other, e.person().Email, "member")

	wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodPost, "/invitations/"+invB.ID+"/resend", nil), http.StatusNotFound, "invitation.not_found")
	wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodDelete, "/invitations/"+invB.ID, nil), http.StatusNotFound, "invitation.not_found")
	if res := s.ob.tenantJSON(s.tenant, http.MethodGet, "/invitations?limit=100", nil); strings.Contains(string(res.body), invB.ID) {
		t.Fatalf("A lists B's invitation: %s", res.body)
	}
	if e.scalar(`SELECT (revoked_at IS NULL)::text FROM invitations WHERE id = $1`, invB.ID) != "true" {
		t.Fatal("B's invitation changed")
	}
}

// Demoting, removing or the departure of whoever invited revokes their pending
// invitations, in the same transaction (auth-y-tenancy §5.6.4).
func TestInvitations_RevokedWhenTheInviterLosesTheRight(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	pendingOf := func(userID string) int {
		return e.count(`SELECT count(*) FROM invitations WHERE tenant_id = $1 AND invited_by = $2 AND revoked_at IS NULL`, s.tenant, userID)
	}

	s.ab.invite(s.tenant, e.person().Email, "member")
	s.ab.invite(s.tenant, e.person().Email, "admin")
	s.a2b.invite(s.tenant, e.person().Email, "member")
	s.o2b.invite(s.tenant, e.person().Email, "admin")
	ownersInvite := s.ob.invite(s.tenant, e.person().Email, "admin")

	if res := s.ob.tenantJSON(s.tenant, http.MethodPatch, "/tenant/members/"+s.adminID, role("member")); res.status != http.StatusOK {
		t.Fatalf("demote: %d %s", res.status, res.body)
	}
	if res := s.ob.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/"+s.admin2ID, nil); res.status != http.StatusNoContent {
		t.Fatalf("remove: %d %s", res.status, res.body)
	}
	if res := s.o2b.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/me", nil); res.status != http.StatusNoContent {
		t.Fatalf("leave: %d %s", res.status, res.body)
	}
	for who, id := range map[string]string{"demoted": s.adminID, "removed": s.admin2ID, "left": s.owner2ID} {
		if n := pendingOf(id); n != 0 {
			t.Errorf("%s inviter still backs %d pending invitations", who, n)
		}
	}
	if e.scalar(`SELECT (revoked_at IS NULL)::text FROM invitations WHERE id = $1`, ownersInvite.ID) != "true" {
		t.Fatal("an invitation of someone else was revoked")
	}
	reasons := map[string]int{}
	for _, ev := range e.auditOf(s.tenant, "invitation.revoked") {
		for _, r := range []string{"inviter_role_changed", "inviter_removed", "inviter_left"} {
			if strings.Contains(ev, `"reason": "`+r+`"`) {
				reasons[r]++
			}
		}
	}
	if reasons["inviter_role_changed"] != 2 || reasons["inviter_removed"] != 1 || reasons["inviter_left"] != 1 {
		t.Fatalf("revocations by reason = %v", reasons)
	}
}

// Fifty times: the invitee accepts the same token from two browsers at once. One
// creates the membership; the other is refused with 409.
func TestInvitations_ConcurrentAcceptsCreateOneMembership(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()

	for i := range 50 {
		who, first := e.signedIn()
		second := e.browser()
		second.login(who)
		s.ob.invite(s.tenant, who.Email, "member")
		token := e.token(who.Email)

		var wg sync.WaitGroup
		results := make([]response, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for j, b := range []*browser{first, second} {
			wg.Go(func() {
				<-start
				results[j], errs[j] = b.send("", http.MethodPost, "/invitations/accept", map[string]string{"token": token})
			})
		}
		close(start)
		wg.Wait()
		ok := 0
		for j, res := range results {
			if errs[j] != nil {
				t.Fatal(errs[j])
			}
			code := codeOf(res)
			switch {
			case res.status == http.StatusOK:
				ok++
			case res.status == http.StatusConflict && (code == "invitation.already_accepted" || code == "invitation.already_member"):
			default:
				t.Fatalf("round %d: %d %s", i, res.status, res.body)
			}
		}
		if n := e.count(`SELECT count(*) FROM memberships m JOIN user_identities u ON u.user_id = m.user_id
			WHERE m.tenant_id = $1 AND u.subject = $2`, s.tenant, who.Subject); ok != 1 || n != 1 {
			t.Fatalf("round %d: %d accepted, %d memberships; want 1 and 1", i, ok, n)
		}
	}
}

func TestInvitations_AcceptIsRateLimited(t *testing.T) {
	e := newEnv(t, options{acceptLimit: 3})
	_, b := e.signedIn()
	for range 3 {
		wantProblem(t, b.accept(strings.Repeat("A", 43)), http.StatusNotFound, "invitation.not_found")
	}
	res := b.accept(strings.Repeat("A", 43))
	wantProblem(t, res, http.StatusTooManyRequests, "rate_limit.exceeded")
	wantRetryAfter(t, res, 15*time.Minute)
}

// The only way to hold a session with an unverified email (auth-y-tenancy §5.1.7): a later login
// brings, as verified, an email another user already has, so the user keeps the old one
// unverified. An invitation to that old email is then refused: the provider no longer
// vouches for it. A login whose email is unverified gets no session at all
// (TestLogin_UnverifiedEmailIsRejected).
func TestInvitations_AnEmailTheLoginUnverifiedCannotAccept(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	who, other := e.person(), e.person()
	e.browser().login(other)
	b := e.browser()
	b.login(who)
	s.ob.invite(s.tenant, who.Email, "member")

	moved := who
	moved.Email = other.Email
	b.login(moved)
	if got := e.scalar(`SELECT email_verified::text FROM users WHERE id = $1`, e.userID(who)); got != "false" {
		t.Fatalf("email_verified = %s, want false", got)
	}
	wantProblem(t, b.accept(e.token(who.Email)), http.StatusForbidden, "invitation.email_mismatch")
}
