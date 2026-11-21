package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"slices"

	"chaskicode.dev/saas/modules/identity/domain"
	"chaskicode.dev/saas/platform/authctx"
	"chaskicode.dev/saas/platform/authz"
	"chaskicode.dev/saas/platform/clientip"
	"chaskicode.dev/saas/platform/ratelimit"
)

// Authorizer is the permission check of the use cases (authz.Authorizer).
type Authorizer interface {
	Require(ctx context.Context, perm authctx.Permission) error
}

// Tenants creates and renames tenants (design, 5.5).
type Tenants struct {
	store      TenantStore
	authz      Authorizer
	maxPerUser int
	limiter    ratelimit.Limiter
}

// NewTenants builds Tenants. maxPerUser is how many tenants a user may create in all;
// limiter caps how often they may try (design, section 6).
func NewTenants(store TenantStore, authorizer Authorizer, maxPerUser int, limiter ratelimit.Limiter) *Tenants {
	return &Tenants{store: store, authz: authorizer, maxPerUser: maxPerUser, limiter: limiter}
}

// CreatedTenant is a tenant just created, with its creator as owner.
type CreatedTenant struct {
	Tenant Tenant
	Role   string
}

// Create creates a tenant with user as its owner. It needs no permission and no MFA:
// if the owner role requires MFA, acting in the tenant does, from the next request.
func (t *Tenants) Create(ctx context.Context, user authctx.User, name, slug string) (CreatedTenant, error) {
	if err := allow(ctx, t.limiter, "tenant:"+user.ID()); err != nil {
		return CreatedTenant{}, err
	}
	name, err := domain.NormalizeTenantName(name)
	if err != nil {
		return CreatedTenant{}, err
	}
	if err := domain.ValidateSlug(slug); err != nil {
		return CreatedTenant{}, err
	}
	var id string
	err = t.store.AsUser(ctx, user.ID(), func(ctx context.Context, r TenantRepo) error {
		var err error
		id, err = r.CreateTenant(ctx, name, slug, t.maxPerUser, ipPrefix(ctx))
		return err
	})
	if err != nil {
		return CreatedTenant{}, fmt.Errorf("create tenant: %w", err)
	}
	return CreatedTenant{Tenant: Tenant{ID: id, Slug: slug, Name: name}, Role: authz.RoleOwner}, nil
}

// Rename renames the principal's tenant (tenant:update) and records tenant.updated with
// the previous and the new name.
func (t *Tenants) Rename(ctx context.Context, name string) (Tenant, error) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return Tenant{}, domain.ErrTenantForbidden()
	}
	if err := t.authz.Require(ctx, authz.PermTenantUpdate); err != nil {
		return Tenant{}, fmt.Errorf("rename tenant: %w", err)
	}
	name, err := domain.NormalizeTenantName(name)
	if err != nil {
		return Tenant{}, err
	}
	var out Tenant
	err = t.store.InTenant(ctx, p, func(ctx context.Context, r TenantRepo) error {
		previous, renamed, err := r.RenameTenant(ctx, p.TenantID(), name)
		if err != nil {
			return err
		}
		out = renamed
		if previous == name {
			return nil
		}
		return r.RecordTenantAudit(ctx, p.TenantID(), AuditEvent{
			Action: AuditTenantUpdated, TargetType: "tenant", TargetID: p.TenantID(),
			Metadata: map[string]string{"old_name": previous, "new_name": name}, IPPrefix: ipPrefix(ctx),
		})
	})
	if err != nil {
		return Tenant{}, fmt.Errorf("rename tenant: %w", err)
	}
	return out, nil
}

// Members lists and manages the members of the principal's tenant (design, 5.7).
type Members struct {
	store   TenantStore
	authz   Authorizer
	catalog *authz.Catalog
}

// NewMembers builds Members.
func NewMembers(store TenantStore, authorizer Authorizer, catalog *authz.Catalog) *Members {
	return &Members{store: store, authz: authorizer, catalog: catalog}
}

// Page sizes of List.
const (
	DefaultMembersPage = 50
	MaxMembersPage     = 100
)

// pageSize returns limit within 1..MaxMembersPage; 0 or less means DefaultMembersPage.
func pageSize(limit int) int {
	switch {
	case limit <= 0:
		return DefaultMembersPage
	case limit > MaxMembersPage:
		return MaxMembersPage
	}
	return limit
}

// MemberPage is a page of members; Next is the cursor of the next page, "" on the last.
type MemberPage struct {
	Members []Member
	Next    string
}

// List returns a page of the tenant's members (members:read). cursor is the Next of
// the previous page, "" for the first; limit 0 means DefaultMembersPage.
func (m *Members) List(ctx context.Context, cursor string, limit int) (MemberPage, error) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return MemberPage{}, domain.ErrTenantForbidden()
	}
	if err := m.authz.Require(ctx, authz.PermMembersRead); err != nil {
		return MemberPage{}, fmt.Errorf("list members: %w", err)
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return MemberPage{}, err
	}
	limit = pageSize(limit)
	var page MemberPage
	err = m.store.InTenant(ctx, p, func(ctx context.Context, r TenantRepo) error {
		// One more than the page tells whether another page follows.
		members, err := r.ListMembers(ctx, p.TenantID(), after, limit+1)
		if err != nil {
			return err
		}
		if len(members) > limit {
			members = members[:limit]
			page.Next = encodeCursor(members[limit-1].MembershipID)
		}
		page.Members = members
		return nil
	})
	if err != nil {
		return MemberPage{}, fmt.Errorf("list members: %w", err)
	}
	return page, nil
}

