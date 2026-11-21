package usecases_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"chaskicode.dev/saas/modules/identity/app"
	"chaskicode.dev/saas/modules/identity/domain"
	"chaskicode.dev/saas/platform/authctx"
	"chaskicode.dev/saas/platform/authz"
)

const (
	tenantA   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	tenantB   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
	suspended = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5d"
	memberID  = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f0001"
	otherID   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f0002"
)

func tenancyCatalog(t *testing.T) *authz.Catalog {
	t.Helper()
	c, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: append(authz.PlatformPermissions(), "cases:read"), RequiresMFA: true, MFAMaxAge: 12 * time.Hour},
		authz.Role{Name: "member", Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}},
		authz.Role{Name: "client", Permissions: []authctx.Permission{"cases:read"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// tenancyFixture: memberID is a member of tenantA and of the suspended tenant; otherID
// is the one member of tenantB.
func tenancyFixture(t *testing.T, now time.Time, role string, disabled bool) *usecases.Tenancy {
	t.Helper()
	return tenancyFixtureWithFactor(t, now, role, disabled, false)
}

// tenancyFixtureWithFactor also gives memberID a verified factor when verified is true.
func tenancyFixtureWithFactor(t *testing.T, now time.Time, role string, disabled, verified bool) *usecases.Tenancy {
	t.Helper()
	store := newMemStore()
	if verified {
		store.st.factors["factor-1"] = memFactor{user: memberID, providerID: "pf-1", verifiedAt: now.Add(-24 * time.Hour)}
	}
	store.st.memberships = map[string]map[string]usecases.Membership{
		tenantA:   {memberID: {Role: role, TenantActive: true, UserDisabled: disabled}},
		tenantB:   {otherID: {Role: "member", TenantActive: true}},
		suspended: {memberID: {Role: role, TenantActive: false}},
	}
	return usecases.NewTenancy(store, tenancyCatalog(t), func() time.Time { return now })
}

func user(t *testing.T, mfa time.Time) authctx.User {
	t.Helper()
	u, err := authctx.NewUser(memberID, mfa)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTenancy_SameForbiddenForEveryReason(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		tenant   string
		disabled bool
	}{
		{"missing header", "", false},
		{"malformed", "not-a-uuid", false},
		{"nil uuid", "00000000-0000-0000-0000-000000000000", false},
		{"no such tenant", "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f9999", false},
		{"another user's tenant", tenantB, false},
		{"suspended tenant", suspended, false},
		{"disabled user", tenantA, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ten := tenancyFixture(t, now, "member", tc.disabled)
			_, err := ten.Enter(t.Context(), user(t, now), tc.tenant)
			if !errors.Is(err, domain.ErrTenantForbidden()) {
				t.Fatalf("Enter = %v, want auth.tenant_forbidden", err)
			}
		})
	}
}

func TestTenancy_RoleMFA(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	enroll, verify := domain.ErrMFAEnrollmentRequired(), domain.ErrMFAVerificationRequired()
	tests := []struct {
		name      string
		role      string
		mfa       time.Time
		hasFactor bool
		want      error // nil: allowed
	}{
		{"owner without a factor must enroll", authz.RoleOwner, time.Time{}, false, enroll},
		{"owner with a factor, session without MFA, must verify", authz.RoleOwner, time.Time{}, true, verify},
		{"owner with MFA older than 12 h", authz.RoleOwner, now.Add(-12*time.Hour - time.Second), true, verify},
		{"owner with recent MFA", authz.RoleOwner, now.Add(-11 * time.Hour), true, nil},
		{"member needs no MFA", "member", time.Time{}, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tenancyFixtureWithFactor(t, now, tc.role, false, tc.hasFactor).Enter(t.Context(), user(t, tc.mfa), tenantA)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Enter = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTenancy_Principal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		role        string
		scope       authctx.Scope
		has         authctx.Permission
		unknownRole bool
	}{
		{"member", authctx.ScopeTenant, authz.PermMembersRead, false},
		{"client", authctx.ScopeOwn, "cases:read", false},
		{"ghost", authctx.ScopeOwn, "", true},
		// The system role is never in a catalog, so it is unknown and cannot be claimed.
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			t.Parallel()
			got, err := tenancyFixture(t, now, tc.role, false).Enter(t.Context(), user(t, time.Time{}), tenantA)
			if err != nil {
				t.Fatal(err)
			}
			p := got.Principal
			if p.UserID() != memberID || p.TenantID() != tenantA || p.Role() != tc.role || p.Scope() != tc.scope {
				t.Fatalf("principal = %s/%s/%s/%s", p.UserID(), p.TenantID(), p.Role(), p.Scope())
			}
			if got.UnknownRole != tc.unknownRole {
				t.Fatalf("UnknownRole = %v, want %v", got.UnknownRole, tc.unknownRole)
			}
			if tc.has == "" {
				if len(p.Permissions()) != 0 {
					t.Fatalf("an unknown role has no permissions, got %v", p.Permissions())
				}
				return
			}
			if !p.Has(tc.has) {
				t.Fatalf("principal lacks %s: %v", tc.has, p.Permissions())
			}
		})
	}
}

