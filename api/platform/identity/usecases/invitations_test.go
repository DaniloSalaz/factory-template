package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"chaskicode.dev/saas/modules/identity/app"
	"chaskicode.dev/saas/modules/identity/domain"
	"chaskicode.dev/saas/platform/apperr"
	"chaskicode.dev/saas/platform/authctx"
	"chaskicode.dev/saas/platform/authz"
	"chaskicode.dev/saas/platform/pii"
	"chaskicode.dev/saas/platform/ratelimit"
)

// The invitation side of memTenants: invitations by id with their token hashes, the
// member emails, and the share locks taken.

type memInvitation struct {
	app.Invitation
	tokenHash []byte
}

func (s *memTenants) invitationsMap() map[string]*memInvitation {
	if s.invitations == nil {
		s.invitations = map[string]*memInvitation{}
	}
	return s.invitations
}

func (r memTenantRepo) TenantByID(_ context.Context, id string) (app.Tenant, bool, error) {
	return app.Tenant{ID: id, Slug: "acme", Name: "Acme"}, true, nil
}

func (r memTenantRepo) ShareLockTenant(_ context.Context, tenantID string) error {
	r.s.calls = append(r.s.calls, "share-lock "+tenantID)
	if r.s.onShareLock != nil {
		r.s.onShareLock()
	}
	return nil
}

func (r memTenantRepo) EmailIsMember(_ context.Context, tenantID string, email pii.Email) (bool, error) {
	return slices.Contains(r.s.memberEmails[tenantID], strings.ToLower(email.Reveal())), nil
}

func (r memTenantRepo) PendingInvitationByEmail(_ context.Context, tenantID string, email pii.Email) (app.Invitation, bool, error) {
	for _, inv := range r.s.invitationsMap() {
		if inv.TenantID == tenantID && strings.EqualFold(inv.Email.Reveal(), email.Reveal()) &&
			inv.AcceptedAt.IsZero() && inv.RevokedAt.IsZero() {
			return inv.Invitation, true, nil
		}
	}
	return app.Invitation{}, false, nil
}

func (r memTenantRepo) CreateInvitation(_ context.Context, n app.NewInvitation) (app.Invitation, error) {
	m := r.s.invitationsMap()
	id := fmt.Sprintf("0190a1b2-c3d4-7e5f-8a9b-%012d", len(m)+1)
	inv := app.Invitation{ID: id, TenantID: n.TenantID, Email: n.Email, Role: n.Role, InvitedBy: n.InvitedBy, ExpiresAt: n.ExpiresAt}
	m[id] = &memInvitation{Invitation: inv, tokenHash: n.TokenHash}
	return inv, nil
}

func (r memTenantRepo) InvitationByID(_ context.Context, tenantID, id string) (app.Invitation, bool, error) {
	inv, ok := r.s.invitationsMap()[id]
	if !ok || inv.TenantID != tenantID {
		return app.Invitation{}, false, nil
	}
	return inv.Invitation, true, nil
}

func (r memTenantRepo) RotateInvitation(_ context.Context, id string, tokenHash []byte, expiresAt time.Time) error {
	inv := r.s.invitationsMap()[id]
	inv.tokenHash, inv.ExpiresAt = tokenHash, expiresAt
	return nil
}

func (r memTenantRepo) RevokeInvitation(_ context.Context, id string) error {
	r.s.invitationsMap()[id].RevokedAt = time.Now()
	return nil
}

