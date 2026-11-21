package usecases_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/identity/usecases"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/authz"
	"chaskicode.dev/saas/platform/kit/ratelimit"
)

// memTenants is a fake usecases.TenantStore: the members of each tenant, by user id, and
// what the use cases asked the database to do.
type memTenants struct {
	members map[string]map[string]usecases.Member // tenant -> user -> member
	names   map[string]string
	calls   []string
	audits  []usecases.AuditEvent
	// offerable is what the last ChangeMemberRole was told the new role may offer.
	offerable []string
	// The invitation side (invitations_test.go).
	invitations  map[string]*memInvitation
	memberEmails map[string][]string
	onShareLock  func()
	// fail, if set, is what the database functions return (last owner, changed role…).
	fail error
}

func newMemTenants() *memTenants {
	return &memTenants{members: map[string]map[string]usecases.Member{}, names: map[string]string{}}
}

func (s *memTenants) add(tenant, userID, role string) {
	if s.members[tenant] == nil {
		s.members[tenant] = map[string]usecases.Member{}
	}
	s.members[tenant][userID] = usecases.Member{
		MembershipID: fmt.Sprintf("0190a1b2-c3d4-7e5f-8a9b-%012d", len(s.members[tenant])+1), UserID: userID, Role: role,
	}
}

func (s *memTenants) AsUser(ctx context.Context, _ string, fn func(context.Context, usecases.TenantRepo) error) error {
	return fn(ctx, memTenantRepo{s: s})
}

func (s *memTenants) InTenant(ctx context.Context, p authctx.Principal, fn func(context.Context, usecases.TenantRepo) error) error {
	return fn(ctx, memTenantRepo{s: s, tenant: p.TenantID(), actor: p.UserID()})
}

type memTenantRepo struct {
	s      *memTenants
	tenant string
	actor  string
}

func (r memTenantRepo) CreateTenant(_ context.Context, name, slug string, maxTenants int, _ netip.Prefix) (string, error) {
	r.s.calls = append(r.s.calls, fmt.Sprintf("create %s %s %d", name, slug, maxTenants))
	return tenantB, r.s.fail
}

func (r memTenantRepo) RenameTenant(_ context.Context, tenantID, name string) (string, usecases.Tenant, error) {
	previous := r.s.names[tenantID]
	r.s.names[tenantID] = name
	return previous, usecases.Tenant{ID: tenantID, Slug: "acme", Name: name}, nil
}

func (r memTenantRepo) RecordTenantAudit(_ context.Context, _ string, e usecases.AuditEvent) error {
	r.s.audits = append(r.s.audits, e)
	return nil
}

func (r memTenantRepo) ListMembers(_ context.Context, tenantID, after string, limit int) ([]usecases.Member, error) {
	var out []usecases.Member
	for _, m := range r.s.members[tenantID] {
		if m.MembershipID > after {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b usecases.Member) int { return strings.Compare(a.MembershipID, b.MembershipID) })
	return out[:min(limit, len(out))], nil
}

func (r memTenantRepo) MemberByUser(_ context.Context, tenantID, userID string) (usecases.Member, bool, error) {
	m, ok := r.s.members[tenantID][userID]
	return m, ok, nil
}

func (r memTenantRepo) ChangeMemberRole(_ context.Context, userID, role, actorRole, memberRole string, offerable []string, _ netip.Prefix) error {
	r.s.calls = append(r.s.calls, fmt.Sprintf("change %s %s expecting %s %s", userID, role, actorRole, memberRole))
	r.s.offerable = offerable
	return r.s.fail
}

func (r memTenantRepo) RemoveMember(_ context.Context, userID, actorRole, memberRole string, _ netip.Prefix) error {
	r.s.calls = append(r.s.calls, fmt.Sprintf("remove %s expecting %s %s", userID, actorRole, memberRole))
	return r.s.fail
}

func (r memTenantRepo) LeaveTenant(context.Context, netip.Prefix) error {
	r.s.calls = append(r.s.calls, "leave "+r.actor)
	return r.s.fail
}

// fakeAuthorizer checks the context's principal, like authz.Authorizer, and records
// what it was asked and with which role.
type fakeAuthorizer struct {
	asked []string
}

func (a *fakeAuthorizer) Require(ctx context.Context, perm authctx.Permission) error {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return errors.New("no principal")
	}
	a.asked = append(a.asked, p.Role()+" "+string(perm))
	if !p.Has(perm) {
		return authz.ErrDenied()
	}
	return nil
}

