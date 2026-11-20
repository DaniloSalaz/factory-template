package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authz"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/clientip"
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
}

// NewTenants builds Tenants. maxPerUser is how many tenants a user may create.
func NewTenants(store TenantStore, authorizer Authorizer, maxPerUser int) *Tenants {
	return &Tenants{store: store, authz: authorizer, maxPerUser: maxPerUser}
}

// CreatedTenant is a tenant just created, with its creator as owner.
type CreatedTenant struct {
	Tenant Tenant
	Role   string
}

// Create creates a tenant with user as its owner. It needs no permission and no MFA:
// if the owner role requires MFA, acting in the tenant does, from the next request.
func (t *Tenants) Create(ctx context.Context, user authctx.User, name, slug string) (CreatedTenant, error) {
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
	switch {
	case limit <= 0:
		limit = DefaultMembersPage
	case limit > MaxMembersPage:
		limit = MaxMembersPage
	}
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

// ChangeRole gives the member userID the role (members:manage). The actor's role is
// read again inside the transaction, so a role lost since the request started counts;
// the database refuses if a role changes between that read and the write
// (membership.changed), and never leaves the tenant without an owner
// (membership.last_owner).
func (m *Members) ChangeRole(ctx context.Context, userID, role string) (Member, error) {
	var out Member
	err := m.manage(ctx, userID, func(ctx context.Context, r TenantRepo, actor, target Member) error {
		if err := domain.CheckRoleChange(m.member(actor), m.member(target), m.role(role)); err != nil {
			return err
		}
		if err := r.ChangeMemberRole(ctx, target.UserID, role, actor.Role, target.Role, ipPrefix(ctx)); err != nil {
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

// manage runs fn in the tenant's transaction with the actor and the member userID as
// they are now, once the actor is known to still hold members:manage.
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
		actor, found, err := r.MemberByUser(ctx, p.TenantID(), p.UserID())
		if err != nil {
			return err
		}
		if !found {
			return authz.ErrDenied()
		}
		if err := m.requireCurrent(ctx, p, actor.Role); err != nil {
			return err
		}
		target, found, err := r.MemberByUser(ctx, p.TenantID(), targetID)
		if err != nil {
			return err
		}
		if !found {
			return domain.ErrMemberNotFound()
		}
		return fn(ctx, r, actor, target)
	})
}

// requireCurrent checks members:manage again with the actor's role as stored now, when
// it differs from the one the request started with. A denial is audited like any other.
func (m *Members) requireCurrent(ctx context.Context, p authctx.Principal, role string) error {
	if role == p.Role() {
		return nil
	}
	current, err := authctx.NewPrincipal(p.UserID(), p.TenantID(), role, m.catalog.Permissions(role)...)
	if err != nil {
		return fmt.Errorf("current principal: %w", err)
	}
	return m.authz.Require(authctx.WithPrincipal(ctx, current), authz.PermMembersManage)
}

func (m *Members) member(mem Member) domain.Member {
	return domain.Member{UserID: mem.UserID, Role: m.role(mem.Role)}
}

func (m *Members) role(name string) domain.Role {
	_, known := m.catalog.Role(name)
	perms := m.catalog.Permissions(name)
	out := domain.Role{Name: name, Known: known, Owner: known && name == authz.RoleOwner, Permissions: make([]string, len(perms))}
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