func (r memTenantRepo) ListInvitations(_ context.Context, tenantID, after string, limit int) ([]app.Invitation, error) {
	var out []app.Invitation
	for _, inv := range r.s.invitationsMap() {
		if inv.TenantID == tenantID && inv.ID > after && inv.AcceptedAt.IsZero() && inv.RevokedAt.IsZero() {
			out = append(out, inv.Invitation)
		}
	}
	slices.SortFunc(out, func(a, b app.Invitation) int { return strings.Compare(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (r memTenantRepo) AcceptInvitation(_ context.Context, tokenHash []byte, _ netip.Prefix) (app.AcceptedInvitation, error) {
	for _, inv := range r.s.invitationsMap() {
		if bytes.Equal(inv.tokenHash, tokenHash) {
			return app.AcceptedInvitation{TenantID: inv.TenantID, Role: inv.Role}, nil
		}
	}
	return app.AcceptedInvitation{}, domain.ErrInvitationNotFound()
}

// memSender records what it was asked to send, and fails if fail is set.
type memSender struct {
	sent     []string // email role link
	messages []app.InvitationMessage
	fail     error
}

func (s *memSender) Send(_ context.Context, msg app.InvitationMessage) error {
	s.sent = append(s.sent, msg.To.Reveal()+" "+msg.Role+" "+msg.Link.Reveal())
	s.messages = append(s.messages, msg)
	return s.fail
}

func (s *memSender) token(t *testing.T, i int) string {
	t.Helper()
	_, token, ok := strings.Cut(s.sent[i], "#token=")
	if !ok {
		t.Fatalf("no token in %q", s.sent[i])
	}
	return token
}

func invitationsCatalog(t *testing.T) *authz.Catalog {
	t.Helper()
	c, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions()},
		authz.Role{Name: "admin", Permissions: []authctx.Permission{
			authz.PermMembersRead, authz.PermMembersInvite, authz.PermMembersManage, authctx.PermScopeTenant,
		}},
		authz.Role{Name: "member", Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}},
		// Invites, but holds less than admin: it may offer member, never admin.
		authz.Role{Name: "recruiter", Permissions: []authctx.Permission{
			authz.PermMembersRead, authz.PermMembersInvite, authctx.PermScopeTenant,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type invFixture struct {
	in     *app.Invitations
	store  *memTenants
	sender *memSender
	c      *authz.Catalog
	logs   *bytes.Buffer
	now    time.Time
}

func newInvFixture(t *testing.T) *invFixture {
	t.Helper()
	c := invitationsCatalog(t)
	store := newMemTenants()
	store.add(tenantA, ownerID, "owner")
	store.add(tenantA, adminID, "admin")
	store.add(tenantA, member2, "member")
	store.add(tenantA, stranger, "recruiter")
	store.memberEmails = map[string][]string{tenantA: {"member@example.com"}}
	f := &invFixture{store: store, sender: &memSender{}, c: c, logs: &bytes.Buffer{}, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	auth := &fakeAuthorizer{}
	f.in = app.NewInvitations(store, auth, app.NewMembers(store, auth, c), f.sender, app.InvitationsConfig{
		FrontBaseURL:  "https://app.example.com/",
		InviteLimiter: ratelimit.NewMemory(20, time.Hour, nil),
		AcceptLimiter: ratelimit.NewMemory(10, 15*time.Minute, nil),
	}, slog.New(slog.NewJSONHandler(f.logs, nil)), func() time.Time { return f.now })
	return f
}

func TestInvitations_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, actor, actorRole, email, role string
		want                                error
	}{
		{"admin invites a member", adminID, "admin", "new@example.com", "member", nil},
		{"admin invites an admin", adminID, "admin", "new@example.com", "admin", nil},
		{"owner invites an owner", ownerID, "owner", "new@example.com", "owner", nil},
		{"admin cannot invite an owner", adminID, "admin", "new@example.com", "owner", domain.ErrInvitationRoleNotOfferable()},
		{"recruiter invites a member", stranger, "recruiter", "new@example.com", "member", nil},
		{"recruiter cannot invite an admin", stranger, "recruiter", "new@example.com", "admin", domain.ErrInvitationRoleNotOfferable()},
		{"member cannot invite", member2, "member", "new@example.com", "member", authz.ErrDenied()},
		{"an unknown role", ownerID, "owner", "new@example.com", "ghost", domain.ErrInvitationUnknownRole()},
		{"an invalid email", ownerID, "owner", "Ana <ana@example.com>", "member", domain.ErrInvitationEmailInvalid()},
		{"a member's email", ownerID, "owner", "MEMBER@example.com", "member", domain.ErrInvitationAlreadyMember()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newInvFixture(t)
			inv, err := f.in.Create(actingAs(t, f.c, tc.actor, tc.actorRole), tc.email, tc.role)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("Create = %v, want %v", err, tc.want)
				}
				if len(f.sender.sent) != 0 || len(f.store.invitations) != 0 {
					t.Fatal("a refused invitation was stored or sent")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if inv.InvitedBy != tc.actor || !inv.ExpiresAt.Equal(f.now.Add(domain.InvitationTTL)) {
				t.Fatalf("invitation = %+v", inv)
			}
			// The link goes out once, with a token whose hash is the one stored.
			if len(f.sender.sent) != 1 || !strings.HasPrefix(f.sender.sent[0], tc.email+" "+tc.role+" https://app.example.com/invite#token=") {
				t.Fatalf("sent = %v", f.sender.sent)
			}
			sum := sha256.Sum256([]byte(f.sender.token(t, 0)))
			if !bytes.Equal(f.store.invitations[inv.ID].tokenHash, sum[:]) {
				t.Fatal("the stored hash is not the hash of the token sent")
			}
			// The tenant was locked before writing, and the audit never names the email.
			if !slices.Contains(f.store.calls, "share-lock "+tenantA) {
				t.Fatalf("calls = %v", f.store.calls)
			}
			if len(f.store.audits) != 1 || f.store.audits[0].Action != app.AuditInvitationCreated {
				t.Fatalf("audits = %+v", f.store.audits)
			}
			for _, v := range f.store.audits[0].Metadata {
				if strings.Contains(v, "@") {
					t.Fatalf("audit metadata holds an email: %v", f.store.audits[0].Metadata)
				}
			}
		})
	}
}

func TestInvitations_Duplicates(t *testing.T) {
	t.Parallel()

	f := newInvFixture(t)
	ctx := actingAs(t, f.c, adminID, "admin")
	first, err := f.in.Create(ctx, "new@example.com", "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.in.Create(ctx, "NEW@example.com", "admin"); !errors.Is(err, domain.ErrInvitationPending()) {
		t.Fatalf("second invitation = %v, want %v", err, domain.ErrInvitationPending())
	}

	// Once expired, inviting again revokes it and creates a new one.
	f.now = f.now.Add(domain.InvitationTTL + time.Second)
	second, err := f.in.Create(ctx, "new@example.com", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if f.store.invitations[first.ID].RevokedAt.IsZero() || second.ID == first.ID {
		t.Fatal("the expired invitation was not replaced")
	}
	var actions []string
	for _, a := range f.store.audits {
		actions = append(actions, a.Action+"/"+a.Metadata["reason"])
	}
	if want := []string{"invitation.created/", "invitation.revoked/expired_replaced", "invitation.created/"}; !slices.Equal(actions, want) {
		t.Fatalf("audits = %v, want %v", actions, want)
	}
}

func TestInvitations_ResendAndRevoke(t *testing.T) {
	t.Parallel()

	f := newInvFixture(t)
	owner := actingAs(t, f.c, ownerID, "owner")
	admin := actingAs(t, f.c, adminID, "admin")
	ownerInv, err := f.in.Create(owner, "boss@example.com", "owner")
	if err != nil {
		t.Fatal(err)
	}
	memberInv, err := f.in.Create(admin, "new@example.com", "member")
	if err != nil {
		t.Fatal(err)
	}

	// An admin may neither resend nor revoke an owner invitation.
	if _, err := f.in.Resend(admin, ownerInv.ID); !errors.Is(err, domain.ErrInvitationRoleNotOfferable()) {
		t.Fatalf("admin resends owner invitation = %v", err)
	}
	if err := f.in.Revoke(admin, ownerInv.ID); !errors.Is(err, domain.ErrInvitationRoleNotOfferable()) {
		t.Fatalf("admin revokes owner invitation = %v", err)
	}

	// Resending rotates the token and renews the expiry; the inviter stays.
	f.now = f.now.Add(time.Hour)
	resent, err := f.in.Resend(owner, memberInv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.sender.token(t, 1) == f.sender.token(t, 2) || !resent.ExpiresAt.Equal(f.now.Add(domain.InvitationTTL)) ||
		resent.InvitedBy != adminID {
		t.Fatalf("resent = %+v, sent = %v", resent, f.sender.sent)
	}
	sum := sha256.Sum256([]byte(f.sender.token(t, 2)))
	if !bytes.Equal(f.store.invitations[memberInv.ID].tokenHash, sum[:]) {
		t.Fatal("the stored hash is not the new token's")
	}

	if err := f.in.Revoke(admin, memberInv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.in.Resend(admin, memberInv.ID); !errors.Is(err, domain.ErrInvitationRevoked()) {
		t.Fatalf("resend revoked = %v", err)
	}
	if err := f.in.Revoke(admin, memberInv.ID); !errors.Is(err, domain.ErrInvitationRevoked()) {
		t.Fatalf("revoke twice = %v", err)
	}
	for _, id := range []string{"0190a1b2-c3d4-7e5f-8a9b-999999999999", "not-a-uuid"} {
		if err := f.in.Revoke(admin, id); !errors.Is(err, domain.ErrInvitationNotFound()) {
			t.Fatalf("revoke %s = %v", id, err)
		}
	}
	f.store.invitations[ownerInv.ID].AcceptedAt = f.now
	if _, err := f.in.Resend(owner, ownerInv.ID); !errors.Is(err, domain.ErrInvitationAccepted()) {
		t.Fatalf("resend accepted = %v", err)
	}
}

// The actor's role is read again under the tenant's lock: one that changed since the
// request started writes nothing.
func TestInvitations_ActorRoleChangedMeanwhile(t *testing.T) {
	t.Parallel()

	f := newInvFixture(t)
	f.store.onShareLock = func() { f.store.add(tenantA, adminID, "member") }
	_, err := f.in.Create(actingAs(t, f.c, adminID, "admin"), "new@example.com", "member")
	if !errors.Is(err, domain.ErrMembershipChanged()) {
		t.Fatalf("Create = %v, want %v", err, domain.ErrMembershipChanged())
	}
	if len(f.store.invitations) != 0 || len(f.sender.sent) != 0 {
		t.Fatal("the invitation was written")
	}
}

// A failed delivery leaves the invitation and logs at Error, without the link.
func TestInvitations_SendFailureKeepsTheInvitation(t *testing.T) {
	t.Parallel()

	f := newInvFixture(t)
	f.sender.fail = errors.New("smtp down")
	inv, err := f.in.Create(actingAs(t, f.c, adminID, "admin"), "new@example.com", "member")
	if err != nil {
		t.Fatalf("Create = %v, want success", err)
	}
	if _, ok := f.store.invitations[inv.ID]; !ok {
		t.Fatal("the invitation is gone")
	}
	logs := f.logs.String()
	if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, inv.ID) || strings.Contains(logs, f.sender.token(t, 0)) {
		t.Fatalf("logs = %s", logs)
	}
}

func TestInvitations_ListAndAccept(t *testing.T) {
	t.Parallel()

	f := newInvFixture(t)
	admin := actingAs(t, f.c, adminID, "admin")
	for i := range 3 {
		if _, err := f.in.Create(admin, fmt.Sprintf("u%d@example.com", i), "member"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.in.List(admin, "", 2)
	if err != nil || len(page.Invitations) != 2 || page.Next == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = f.in.List(admin, page.Next, 2)
	if err != nil || len(page.Invitations) != 1 || page.Next != "" {
		t.Fatalf("second page = %+v, %v", page, err)
	}
	if _, err := f.in.List(actingAs(t, f.c, member2, "member"), "", 0); !errors.Is(err, authz.ErrDenied()) {
		t.Fatalf("member lists = %v", err)
	}

	u, err := authctx.NewUser(stranger, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.in.Accept(t.Context(), u, f.sender.token(t, 0))
	if err != nil || got.TenantID != tenantA || got.Role != "member" {
		t.Fatalf("Accept = %+v, %v", got, err)
	}
	if _, err := f.in.Accept(t.Context(), u, "not-a-token"); !errors.Is(err, domain.ErrInvitationNotFound()) {
		t.Fatalf("malformed token = %v", err)
	}
}

func TestInvitations_RateLimits(t *testing.T) {
	t.Parallel()

	f := newInvFixture(t)
	admin := actingAs(t, f.c, adminID, "admin")
	for i := range 20 {
		// Valid or not, every attempt counts.
		_, _ = f.in.Create(admin, fmt.Sprintf("u%d@example.com", i), "member")
	}
	if _, err := f.in.Create(admin, "late@example.com", "member"); apperr.KindOf(err) != apperr.KindRateLimited {
		t.Fatalf("21st invitation = %v", err)
	}
	if _, err := f.in.Resend(admin, "0190a1b2-c3d4-7e5f-8a9b-000000000001"); apperr.KindOf(err) != apperr.KindRateLimited {
		t.Fatalf("resend shares the quota: %v", err)
	}
	// Another user has their own quota.
	if _, err := f.in.Create(actingAs(t, f.c, ownerID, "owner"), "other@example.com", "member"); err != nil {
		t.Fatal(err)
	}

	u, err := authctx.NewUser(stranger, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		_, _ = f.in.Accept(t.Context(), u, "x")
	}
	if _, err := f.in.Accept(t.Context(), u, "x"); apperr.KindOf(err) != apperr.KindRateLimited {
		t.Fatalf("11th accept = %v", err)
	}
}

// The email says who invites: the member who sent it, by display name, or by email when
// they have none. The tenant's name and the role come along for the adapter to render.
func TestInvitations_MessageNamesTheInviter(t *testing.T) {
	t.Parallel()

	f := newInvFixture(t)
	admin := f.store.members[tenantA][adminID]
	admin.DisplayName, admin.Email = "Bea <b>", pii.Email("bea@example.com")
	f.store.members[tenantA][adminID] = admin
	owner := f.store.members[tenantA][ownerID]
	owner.Email = pii.Email("owner@example.com")
	f.store.members[tenantA][ownerID] = owner

	inv, err := f.in.Create(actingAs(t, f.c, adminID, "admin"), "new@example.com", "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.in.Resend(actingAs(t, f.c, ownerID, "owner"), inv.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.sender.messages) != 2 {
		t.Fatalf("messages = %+v", f.sender.messages)
	}
	first, resent := f.sender.messages[0], f.sender.messages[1]
	if first.InviterName != "Bea <b>" || first.Role != "member" || first.To.Reveal() != "new@example.com" ||
		first.TenantName == "" {
		t.Fatalf("message = %+v", first)
	}
	if resent.InviterName != "owner@example.com" {
		t.Fatalf("resent by %q, want the owner who resent it, by email", resent.InviterName)
	}
}
