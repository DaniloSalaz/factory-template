package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authz"
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
func tenancyFixture(t *testing.T, now time.Time, role string, disabled bool) *app.Tenancy {
	t.Helper()
	store := newMemStore()
	store.st.memberships = map[string]map[string]app.Membership{
		tenantA:   {memberID: {Role: role, TenantActive: true, UserDisabled: disabled}},
		tenantB:   {otherID: {Role: "member", TenantActive: true}},
		suspended: {memberID: {Role: role, TenantActive: false}},
	}
	return app.NewTenancy(store, tenancyCatalog(t), func() time.Time { return now })
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
	tests := []struct {
		name    string
		role    string
		mfa     time.Time
		wantErr bool
	}{
		{"owner without MFA", authz.RoleOwner, time.Time{}, true},
		{"owner with MFA older than 12 h", authz.RoleOwner, now.Add(-12*time.Hour - time.Second), true},
		{"owner with recent MFA", authz.RoleOwner, now.Add(-11 * time.Hour), false},
		{"member needs no MFA", "member", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tenancyFixture(t, now, tc.role, false).Enter(t.Context(), user(t, tc.mfa), tenantA)
			if tc.wantErr != errors.Is(err, domain.ErrRoleMFARequired()) || (!tc.wantErr && err != nil) {
				t.Fatalf("Enter = %v, want MFA error %v", err, tc.wantErr)
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