func TestTenancy_SystemRoleIsRefused(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	_, err := tenancyFixture(t, now, authctx.SystemRole, false).Enter(t.Context(), user(t, time.Time{}), tenantA)
	if !errors.Is(err, domain.ErrTenantForbidden()) {
		t.Fatalf("a membership with the system role must be refused, got %v", err)
	}
}

func TestDenialAuditor_RecordsInThePrincipalsTenant(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	p, err := authctx.NewPrincipal(memberID, tenantA, "member", authctx.PermScopeTenant)
	if err != nil {
		t.Fatal(err)
	}
	if err := usecases.NewDenialAuditor(store).RecordDenied(t.Context(), p, authz.PermAuditRead); err != nil {
		t.Fatal(err)
	}
	audit := store.snapshot().audit
	if len(audit) != 1 {
		t.Fatalf("audit = %v", audit)
	}
	a := audit[0]
	if a.Action != authz.AuditDenied || a.tenant != tenantA || a.actor != memberID ||
		a.TargetID != string(authz.PermAuditRead) || a.Metadata["role"] != "member" {
		t.Fatalf("event = %+v", a)
	}
}

func TestAccounts_MeOnlyHasTheUsersOwnMemberships(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	store.st.users[memberID] = memUser{email: "me@example.test"}
	store.st.memberships = map[string]map[string]usecases.Membership{
		tenantA:   {memberID: {Role: "member", TenantActive: true}, otherID: {Role: "owner", TenantActive: true}},
		tenantB:   {otherID: {Role: "member", TenantActive: true}},
		suspended: {memberID: {Role: "client"}},
	}
	store.st.factors["factor-1"] = memFactor{user: memberID, providerID: "pf-1", verifiedAt: time.Now()}
	store.st.factors["factor-2"] = memFactor{user: otherID, providerID: "pf-2", verifiedAt: time.Now()}
	me, err := usecases.NewAccounts(store, tenancyCatalog(t)).Me(t.Context(), usecases.Current{UserID: memberID})
	if err != nil {
		t.Fatal(err)
	}
	if !me.MFAEnrolled() || len(me.Factors) != 1 || me.Factors[0].ID != "factor-1" {
		t.Fatalf("factors = %+v, want only the user's own", me.Factors)
	}
	if me.User.ID != memberID || me.User.Email.Reveal() != "me@example.test" {
		t.Fatalf("user = %+v", me.User)
	}
	want := []usecases.TenantMembership{
		{TenantID: tenantA, TenantStatus: "active", Role: "member"},
		{TenantID: suspended, TenantStatus: "suspended", Role: "client"},
	}
	if !slices.Equal(me.Memberships, want) {
		t.Fatalf("memberships = %+v, want %+v", me.Memberships, want)
	}

	if _, err := usecases.NewAccounts(store, tenancyCatalog(t)).Me(t.Context(), usecases.Current{UserID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f0003"}); !errors.Is(err, domain.ErrSessionInvalid()) {
		t.Fatalf("a user that no longer exists: %v, want auth.session_invalid", err)
	}
}

func TestAccounts_CurrentTenant(t *testing.T) {
	t.Parallel()

	accounts := usecases.NewAccounts(newMemStore(), tenancyCatalog(t))
	if _, err := accounts.CurrentTenant(t.Context()); !errors.Is(err, domain.ErrTenantForbidden()) {
		t.Fatalf("without a principal: %v", err)
	}
	p, err := authctx.NewPrincipal(memberID, tenantA, "member", authctx.PermScopeTenant)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := accounts.CurrentTenant(authctx.WithPrincipal(t.Context(), p))
	if err != nil || cur.Tenant.ID != tenantA || cur.Principal.Role() != "member" {
		t.Fatalf("CurrentTenant = %+v, %v", cur, err)
	}
}