const (
	ownerID  = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f1001"
	adminID  = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f1002"
	member2  = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f1003"
	stranger = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f1004"
)

func membersCatalog(t *testing.T) *authz.Catalog {
	t.Helper()
	c, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions()},
		authz.Role{Name: "admin", Permissions: []authctx.Permission{
			authz.PermMembersRead, authz.PermMembersManage, authz.PermTenantUpdate, authctx.PermScopeTenant,
		}},
		authz.Role{Name: "member", Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// actingAs returns a context with the principal of userID in tenantA with role.
func actingAs(t *testing.T, c *authz.Catalog, userID, role string) context.Context {
	t.Helper()
	p, err := authctx.NewPrincipal(userID, tenantA, role, c.Permissions(role)...)
	if err != nil {
		t.Fatal(err)
	}
	return authctx.WithPrincipal(t.Context(), p)
}

// membersFixture: tenantA has an owner, an admin and a member; tenantB has stranger.
func membersFixture(t *testing.T) (*usecases.Members, *memTenants, *fakeAuthorizer, *authz.Catalog) {
	t.Helper()
	c := membersCatalog(t)
	store := newMemTenants()
	store.add(tenantA, ownerID, "owner")
	store.add(tenantA, adminID, "admin")
	store.add(tenantA, member2, "member")
	store.add(tenantB, stranger, "owner")
	authorizer := &fakeAuthorizer{}
	return usecases.NewMembers(store, authorizer, c), store, authorizer, c
}

func TestMembers_ChangeRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		actor, actorRole string
		target, role     string
		wantErr          error
		wantCall         string
	}{
		{"admin promotes a member", adminID, "admin", member2, "admin", nil,
			"change " + member2 + " admin expecting admin member"},
		{"owner demotes the admin", ownerID, "owner", adminID, "member", nil,
			"change " + adminID + " member expecting owner admin"},
		{"admin cannot touch the owner", adminID, "admin", ownerID, "member", domain.ErrMemberTargetForbidden(), ""},
		{"admin cannot name owners", adminID, "admin", member2, "owner", domain.ErrRoleNotAssignable(), ""},
		{"member cannot manage", member2, "member", adminID, "member", authz.ErrDenied(), ""},
		{"nobody changes their own role", ownerID, "owner", ownerID, "admin", domain.ErrMemberSelfChange(), ""},
		{"a user of another tenant is not found", ownerID, "owner", stranger, "member", domain.ErrMemberNotFound(), ""},
		{"a malformed id is not found", ownerID, "owner", "me", "member", domain.ErrMemberNotFound(), ""},
		{"an unknown role", ownerID, "owner", member2, "ghost", domain.ErrUnknownRole(), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			members, store, _, c := membersFixture(t)
			got, err := members.ChangeRole(actingAs(t, c, tc.actor, tc.actorRole), tc.target, tc.role)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ChangeRole = %v, want %v", err, tc.wantErr)
				}
				if len(store.calls) != 0 {
					t.Fatalf("a refused change reached the database: %v", store.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.UserID != tc.target || got.Role != tc.role {
				t.Fatalf("member = %+v", got)
			}
			if !slices.Equal(store.calls, []string{tc.wantCall}) {
				t.Fatalf("calls = %v, want [%s]", store.calls, tc.wantCall)
			}
		})
	}
}

// The decision is made on the role the request started with, and that role is what
// the database is told to expect: if it changed meanwhile, the database refuses.
func TestMembers_ExpectedRolesReachTheDatabase(t *testing.T) {
	t.Parallel()

	members, store, _, c := membersFixture(t)
	store.fail = apperr.Conflict("membership.changed", "changed")
	_, err := members.ChangeRole(actingAs(t, c, adminID, "admin"), member2, "admin")
	if e, ok := apperr.As(err); !ok || e.Code != "membership.changed" {
		t.Fatalf("ChangeRole = %v, want membership.changed", err)
	}
	if want := []string{"change " + member2 + " admin expecting admin member"}; !slices.Equal(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
}

func TestMembers_RemoveAndLeave(t *testing.T) {
	t.Parallel()

	members, store, _, c := membersFixture(t)
	if err := members.Remove(actingAs(t, c, adminID, "admin"), ownerID); !errors.Is(err, domain.ErrMemberTargetForbidden()) {
		t.Fatalf("admin removes owner = %v", err)
	}
	if err := members.Remove(actingAs(t, c, adminID, "admin"), adminID); !errors.Is(err, domain.ErrMemberSelfChange()) {
		t.Fatalf("admin removes self = %v", err)
	}
	if err := members.Remove(actingAs(t, c, ownerID, "owner"), adminID); err != nil {
		t.Fatal(err)
	}
	// Leaving needs no permission: a member without members:manage may.
	if err := members.Leave(actingAs(t, c, member2, "member")); err != nil {
		t.Fatal(err)
	}
	want := []string{"remove " + adminID + " expecting owner admin", "leave " + member2}
	if !slices.Equal(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}

	// What the database refuses comes back as is.
	store.fail = apperr.Conflict("membership.last_owner", "last owner")
	if err := members.Leave(actingAs(t, c, ownerID, "owner")); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("Leave = %v, want the conflict", err)
	}
}

