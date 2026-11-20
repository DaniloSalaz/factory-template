// Package authz holds the permissions of the platform, the app's role catalog and the
// check that use cases make before acting (design, section 8). Only Go code decides
// permissions: the database stores roles as text and never evaluates them.
package authz

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
)

// Platform permissions. Apps add their own, with the same "resource:action" form.
const (
	PermMembersRead   authctx.Permission = "members:read"
	PermMembersInvite authctx.Permission = "members:invite"
	PermMembersManage authctx.Permission = "members:manage"
	PermAuditRead     authctx.Permission = "audit:read"
	// PermTenantOwn is reserved for RoleOwner. Since no other role holds it, no role can
	// include owner, so the inclusion rule alone keeps anyone else from granting it.
	PermTenantOwn authctx.Permission = "tenant:own"
)

// PlatformPermissions returns every permission of the platform, the scope included.
func PlatformPermissions() []authctx.Permission {
	return []authctx.Permission{
		PermMembersRead, PermMembersInvite, PermMembersManage, PermAuditRead, PermTenantOwn,
		authctx.PermScopeTenant,
	}
}

// RoleOwner is the platform's reserved role: every app has it, under this name.
const RoleOwner = "owner"

// Role is a role of the app's catalog.
type Role struct {
	Name        string
	Permissions []authctx.Permission
	// RequiresMFA roles need an MFA no older than MFAMaxAge to act in a tenant.
	RequiresMFA bool
	MFAMaxAge   time.Duration
}

// MFASatisfied reports whether a user whose last MFA was at mfaAuthTime (zero: never)
// may act with this role at now.
func (r Role) MFASatisfied(mfaAuthTime, now time.Time) bool {
	if !r.RequiresMFA {
		return true
	}
	if mfaAuthTime.IsZero() {
		return false
	}
	return now.Sub(mfaAuthTime) <= r.MFAMaxAge
}

// Catalog is the app's validated set of roles. It is immutable once built.
type Catalog struct {
	roles map[string]catalogRole
}

type catalogRole struct {
	Role
	perms map[authctx.Permission]struct{}
}

// NewCatalog validates roles and builds the catalog. The app does not start with an
// invalid one: owner must exist under its name and hold every platform permission and
// every permission of the other roles, and no other role may hold tenant:own.
func NewCatalog(roles ...Role) (*Catalog, error) {
	c := &Catalog{roles: make(map[string]catalogRole, len(roles))}
	var errs []error
	for _, r := range roles {
		if err := validateRole(r); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := c.roles[r.Name]; dup {
			errs = append(errs, fmt.Errorf("role %q is declared twice", r.Name))
			continue
		}
		set := make(map[authctx.Permission]struct{}, len(r.Permissions))
		for _, p := range r.Permissions {
			set[p] = struct{}{}
		}
		r.Permissions = slices.Sorted(maps.Keys(set))
		c.roles[r.Name] = catalogRole{Role: r, perms: set}
	}
	errs = append(errs, c.validateOwner()...)
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("invalid role catalog: %w", err)
	}
	return c, nil
}

func validateRole(r Role) error {
	if !validName(r.Name) {
		return fmt.Errorf("role name %q must be lower-case letters, digits, '_' or '-', starting with a letter", r.Name)
	}
	if r.Name == authctx.SystemRole {
		return fmt.Errorf("role %q is reserved for system jobs", r.Name)
	}
	for _, p := range r.Permissions {
		if !validPermission(p) {
			return fmt.Errorf("role %q: permission %q is not of the form resource:action", r.Name, p)
		}
		if p == PermTenantOwn && r.Name != RoleOwner {
			return fmt.Errorf("role %q has %s, which only %q may hold", r.Name, PermTenantOwn, RoleOwner)
		}
	}
	if r.RequiresMFA && r.MFAMaxAge <= 0 {
		return fmt.Errorf("role %q requires MFA but has no MFAMaxAge", r.Name)
	}
	if !r.RequiresMFA && r.MFAMaxAge != 0 {
		return fmt.Errorf("role %q has an MFAMaxAge but does not require MFA", r.Name)
	}
	return nil
}

func (c *Catalog) validateOwner() []error {
	owner, ok := c.roles[RoleOwner]
	if !ok {
		return []error{fmt.Errorf("role %q is missing: it is reserved by the platform and cannot be renamed or removed", RoleOwner)}
	}
	var errs []error
	for _, p := range PlatformPermissions() {
		if _, has := owner.perms[p]; !has {
			errs = append(errs, fmt.Errorf("role %q lacks platform permission %s", RoleOwner, p))
		}
	}
	// Owner can act on every member and grant every role, so it includes them all.
	for _, name := range slices.Sorted(maps.Keys(c.roles)) {
		if !c.Includes(name, RoleOwner) {
			errs = append(errs, fmt.Errorf("role %q has permissions that %q lacks", name, RoleOwner))
		}
	}
	return errs
}

func validName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func validPermission(p authctx.Permission) bool {
	resource, action, ok := strings.Cut(string(p), ":")
	return ok && validName(resource) && validName(action)
}

// Role returns the named role, if the catalog has it.
func (c *Catalog) Role(name string) (Role, bool) {
	r, ok := c.roles[name]
	if !ok {
		return Role{}, false
	}
	out := r.Role
	out.Permissions = slices.Clone(r.Permissions)
	return out, true
}

// Permissions returns the permissions of the named role. An unknown role has none.
func (c *Catalog) Permissions(name string) []authctx.Permission {
	r, ok := c.roles[name]
	if !ok {
		return nil
	}
	return slices.Clone(r.Permissions)
}

// Includes reports whether role a's permissions are a subset of role b's. It is false
// if either role is unknown, so an unknown role can never be granted.
func (c *Catalog) Includes(a, b string) bool {
	ra, okA := c.roles[a]
	rb, okB := c.roles[b]
	if !okA || !okB {
		return false
	}
	for p := range ra.perms {
		if _, ok := rb.perms[p]; !ok {
			return false
		}
	}
	return true
}

// StrictlyIncludes reports whether role a's permissions are a proper subset of role
// b's: b can do everything a can, and more.
func (c *Catalog) StrictlyIncludes(a, b string) bool {
	return c.Includes(a, b) && !c.Includes(b, a)
}

// RolesRequiringMFA returns the names of the roles that require MFA, sorted.
func (c *Catalog) RolesRequiringMFA() []string {
	var out []string
	for name, r := range c.roles {
		if r.RequiresMFA {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Unknown returns, sorted and without repeats, the names that are not in the catalog.
func (c *Catalog) Unknown(names []string) []string {
	var out []string
	for _, n := range names {
		if _, ok := c.roles[n]; !ok {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
