package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"chaskicode.dev/saas/core/kit/apperr"
)

// Tenants and members (auth-y-tenancy §5.5 and §5.7): what a slug and a name may be, and who may
// change or remove whom. The hierarchy is decided here, on the permissions of the
// app's catalog, never on role names written by hand; the database only keeps an owner
// in every tenant.

// Slug lengths. The database CHECK of migration 00005 enforces the same.
const (
	SlugMinLen = 3
	SlugMaxLen = 40
)

// TenantNameMaxLen is the longest tenant name, in characters.
const TenantNameMaxLen = 100

// ValidateSlug checks a new tenant's slug: SlugMinLen to SlugMaxLen lower-case letters,
// digits and hyphens, neither first nor last a hyphen, and not reserved. Uniqueness is
// the database's (tenant.slug_taken).
func ValidateSlug(slug string) error {
	if len(slug) < SlugMinLen || len(slug) > SlugMaxLen || slug[0] == '-' || slug[len(slug)-1] == '-' {
		return ErrSlugInvalid()
	}
	for _, c := range []byte(slug) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return ErrSlugInvalid()
		}
	}
	if reservedSlug(slug) {
		return ErrSlugReserved()
	}
	return nil
}

// reservedSlug reports the slugs kept for the core: hostnames it may serve (api,
// www, app…), paths of its own and names that would mislead users.
func reservedSlug(slug string) bool {
	switch slug {
	case "api", "www", "admin", "app", "staging", "auth", "static", "mail",
		"assets", "cdn", "dev", "docs", "ftp", "help", "imap", "login", "logout", "me",
		"pop", "preview", "prod", "production", "root", "settings", "signup", "smtp",
		"status", "support", "system", "tenant", "tenants", "test", "invite", "invitations":
		return true
	}
	return false
}

// NormalizeTenantName trims name and checks it: 1 to TenantNameMaxLen characters, no
// control characters.
func NormalizeTenantName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > TenantNameMaxLen || !utf8.ValidString(name) ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return "", ErrTenantNameInvalid()
	}
	return name, nil
}

// Role is a role as the hierarchy sees it: its permissions in the catalog (none if
// unknown) and whether it is the core's owner.
type Role struct {
	Name  string
	Known bool
	Owner bool
	// CanInvite: the role holds members:invite.
	CanInvite bool
	// CanManage: the role holds members:manage.
	CanManage   bool
	Permissions []string
}

// Member is a member of the tenant with their role.
type Member struct {
	UserID string
	Role   Role
}

// CheckRoleChange decides whether actor may give target the role to (auth-y-tenancy §5.7.3):
// nobody changes their own role, to must exist in the catalog, the actor acts only on a
// member their role manages (canManage) and assigns only a role CanAssign allows.
func CheckRoleChange(actor, target Member, to Role) error {
	if actor.UserID == target.UserID {
		return ErrMemberSelfChange()
	}
	if !to.Known {
		return ErrUnknownRole()
	}
	if !canManage(actor.Role, target.Role) {
		return ErrMemberTargetForbidden()
	}
	if !CanAssign(actor.Role, to) {
		return ErrRoleNotAssignable()
	}
	return nil
}

// CanAssign reports whether a member with role actor may give some member the role
// (auth-y-tenancy §5.7.2): actor holds members:manage, role exists and is included in actor's,
// and owner is granted only by an owner; tenant:own already keeps any other role from
// including it, and this says it explicitly. Whom it may be given to is canManage's.
func CanAssign(actor, role Role) bool {
	return actor.CanManage && role.Known && included(role.Permissions, actor.Permissions) && (!role.Owner || actor.Owner)
}

// CanOffer reports whether a member with role actor may offer role in an invitation
// (auth-y-tenancy §5.6.1): actor holds members:invite, role exists and is included in actor's,
// and owner is offered only by an owner. The same rule decides which pending invitations
// survive their inviter's change of role (auth-y-tenancy §5.6.4).
func CanOffer(actor, role Role) bool {
	return actor.CanInvite && role.Known && included(role.Permissions, actor.Permissions) && (!role.Owner || actor.Owner)
}

