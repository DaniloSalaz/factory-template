// Package authctx carries the request's identity through context.Context, at the two
// levels that exist at different moments of a request (auth-y-tenancy §5.4 and §7.5):
//
//   - User: the authenticated user, set by the session middleware before any tenant is
//     known;
//   - Principal: the user acting in a tenant, with its role and permissions, set by the
//     tenant middleware (or by a job worker).
//
// platform/kit/db reads them to set the RLS variables, so both are immutable once built:
// their fields are unexported and the permission set is copied in and out. It depends
// only on the standard library.
package authctx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Permission is a "resource:action" permission, or PermScopeTenant.
type Permission string

// PermScopeTenant gives a principal the tenant scope; without it the scope is own.
const PermScopeTenant Permission = "scope:tenant"

// Scope is the row scope a principal has inside its tenant; platform/kit/db hands it to RLS.
type Scope string

// The two scopes of the design (auth-y-tenancy §7.2).
const (
	ScopeTenant Scope = "tenant"
	ScopeOwn    Scope = "own"
)

// SystemUserID is the reserved, disabled user that system jobs act as (auth-y-tenancy §4.1).
const SystemUserID = "00000000-0000-7000-8000-000000000001"

// SystemRole is the role of SystemPrincipal. NewPrincipal refuses it, so a system
// principal can only be built on purpose.
const SystemRole = "system"

const nilUUID = "00000000-0000-0000-0000-000000000000"

// User is the authenticated user of a request, before it picks a tenant.
type User struct {
	id          string
	mfaAuthTime time.Time
}

// NewUser builds a User. mfaAuthTime is when the user last passed MFA, as reported by the
// identity provider; the zero time means never.
func NewUser(id string, mfaAuthTime time.Time) (User, error) {
	id, err := parseUUID("user id", id)
	if err != nil {
		return User{}, err
	}
	return User{id: id, mfaAuthTime: mfaAuthTime}, nil
}

// ID returns the user's id, a lower-case UUID.
func (u User) ID() string { return u.id }

// MFAAuthTime returns when the user last passed MFA, if ever.
func (u User) MFAAuthTime() (time.Time, bool) {
	return u.mfaAuthTime, !u.mfaAuthTime.IsZero()
}

// Principal is a user acting in a tenant. Its scope follows from PermScopeTenant, so a
// principal cannot claim the tenant scope without holding that permission.
type Principal struct {
	userID   string
	tenantID string
	role     string
	perms    map[Permission]struct{}
}

// NewPrincipal builds the principal of a member of a tenant. It refuses the system user
// and the system role: those are only for SystemPrincipal.
func NewPrincipal(userID, tenantID, role string, perms ...Permission) (Principal, error) {
	if role == "" {
		return Principal{}, errors.New("principal role is empty")
	}
	if role == SystemRole {
		return Principal{}, fmt.Errorf("role %q is reserved for SystemPrincipal", SystemRole)
	}
	p, err := newPrincipal(userID, tenantID, role, perms)
	if err != nil {
		return Principal{}, err
	}
	if p.userID == SystemUserID {
		return Principal{}, errors.New("the system user acts only through SystemPrincipal")
	}
	return p, nil
}

// SystemPrincipal builds the principal of a system job in a tenant (auth-y-tenancy §7.6): the
// system user, the system role, the tenant scope and exactly the permissions given.
func SystemPrincipal(tenantID string, perms ...Permission) (Principal, error) {
	return newPrincipal(SystemUserID, tenantID, SystemRole, append(slices.Clone(perms), PermScopeTenant))
}

func newPrincipal(userID, tenantID, role string, perms []Permission) (Principal, error) {
	userID, err := parseUUID("user id", userID)
	if err != nil {
		return Principal{}, err
	}
	tenantID, err = parseUUID("tenant id", tenantID)
	if err != nil {
		return Principal{}, err
	}
	set := make(map[Permission]struct{}, len(perms))
	for _, perm := range perms {
		if perm == "" {
			return Principal{}, errors.New("empty permission")
		}
		set[perm] = struct{}{}
	}
	return Principal{userID: userID, tenantID: tenantID, role: role, perms: set}, nil
}

// UserID returns the acting user's id, a lower-case UUID.
func (p Principal) UserID() string { return p.userID }

// TenantID returns the tenant's id, a lower-case UUID.
func (p Principal) TenantID() string { return p.tenantID }

// Role returns the principal's role in the tenant.
func (p Principal) Role() string { return p.role }

// Has reports whether the principal holds perm.
func (p Principal) Has(perm Permission) bool {
	_, ok := p.perms[perm]
	return ok
}

// Scope returns ScopeTenant if the principal holds PermScopeTenant, ScopeOwn otherwise.
func (p Principal) Scope() Scope {
	if p.Has(PermScopeTenant) {
		return ScopeTenant
	}
	return ScopeOwn
}

// Permissions returns a sorted copy of the principal's permissions.
func (p Principal) Permissions() []Permission {
	out := make([]Permission, 0, len(p.perms))
	for perm := range p.perms {
		out = append(out, perm)
	}
	slices.Sort(out)
	return out
}

// IsSystem reports whether the principal was built by SystemPrincipal.
func (p Principal) IsSystem() bool { return p.userID == SystemUserID && p.role == SystemRole }

type ctxKey int

const (
	userKey ctxKey = iota
	principalKey
)

// WithUser returns a context carrying the authenticated user.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFrom returns the authenticated user stored in ctx, if any.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey).(User)
	return u, ok && u.id != ""
}

// WithPrincipal returns a context carrying the principal.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the principal stored in ctx, if any. A zero Principal (not built
// by a constructor) does not count.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok && p.userID != "" && p.tenantID != ""
}

// ParseID returns s as a lower-case UUID if it is one (canonical form, either case, not
// the nil UUID), the form the identifiers of this package take.
func ParseID(s string) (string, error) {
	return parseUUID("id", s)
}

// parseUUID accepts a UUID in its canonical 8-4-4-4-12 hex form, in either case, and
// returns it lower-cased. The nil UUID is refused: it is never a real user or tenant.
func parseUUID(what, s string) (string, error) {
	if len(s) != 36 {
		return "", fmt.Errorf("%s %q is not a UUID", what, s)
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return "", fmt.Errorf("%s %q is not a UUID", what, s)
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return "", fmt.Errorf("%s %q is not a UUID", what, s)
			}
		}
	}
	s = strings.ToLower(s)
	if s == nilUUID {
		return "", fmt.Errorf("%s is the nil UUID", what)
	}
	return s, nil
}
