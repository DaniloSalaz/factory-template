package http

import (
	"context"

	identityapi "chaskicode.dev/saas/gen/openapi/identity"
	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/identity/usecases"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
)

// Tenants and members (auth-y-tenancy §5.5 and §5.7). Permissions are checked in the use cases,
// never here.

func errBodyRequired() error {
	return apperr.BadRequest("invalid_request", "The request body is required.")
}

// CreateTenant creates a tenant with the caller as its owner.
func (h *Handler) CreateTenant(ctx context.Context, req identityapi.CreateTenantRequestObject) (identityapi.CreateTenantResponseObject, error) {
	user, ok := authctx.UserFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	if req.Body == nil {
		return nil, errBodyRequired()
	}
	created, err := h.tenants.Create(ctx, user, req.Body.Name, req.Body.Slug)
	if err != nil {
		return nil, err
	}
	id, err := uuid(created.Tenant.ID)
	if err != nil {
		return nil, err
	}
	return identityapi.CreateTenant201JSONResponse{
		Id: id, Slug: created.Tenant.Slug, Name: created.Tenant.Name, Role: created.Role,
	}, nil
}

// UpdateCurrentTenant renames the tenant of X-Tenant-ID.
func (h *Handler) UpdateCurrentTenant(ctx context.Context, req identityapi.UpdateCurrentTenantRequestObject) (identityapi.UpdateCurrentTenantResponseObject, error) {
	if req.Body == nil {
		return nil, errBodyRequired()
	}
	t, err := h.tenants.Rename(ctx, req.Body.Name)
	if err != nil {
		return nil, err
	}
	id, err := uuid(t.ID)
	if err != nil {
		return nil, err
	}
	return identityapi.UpdateCurrentTenant200JSONResponse{Id: id, Slug: t.Slug, Name: t.Name}, nil
}

// ListMembers returns a page of the tenant's members.
func (h *Handler) ListMembers(ctx context.Context, req identityapi.ListMembersRequestObject) (identityapi.ListMembersResponseObject, error) {
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	page, err := h.members.List(ctx, deref(req.Params.Cursor), limit)
	if err != nil {
		return nil, err
	}
	out := identityapi.ListMembers200JSONResponse{Items: make([]identityapi.Member, 0, len(page.Members))}
	for _, m := range page.Members {
		item, err := memberView(m)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	if page.Next != "" {
		next := page.Next
		out.NextCursor = &next
	}
	return out, nil
}

// ListTenantRoles returns the catalog's roles, judged for the caller.
func (h *Handler) ListTenantRoles(ctx context.Context, _ identityapi.ListTenantRolesRequestObject) (identityapi.ListTenantRolesResponseObject, error) {
	roles, err := h.members.Roles(ctx)
	if err != nil {
		return nil, err
	}
	out := identityapi.ListTenantRoles200JSONResponse{Items: make([]identityapi.TenantRole, 0, len(roles))}
	for _, r := range roles {
		out.Items = append(out.Items, identityapi.TenantRole{
			Name: r.Name, MfaRequired: r.MFARequired, CanAssign: r.CanAssign, CanOffer: r.CanOffer,
		})
	}
	return out, nil
}

// LeaveTenant removes the caller from the tenant of X-Tenant-ID.
func (h *Handler) LeaveTenant(ctx context.Context, _ identityapi.LeaveTenantRequestObject) (identityapi.LeaveTenantResponseObject, error) {
	if err := h.members.Leave(ctx); err != nil {
		return nil, err
	}
	return identityapi.LeaveTenant204Response{}, nil
}

// ChangeMemberRole changes a member's role.
func (h *Handler) ChangeMemberRole(ctx context.Context, req identityapi.ChangeMemberRoleRequestObject) (identityapi.ChangeMemberRoleResponseObject, error) {
	if req.Body == nil {
		return nil, errBodyRequired()
	}
	m, err := h.members.ChangeRole(ctx, req.UserId.String(), req.Body.Role)
	if err != nil {
		return nil, err
	}
	view, err := memberView(m)
	if err != nil {
		return nil, err
	}
	return identityapi.ChangeMemberRole200JSONResponse(view), nil
}

// RemoveMember removes a member from the tenant.
func (h *Handler) RemoveMember(ctx context.Context, req identityapi.RemoveMemberRequestObject) (identityapi.RemoveMemberResponseObject, error) {
	if err := h.members.Remove(ctx, req.UserId.String()); err != nil {
		return nil, err
	}
	return identityapi.RemoveMember204Response{}, nil
}

func memberView(m usecases.Member) (identityapi.Member, error) {
	id, err := uuid(m.UserID)
	if err != nil {
		return identityapi.Member{}, err
	}
	// Other members' emails, to a member of the same tenant: the API boundary is where
	// pii is revealed.
	return identityapi.Member{
		UserId: id, Email: m.Email.Reveal(), DisplayName: m.DisplayName, Role: m.Role, JoinedAt: m.JoinedAt,
	}, nil
}