func TestMembers_List(t *testing.T) {
	t.Parallel()

	members, _, authorizer, c := membersFixture(t)
	ctx := actingAs(t, c, member2, "member")

	first, err := members.List(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Members) != 2 || first.Next == "" || first.Members[0].UserID != ownerID {
		t.Fatalf("first page = %+v", first)
	}
	second, err := members.List(ctx, first.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Members) != 1 || second.Next != "" || second.Members[0].UserID != member2 {
		t.Fatalf("second page = %+v", second)
	}
	for _, m := range append(first.Members, second.Members...) {
		if m.UserID == stranger {
			t.Fatal("a member of another tenant was listed")
		}
	}
	if authorizer.asked[0] != "member members:read" {
		t.Fatalf("asked %v", authorizer.asked)
	}

	for _, bad := range []string{"not base64!", "bm90LWEtdXVpZA"} {
		if _, err := members.List(ctx, bad, 0); !errors.Is(err, domain.ErrCursorInvalid()) {
			t.Fatalf("List(%q) = %v, want %v", bad, err, domain.ErrCursorInvalid())
		}
	}

	noRead, err := authz.NewCatalog(authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions()},
		authz.Role{Name: "client"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := members.List(actingAs(t, noRead, member2, "client"), "", 0); !errors.Is(err, authz.ErrDenied()) {
		t.Fatalf("client lists members = %v, want %v", err, authz.ErrDenied())
	}
}

func TestTenants_Create(t *testing.T) {
	t.Parallel()

	store := newMemTenants()
	tenants := usecases.NewTenants(store, &fakeAuthorizer{}, 5, ratelimit.NewMemory(5, time.Hour, nil))
	u := user(t, time.Time{})

	got, err := tenants.Create(t.Context(), u, "  Acme  ", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tenant.Name != "Acme" || got.Role != authz.RoleOwner || got.Tenant.ID != tenantB {
		t.Fatalf("created = %+v", got)
	}
	for _, tc := range []struct {
		name, slug string
		want       error
	}{
		{"Acme", "a", domain.ErrSlugInvalid()},
		{"Acme", "admin", domain.ErrSlugReserved()},
		{"   ", "acme-2", domain.ErrTenantNameInvalid()},
	} {
		if _, err := tenants.Create(t.Context(), u, tc.name, tc.slug); !errors.Is(err, tc.want) {
			t.Fatalf("Create(%q, %q) = %v, want %v", tc.name, tc.slug, err, tc.want)
		}
	}
	if want := []string{"create Acme acme 5"}; !slices.Equal(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
	// Four attempts so far, valid or not, count against the five an hour.
	if _, err := tenants.Create(t.Context(), u, "Acme", "acme-5"); err != nil {
		t.Fatal(err)
	}
	if _, err := tenants.Create(t.Context(), u, "Acme", "acme-6"); apperr.KindOf(err) != apperr.KindRateLimited {
		t.Fatalf("sixth attempt = %v, want rate limited", err)
	}
}

func TestTenants_Rename(t *testing.T) {
	t.Parallel()

	c := membersCatalog(t)
	store := newMemTenants()
	store.names[tenantA] = "Old"
	tenants := usecases.NewTenants(store, &fakeAuthorizer{}, 5, ratelimit.NewMemory(5, time.Hour, nil))

	if _, err := tenants.Rename(actingAs(t, c, member2, "member"), "New"); !errors.Is(err, authz.ErrDenied()) {
		t.Fatalf("member renames = %v, want %v", err, authz.ErrDenied())
	}
	got, err := tenants.Rename(actingAs(t, c, adminID, "admin"), " New ")
	if err != nil || got.Name != "New" {
		t.Fatalf("Rename = %+v, %v", got, err)
	}
	// The same name again records nothing.
	if _, err := tenants.Rename(actingAs(t, c, adminID, "admin"), "New"); err != nil {
		t.Fatal(err)
	}
	if len(store.audits) != 1 {
		t.Fatalf("audits = %+v, want one", store.audits)
	}
	e := store.audits[0]
	if e.Action != usecases.AuditTenantUpdated || e.TargetID != tenantA || e.Metadata["old_name"] != "Old" || e.Metadata["new_name"] != "New" {
		t.Fatalf("audit = %+v", e)
	}
}

// A role change tells the database which roles the new role may still offer: it keeps
// those pending invitations and revokes the rest (design, 5.6.4).
func TestMembers_ChangeRoleNamesTheOfferableRoles(t *testing.T) {
	t.Parallel()

	c, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions()},
		authz.Role{Name: "admin", Permissions: []authctx.Permission{
			authz.PermMembersRead, authz.PermMembersInvite, authz.PermMembersManage, authctx.PermScopeTenant,
		}},
		authz.Role{Name: "member", Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}},
		authz.Role{Name: "client"},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemTenants()
	store.add(tenantA, ownerID, "owner")
	store.add(tenantA, adminID, "member")
	members := usecases.NewMembers(store, &fakeAuthorizer{}, c)
	ctx := actingAs(t, c, ownerID, "owner")

	for role, want := range map[string][]string{
		"admin":  {"admin", "client", "member"},
		"owner":  {"admin", "client", "member", "owner"},
		"member": {},
	} {
		store.add(tenantA, adminID, "member")
		if role == "member" {
			store.add(tenantA, adminID, "admin")
		}
		if _, err := members.ChangeRole(ctx, adminID, role); err != nil {
			t.Fatalf("to %s: %v", role, err)
		}
		if !slices.Equal(store.offerable, want) {
			t.Fatalf("to %s: offerable = %v, want %v", role, store.offerable, want)
		}
	}
}

// GET /tenant/roles judges each role of the catalog for the caller with the rules of
// ChangeRole (can_assign) and inviting (can_offer). The catalog is the example one.
func TestMembers_Roles(t *testing.T) {
	t.Parallel()

	members := []authctx.Permission{authz.PermMembersRead, authz.PermMembersInvite, authz.PermMembersManage}
	c, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions(), RequiresMFA: true, MFAMaxAge: time.Hour},
		authz.Role{Name: "admin", Permissions: append(members, authz.PermAuditRead, authz.PermTenantUpdate, authctx.PermScopeTenant)},
		authz.Role{Name: "member", Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// name, mfa_required, can_assign, can_offer, in the order of the response.
	view := func(name string, mfa, assign, offer bool) usecases.RoleView {
		return usecases.RoleView{Name: name, MFARequired: mfa, CanAssign: assign, CanOffer: offer}
	}
	tests := []struct {
		role string
		want []usecases.RoleView
	}{
		{"owner", []usecases.RoleView{view("owner", true, true, true), view("admin", false, true, true), view("member", false, true, true)}},
		{"admin", []usecases.RoleView{view("owner", true, false, false), view("admin", false, true, true), view("member", false, true, true)}},
		{"member", []usecases.RoleView{view("owner", true, false, false), view("admin", false, false, false), view("member", false, false, false)}},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			t.Parallel()
			authorizer := &fakeAuthorizer{}
			got, err := usecases.NewMembers(newMemTenants(), authorizer, c).Roles(actingAs(t, c, ownerID, tc.role))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Roles = %+v\nwant    %+v", got, tc.want)
			}
			if want := []string{tc.role + " members:read"}; !slices.Equal(authorizer.asked, want) {
				t.Fatalf("asked = %v, want %v", authorizer.asked, want)
			}
		})
	}

	t.Run("without members:read", func(t *testing.T) {
		t.Parallel()
		noRead, err := authz.NewCatalog(
			authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions()},
			authz.Role{Name: "client"},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = usecases.NewMembers(newMemTenants(), &fakeAuthorizer{}, noRead).Roles(actingAs(t, noRead, ownerID, "client"))
		if !errors.Is(err, authz.ErrDenied()) {
			t.Fatalf("Roles = %v, want %v", err, authz.ErrDenied())
		}
	})
}
