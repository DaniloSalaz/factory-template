package authctx_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"chaskicode.dev/saas/platform/authctx"
)

const (
	userID   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	tenantID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
)

func TestNewUser(t *testing.T) {
	t.Parallel()

	mfa := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	u, err := authctx.NewUser("0190A1B2-C3D4-7E5F-8A9B-0C1D2E3F4A5B", mfa)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID() != userID {
		t.Errorf("ID() = %q, want the lower-cased %q", u.ID(), userID)
	}
	if got, ok := u.MFAAuthTime(); !ok || !got.Equal(mfa) {
		t.Errorf("MFAAuthTime() = %v, %v; want %v, true", got, ok, mfa)
	}

	noMFA, err := authctx.NewUser(userID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := noMFA.MFAAuthTime(); ok {
		t.Error("MFAAuthTime() reports MFA for a zero time")
	}
}

func TestInvalidUUIDs(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"",
		"not-a-uuid",
		"0190a1b2c3d47e5f8a9b0c1d2e3f4a5b",       // no dashes
		"{0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b}", // braces
		"0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5g",   // not hex
		"0190a1b2-c3d4-7e5f-8a9b0-c1d2e3f4a5b",   // dash misplaced
		"00000000-0000-0000-0000-000000000000",   // nil UUID
		"0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b'; --",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			if _, err := authctx.NewUser(id, time.Time{}); err == nil {
				t.Errorf("NewUser(%q) accepted it", id)
			}
			if _, err := authctx.NewPrincipal(id, tenantID, "member"); err == nil {
				t.Errorf("NewPrincipal user %q accepted it", id)
			}
			if _, err := authctx.NewPrincipal(userID, id, "member"); err == nil {
				t.Errorf("NewPrincipal tenant %q accepted it", id)
			}
			if _, err := authctx.SystemPrincipal(id); err == nil {
				t.Errorf("SystemPrincipal(%q) accepted it", id)
			}
			if _, err := authctx.ParseID(id); err == nil {
				t.Errorf("ParseID(%q) accepted it", id)
			}
		})
	}
}

func TestParseID(t *testing.T) {
	t.Parallel()

	got, err := authctx.ParseID("0190A1B2-C3D4-7E5F-8A9B-0C1D2E3F4A5B")
	if err != nil || got != "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatalf("ParseID = %q, %v; want the lower-case UUID", got, err)
	}
}

func TestNewPrincipal(t *testing.T) {
	t.Parallel()

	p, err := authctx.NewPrincipal(userID, tenantID, "lawyer", "cases:read", "cases:write", "cases:read")
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID() != userID || p.TenantID() != tenantID || p.Role() != "lawyer" {
		t.Errorf("got %q %q %q", p.UserID(), p.TenantID(), p.Role())
	}
	if !p.Has("cases:read") || !p.Has("cases:write") || p.Has("members:manage") {
		t.Errorf("Has is wrong for %v", p.Permissions())
	}
	if want := []authctx.Permission{"cases:read", "cases:write"}; !slices.Equal(p.Permissions(), want) {
		t.Errorf("Permissions() = %v, want %v", p.Permissions(), want)
	}
	if p.IsSystem() {
		t.Error("a member principal reports IsSystem")
	}
}

func TestNewPrincipalRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		user   string
		role   string
		perms  []authctx.Permission
		reason string
	}{
		{"empty role", userID, "", nil, "role is required"},
		{"system role", userID, authctx.SystemRole, nil, "reserved for SystemPrincipal"},
		{"system user", authctx.SystemUserID, "admin", nil, "reserved for SystemPrincipal"},
		{"empty permission", userID, "member", []authctx.Permission{""}, "permissions are never empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := authctx.NewPrincipal(tc.user, tenantID, tc.role, tc.perms...); err == nil {
				t.Fatalf("accepted: %s", tc.reason)
			}
		})
	}
}

// The scope is derived from scope:tenant, never given: a principal cannot have the
// tenant scope without the permission.
func TestPrincipalScope(t *testing.T) {
	t.Parallel()

	own, err := authctx.NewPrincipal(userID, tenantID, "client", "cases:read")
	if err != nil {
		t.Fatal(err)
	}
	if own.Scope() != authctx.ScopeOwn {
		t.Errorf("without scope:tenant: Scope() = %q, want own", own.Scope())
	}
	tenant, err := authctx.NewPrincipal(userID, tenantID, "lawyer", "cases:read", authctx.PermScopeTenant)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Scope() != authctx.ScopeTenant {
		t.Errorf("with scope:tenant: Scope() = %q, want tenant", tenant.Scope())
	}
}

func TestSystemPrincipal(t *testing.T) {
	t.Parallel()

	p, err := authctx.SystemPrincipal(tenantID, "reports:generate")
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID() != authctx.SystemUserID || p.Role() != authctx.SystemRole || !p.IsSystem() {
		t.Errorf("got user %q role %q system %v", p.UserID(), p.Role(), p.IsSystem())
	}
	if p.Scope() != authctx.ScopeTenant {
		t.Errorf("Scope() = %q, want tenant", p.Scope())
	}
	want := []authctx.Permission{"reports:generate", authctx.PermScopeTenant}
	if !slices.Equal(p.Permissions(), want) {
		t.Errorf("Permissions() = %v, want exactly %v", p.Permissions(), want)
	}
}

// Neither the slice passed in nor the one returned can change a principal.
func TestPrincipalIsImmutable(t *testing.T) {
	t.Parallel()

	in := []authctx.Permission{"cases:read"}
	p, err := authctx.NewPrincipal(userID, tenantID, "client", in...)
	if err != nil {
		t.Fatal(err)
	}
	in[0] = authctx.PermScopeTenant
	out := p.Permissions()
	out[0] = authctx.PermScopeTenant

	if p.Has(authctx.PermScopeTenant) || p.Scope() != authctx.ScopeOwn {
		t.Fatalf("principal changed through a slice: %v", p.Permissions())
	}

	sysIn := []authctx.Permission{"reports:generate"}
	sys, err := authctx.SystemPrincipal(tenantID, sysIn...)
	if err != nil {
		t.Fatal(err)
	}
	sysIn[0] = "members:manage"
	if sys.Has("members:manage") {
		t.Fatal("system principal changed through the slice passed in")
	}
}

func TestContext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if _, ok := authctx.UserFrom(ctx); ok {
		t.Error("UserFrom found a user in an empty context")
	}
	if _, ok := authctx.PrincipalFrom(ctx); ok {
		t.Error("PrincipalFrom found a principal in an empty context")
	}
	if _, ok := authctx.UserFrom(authctx.WithUser(ctx, authctx.User{})); ok {
		t.Error("UserFrom accepted a zero User")
	}
	if _, ok := authctx.PrincipalFrom(authctx.WithPrincipal(ctx, authctx.Principal{})); ok {
		t.Error("PrincipalFrom accepted a zero Principal")
	}

	u, err := authctx.NewUser(userID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := authctx.NewPrincipal(userID, tenantID, "member")
	if err != nil {
		t.Fatal(err)
	}
	ctx = authctx.WithPrincipal(authctx.WithUser(ctx, u), p)
	if got, ok := authctx.UserFrom(ctx); !ok || got.ID() != userID {
		t.Errorf("UserFrom = %v, %v", got, ok)
	}
	if got, ok := authctx.PrincipalFrom(ctx); !ok || got.TenantID() != tenantID {
		t.Errorf("PrincipalFrom = %v, %v", got, ok)
	}
}