// CheckRemoval decides whether actor may remove target: the same hierarchy as a role
// change. Leaving the tenant is another operation, so removing oneself is refused.
func CheckRemoval(actor, target Member) error {
	if actor.UserID == target.UserID {
		return ErrMemberSelfChange()
	}
	if !canManage(actor.Role, target.Role) {
		return ErrMemberTargetForbidden()
	}
	return nil
}

// canManage reports whether actor may act on a member with role target: target's
// permissions are a proper subset of actor's, or both are owners. A role unknown to the
// catalog has no permissions, so anyone who manages members manages it.
func canManage(actor, target Role) bool {
	if actor.Owner && target.Owner {
		return true
	}
	return included(target.Permissions, actor.Permissions) && !included(actor.Permissions, target.Permissions)
}

// included reports whether every permission of a is in b.
func included(a, b []string) bool {
	set := make(map[string]struct{}, len(b))
	for _, p := range b {
		set[p] = struct{}{}
	}
	for _, p := range a {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}

// ErrSlugInvalid is returned for a slug that is not SlugMinLen to SlugMaxLen
// lower-case letters, digits and inner hyphens.
func ErrSlugInvalid() error {
	return apperr.Validation("tenant.slug_invalid",
		"The slug must be 3 to 40 lower-case letters, digits and hyphens, not starting or ending with a hyphen.",
		apperr.FieldError{Field: "slug", Code: "invalid", Message: "3 to 40 of a-z, 0-9 and -, not at either end"})
}

// ErrSlugReserved is returned for a slug the core keeps for itself.
func ErrSlugReserved() error {
	return apperr.Validation("tenant.slug_reserved", "This slug is reserved; choose another one.",
		apperr.FieldError{Field: "slug", Code: "reserved", Message: "reserved"})
}

// ErrTenantNameInvalid is returned for an empty, too long or malformed tenant name.
func ErrTenantNameInvalid() error {
	return apperr.Validation("tenant.name_invalid", "The name must be 1 to 100 characters.",
		apperr.FieldError{Field: "name", Code: "invalid", Message: "1 to 100 characters"})
}

// ErrMemberNotFound is returned when the user is not a member of the current tenant,
// whether or not the user exists, so a caller cannot probe other tenants' users.
func ErrMemberNotFound() error {
	return apperr.NotFound("membership.not_found", "The user is not a member of this tenant.")
}

// ErrMemberSelfChange is returned when someone changes their own role or removes
// themselves: another member does it, or they leave (DELETE /tenant/members/me).
func ErrMemberSelfChange() error {
	return apperr.Forbidden("membership.self_change",
		"You cannot change your own role or remove yourself; to leave the tenant, use DELETE /tenant/members/me.")
}

// ErrMemberTargetForbidden is returned when the member's role is not below the
// actor's.
func ErrMemberTargetForbidden() error {
	return apperr.Forbidden("membership.target_forbidden", "You can only manage members whose role is below yours.")
}

// ErrRoleNotAssignable is returned when the new role is not included in the actor's.
func ErrRoleNotAssignable() error {
	return apperr.Forbidden("membership.role_not_assignable", "You can only assign roles included in yours.")
}

// ErrUnknownRole is returned when the new role is not in the app's catalog: an unknown
// role is never assigned (auth-y-tenancy §8).
func ErrUnknownRole() error {
	return apperr.Validation("membership.unknown_role", "The role does not exist.",
		apperr.FieldError{Field: "role", Code: "unknown", Message: "not a role of the catalog"})
}

// ErrCursorInvalid is returned for a page cursor this API did not issue.
func ErrCursorInvalid() error {
	return apperr.BadRequest("membership.cursor_invalid", "The cursor is not valid; start from the first page.")
}