// ChangeRole gives the member userID the role (members:manage). The hierarchy is
// judged on the roles the request started with; the database function locks the tenant,
// reads both roles again and refuses if either changed meanwhile (membership.changed),
// so a decision on stale roles is never applied. It never leaves the tenant without an
// owner (membership.last_owner).
func (m *Members) ChangeRole(ctx context.Context, userID, role string) (Member, error) {
	var out Member
	err := m.manage(ctx, userID, func(ctx context.Context, r TenantRepo, actor, target Member) error {
		if err := domain.CheckRoleChange(m.member(actor), m.member(target), m.role(role)); err != nil {
			return err
		}
		// The member keeps the pending invitations their new role could still issue; the
		// database revokes the rest in the same transaction (design, 5.6.4).
		if err := r.ChangeMemberRole(ctx, target.UserID, role, actor.Role, target.Role, m.offerable(role), ipPrefix(ctx)); err != nil {
			return err
		}
		out = target
		out.Role = role
		return nil
	})
	if err != nil {
		return Member{}, fmt.Errorf("change member role: %w", err)
	}
	return out, nil
}

// Remove removes the member userID from the tenant (members:manage), with the same
// hierarchy and guarantees as ChangeRole.
func (m *Members) Remove(ctx context.Context, userID string) error {
	err := m.manage(ctx, userID, func(ctx context.Context, r TenantRepo, actor, target Member) error {
		if err := domain.CheckRemoval(m.member(actor), m.member(target)); err != nil {
			return err
		}
		return r.RemoveMember(ctx, target.UserID, actor.Role, target.Role, ipPrefix(ctx))
	})
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

// Leave removes the principal from its tenant. It needs no permission: any member may
// leave, except the last owner (membership.last_owner).
func (m *Members) Leave(ctx context.Context) error {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return domain.ErrTenantForbidden()
	}
	err := m.store.InTenant(ctx, p, func(ctx context.Context, r TenantRepo) error {
		return r.LeaveTenant(ctx, ipPrefix(ctx))
	})
	if err != nil {
		return fmt.Errorf("leave tenant: %w", err)
	}
	return nil
}

// manage runs fn in the tenant's transaction with the actor and the member userID,
// once the actor holds members:manage.
func (m *Members) manage(ctx context.Context, userID string, fn func(context.Context, TenantRepo, Member, Member) error) error {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return domain.ErrTenantForbidden()
	}
	if err := m.authz.Require(ctx, authz.PermMembersManage); err != nil {
		return err
	}
	targetID, err := authctx.ParseID(userID)
	if err != nil {
		return domain.ErrMemberNotFound()
	}
	return m.store.InTenant(ctx, p, func(ctx context.Context, r TenantRepo) error {
		target, found, err := r.MemberByUser(ctx, p.TenantID(), targetID)
		if err != nil {
			return err
		}
		if !found {
			return domain.ErrMemberNotFound()
		}
		return fn(ctx, r, Member{UserID: p.UserID(), Role: p.Role()}, target)
	})
}

// RoleView is a role of the catalog as the principal sees it: whether it requires MFA,
// and whether the principal could give it to a member (CanAssign) or offer it in an
// invitation (CanOffer).
type RoleView struct {
	Name        string
	MFARequired bool
	CanAssign   bool
	CanOffer    bool
}

// Roles returns every role of the catalog (members:read), the roles with the most
// permissions first, judged for the principal with the same rules as ChangeRole and
// inviting, so the front end only offers what the API would accept.
func (m *Members) Roles(ctx context.Context) ([]RoleView, error) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return nil, domain.ErrTenantForbidden()
	}
	if err := m.authz.Require(ctx, authz.PermMembersRead); err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	actor := m.role(p.Role())
	names := m.catalog.Names()
	slices.SortStableFunc(names, func(a, b string) int {
		return len(m.catalog.Permissions(b)) - len(m.catalog.Permissions(a))
	})
	out := make([]RoleView, 0, len(names))
	for _, name := range names {
		r, _ := m.catalog.Role(name)
		role := m.role(name)
		out = append(out, RoleView{
			Name: name, MFARequired: r.RequiresMFA,
			CanAssign: domain.CanAssign(actor, role), CanOffer: domain.CanOffer(actor, role),
		})
	}
	return out, nil
}

// offerable returns the roles of the catalog that a member with role may offer.
func (m *Members) offerable(role string) []string {
	r := m.role(role)
	out := []string{}
	for _, name := range m.catalog.Names() {
		if domain.CanOffer(r, m.role(name)) {
			out = append(out, name)
		}
	}
	return out
}

func (m *Members) member(mem Member) domain.Member {
	return domain.Member{UserID: mem.UserID, Role: m.role(mem.Role)}
}

func (m *Members) role(name string) domain.Role {
	_, known := m.catalog.Role(name)
	perms := m.catalog.Permissions(name)
	out := domain.Role{
		Name: name, Known: known, Owner: known && name == authz.RoleOwner,
		CanInvite: slices.Contains(perms, authz.PermMembersInvite), CanManage: slices.Contains(perms, authz.PermMembersManage),
		Permissions: make([]string, len(perms)),
	}
	for i, p := range perms {
		out.Permissions[i] = string(p)
	}
	return out
}

// A cursor is the last membership id of a page, opaque to the client. It needs no
// signature: whatever id it names, RLS keeps the page in the principal's tenant.
func encodeCursor(membershipID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(membershipID))
}

func decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", domain.ErrCursorInvalid()
	}
	id, err := authctx.ParseID(string(raw))
	if err != nil {
		return "", domain.ErrCursorInvalid()
	}
	return id, nil
}

func ipPrefix(ctx context.Context) netip.Prefix {
	p, _ := clientip.PrefixFrom(ctx)
	return p
}
