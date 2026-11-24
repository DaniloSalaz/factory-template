package authz_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"chaskicode.dev/saas/core/kit/authctx"
	"chaskicode.dev/saas/core/kit/authz"
)

func owner() authz.Role {
	return authz.Role{
		Name: authz.RoleOwner, Permissions: append(authz.CorePermissions(), "cases:read"),
		RequiresMFA: true, MFAMaxAge: 12 * time.Hour,
	}
}

func admin() authz.Role {
	return authz.Role{Name: "admin", Permissions: []authctx.Permission{
		authz.PermMembersRead, authz.PermMembersInvite, authz.PermMembersManage, authz.PermAuditRead,
		authctx.PermScopeTenant, "cases:read",
	}}
}

func member() authz.Role {
	return authz.Role{Name: "member", Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}}
}

func client() authz.Role {
	return authz.Role{Name: "client", Permissions: []authctx.Permission{"cases:read"}}
}

func mustCatalog(t *testing.T, roles ...authz.Role) *authz.Catalog {
	t.Helper()
	c, err := authz.NewCatalog(roles...)
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return c
}

func TestNewCatalog_Refuses(t *testing.T) {
	t.Parallel()

	adminWithTenantOwn := admin()
	adminWithTenantOwn.Permissions = append(adminWithTenantOwn.Permissions, authz.PermTenantOwn)
	renamedOwner := owner()
	renamedOwner.Name = "proprietor"
	ownerWithoutAudit := owner()
	ownerWithoutAudit.Permissions = slices.DeleteFunc(slices.Clone(ownerWithoutAudit.Permissions),
		func(p authctx.Permission) bool { return p == authz.PermAuditRead })
	broaderThanOwner := admin()
	broaderThanOwner.Permissions = append(broaderThanOwner.Permissions, "cases:delete")
	mfaWithoutAge := admin()
	mfaWithoutAge.RequiresMFA = true
	ageWithoutMFA := admin()
	ageWithoutMFA.MFAMaxAge = time.Hour

	tests := []struct {
		name  string
		roles []authz.Role
		want  string
	}{
		{"another role has tenant:own", []authz.Role{owner(), adminWithTenantOwn}, `role "admin" has tenant:own`},
		{"owner renamed", []authz.Role{renamedOwner, admin()}, `role "owner" is missing`},
		{"owner removed", []authz.Role{admin(), member()}, `role "owner" is missing`},
		{"owner lacks a core permission", []authz.Role{ownerWithoutAudit}, "lacks core permission audit:read"},
		{"a role broader than owner", []authz.Role{owner(), broaderThanOwner}, `role "admin" has permissions that "owner" lacks`},
		{"duplicate role", []authz.Role{owner(), member(), member()}, `"member" is declared twice`},
		{"system role", []authz.Role{owner(), {Name: authctx.SystemRole}}, "reserved for system jobs"},
		{"empty name", []authz.Role{owner(), {Name: ""}}, "role name"},
		{"bad permission", []authz.Role{owner(), {Name: "x", Permissions: []authctx.Permission{"read"}}}, "resource:action"},
		{"MFA without age", []authz.Role{owner(), mfaWithoutAge}, "requires MFA but has no MFAMaxAge"},
		{"age without MFA", []authz.Role{owner(), ageWithoutMFA}, "does not require MFA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := authz.NewCatalog(tc.roles...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewCatalog = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestCatalog_Inclusion(t *testing.T) {
	t.Parallel()

	c := mustCatalog(t, owner(), admin(), member(), client())
	twin := admin()
	twin.Name = "admin-twin"
	c2 := mustCatalog(t, owner(), admin(), twin)

	tests := []struct {
		name           string
		cat            *authz.Catalog
		a, b           string
		includes, strc bool
	}{
		{"member in admin", c, "member", "admin", true, true},
		{"admin in owner", c, "admin", authz.RoleOwner, true, true},
		{"owner never in admin", c, authz.RoleOwner, "admin", false, false},
		{"a role includes itself, not strictly", c, "admin", "admin", true, false},
		{"same permissions, not strictly", c2, "admin-twin", "admin", true, false},
		{"disjoint", c, "client", "member", false, false},
		{"unknown a", c, "ghost", "owner", false, false},
		{"unknown b", c, "member", "ghost", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.cat.Includes(tc.a, tc.b); got != tc.includes {
				t.Errorf("Includes(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.includes)
			}
			if got := tc.cat.StrictlyIncludes(tc.a, tc.b); got != tc.strc {
				t.Errorf("StrictlyIncludes(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.strc)
			}
		})
	}
}

func TestCatalog_Lookups(t *testing.T) {
	t.Parallel()

	c := mustCatalog(t, owner(), admin(), member())
	if got := c.Permissions("ghost"); got != nil {
		t.Fatalf("an unknown role has no permissions, got %v", got)
	}
	perms := c.Permissions("member")
	if !slices.Equal(perms, []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}) {
		t.Fatalf("member permissions = %v", perms)
	}
	perms[0] = authz.PermTenantOwn
	if slices.Contains(c.Permissions("member"), authz.PermTenantOwn) {
		t.Fatal("Permissions must return a copy")
	}
	if got := c.RolesRequiringMFA(); !slices.Equal(got, []string{"owner"}) {
		t.Fatalf("RolesRequiringMFA = %v", got)
	}
	if got := c.Unknown([]string{"member", "ghost", "lawyer", "ghost", "owner"}); !slices.Equal(got, []string{"ghost", "lawyer"}) {
		t.Fatalf("Unknown = %v", got)
	}
	if r, ok := c.Role("owner"); !ok || !r.RequiresMFA || r.MFAMaxAge != 12*time.Hour {
		t.Fatalf("Role(owner) = %+v, %v", r, ok)
	}
	if _, ok := c.Role("ghost"); ok {
		t.Fatal("Role(ghost) must not exist")
	}
}

func TestRole_MFASatisfied(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	o := owner()
	tests := []struct {
		name string
		role authz.Role
		mfa  time.Time
		want bool
	}{
		{"no MFA needed", member(), time.Time{}, true},
		{"never passed MFA", o, time.Time{}, false},
		{"recent", o, now.Add(-time.Hour), true},
		{"exactly the maximum", o, now.Add(-12 * time.Hour), true},
		{"older than the maximum", o, now.Add(-12*time.Hour - time.Second), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.role.MFASatisfied(tc.mfa, now); got != tc.want {
				t.Fatalf("MFASatisfied = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCatalog_Names(t *testing.T) {
	t.Parallel()

	c, err := authz.NewCatalog(owner(), authz.Role{Name: "zeta"}, authz.Role{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Names(); !slices.Equal(got, []string{"alpha", "owner", "zeta"}) {
		t.Fatalf("Names = %v", got)
	}
}
