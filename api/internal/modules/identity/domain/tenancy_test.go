package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSlug(t *testing.T) {
	t.Parallel()

	valid := []string{"abc", "acme", "acme-legal", "a-b", "123", "a1-2b", strings.Repeat("a", 40)}
	for _, s := range valid {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{
		"", "ab", strings.Repeat("a", 41), "-acme", "acme-", "Acme", "acme_legal", "acme.legal",
		"acme legal", "ácme", "acme/x", "-",
	}
	for _, s := range invalid {
		if err := ValidateSlug(s); !errors.Is(err, ErrSlugInvalid()) {
			t.Errorf("ValidateSlug(%q) = %v, want %v", s, err, ErrSlugInvalid())
		}
	}
	for _, s := range []string{"api", "www", "admin", "app", "staging", "auth", "static", "mail"} {
		if err := ValidateSlug(s); !errors.Is(err, ErrSlugReserved()) {
			t.Errorf("ValidateSlug(%q) = %v, want %v", s, err, ErrSlugReserved())
		}
	}
}

func TestNormalizeTenantName(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"Acme":                   "Acme",
		"  Despacho Pérez  ":     "Despacho Pérez",
		strings.Repeat("ñ", 100): strings.Repeat("ñ", 100),
		"\tAcme & Co.\n":         "Acme & Co.",
	} {
		got, err := NormalizeTenantName(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeTenantName(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "   ", strings.Repeat("ñ", 101), "Acme\x00", "Ac\u0007me", "\xff\xfe"} {
		if _, err := NormalizeTenantName(raw); !errors.Is(err, ErrTenantNameInvalid()) {
			t.Errorf("NormalizeTenantName(%q) = %v, want %v", raw, err, ErrTenantNameInvalid())
		}
	}
}

// The roles of the example catalog, plus a role unknown to it and "coowner": every
// permission of owner but tenant:own.
var (
	ownerRole  = Role{Name: "owner", Known: true, Owner: true, Permissions: []string{"members:read", "members:manage", "audit:read", "tenant:update", "tenant:own", "scope:tenant"}}
	coowner    = Role{Name: "coowner", Known: true, Permissions: []string{"members:read", "members:manage", "audit:read", "tenant:update", "scope:tenant"}}
	adminRole  = Role{Name: "admin", Known: true, Permissions: []string{"members:read", "members:manage", "audit:read", "tenant:update", "scope:tenant"}}
	memberRole = Role{Name: "member", Known: true, Permissions: []string{"members:read", "scope:tenant"}}
	clientRole = Role{Name: "client", Known: true}
	ghostRole  = Role{Name: "ghost"}
)

func as(id string, r Role) Member { return Member{UserID: id, Role: r} }

func TestCheckRoleChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		actor, target Member
		to            Role
		want          error
	}{
		{"owner promotes a member to admin", as("a", ownerRole), as("b", memberRole), adminRole, nil},
		{"owner makes a member owner", as("a", ownerRole), as("b", memberRole), ownerRole, nil},
		{"owner demotes another owner", as("a", ownerRole), as("b", ownerRole), adminRole, nil},
		{"admin promotes a member to admin", as("a", adminRole), as("b", memberRole), adminRole, nil},
		{"admin demotes a member to client", as("a", adminRole), as("b", memberRole), clientRole, nil},
		{"admin fixes an unknown role", as("a", adminRole), as("b", ghostRole), memberRole, nil},

		{"nobody changes their own role", as("a", ownerRole), as("a", ownerRole), adminRole, ErrMemberSelfChange()},
		{"admin does not change their own role", as("a", adminRole), as("a", adminRole), memberRole, ErrMemberSelfChange()},
		{"admin cannot touch an owner", as("a", adminRole), as("b", ownerRole), memberRole, ErrMemberTargetForbidden()},
		{"admin cannot touch another admin", as("a", adminRole), as("b", adminRole), memberRole, ErrMemberTargetForbidden()},
		{"admin cannot name owners", as("a", adminRole), as("b", memberRole), ownerRole, ErrRoleNotAssignable()},
		{"a role like owner but tenant:own cannot name owners", as("a", coowner), as("b", memberRole), ownerRole, ErrRoleNotAssignable()},
		{"member cannot manage a client", as("a", memberRole), as("b", clientRole), adminRole, ErrRoleNotAssignable()},
		{"member cannot touch another member", as("a", memberRole), as("b", memberRole), clientRole, ErrMemberTargetForbidden()},
		{"an unknown role is never assigned", as("a", ownerRole), as("b", memberRole), ghostRole, ErrUnknownRole()},
		{"an unknown actor role manages nobody", as("a", ghostRole), as("b", clientRole), clientRole, ErrMemberTargetForbidden()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckRoleChange(tc.actor, tc.target, tc.to)
			if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("CheckRoleChange = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckRemoval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		actor, target Member
		want          error
	}{
		{"owner removes another owner", as("a", ownerRole), as("b", ownerRole), nil},
		{"owner removes an admin", as("a", ownerRole), as("b", adminRole), nil},
		{"admin removes a member", as("a", adminRole), as("b", memberRole), nil},
		{"admin removes a member with an unknown role", as("a", adminRole), as("b", ghostRole), nil},
		{"admin cannot remove an owner", as("a", adminRole), as("b", ownerRole), ErrMemberTargetForbidden()},
		{"admin cannot remove another admin", as("a", adminRole), as("b", adminRole), ErrMemberTargetForbidden()},
		{"coowner cannot remove an owner", as("a", coowner), as("b", ownerRole), ErrMemberTargetForbidden()},
		{"nobody removes themselves", as("a", ownerRole), as("a", ownerRole), ErrMemberSelfChange()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckRemoval(tc.actor, tc.target)
			if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("CheckRemoval = %v, want %v", err, tc.want)
			}
		})
	}
}
