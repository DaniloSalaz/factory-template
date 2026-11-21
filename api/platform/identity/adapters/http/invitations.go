package http

import (
	"context"
	"time"

	identityapi "chaskicode.dev/saas/gen/openapi/identity"
	"chaskicode.dev/saas/modules/identity/app"
	"chaskicode.dev/saas/modules/identity/domain"
	"chaskicode.dev/saas/platform/authctx"
)

// Invitations (design, 5.6). Permissions are checked in the use case. No response
// carries a token or its hash.

// ListInvitations returns a page of the tenant's pending invitations.
func (h *Handler) ListInvitations(ctx context.Context, req identityapi.ListInvitationsRequestObject) (identityapi.ListInvitationsResponseObject, error) {
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	page, err := h.invitations.List(ctx, deref(req.Params.Cursor), limit)
	if err != nil {
		return nil, err
	}
	now := h.invitations.Now()
	out := identityapi.ListInvitations200JSONResponse{Items: make([]identityapi.Invitation, 0, len(page.Invitations))}
	for _, inv := range page.Invitations {
		item, err := invitationView(inv, now)
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

// CreateInvitation invites an email to the tenant.
func (h *Handler) CreateInvitation(ctx context.Context, req identityapi.CreateInvitationRequestObject) (identityapi.CreateInvitationResponseObject, error) {
	if req.Body == nil {
		return nil, errBodyRequired()
	}
	inv, err := h.invitations.Create(ctx, req.Body.Email, req.Body.Role)
	if err != nil {
		return nil, err
	}
	view, err := invitationView(inv, h.invitations.Now())
	if err != nil {
		return nil, err
	}
	return identityapi.CreateInvitation201JSONResponse(view), nil
}

// ResendInvitation rotates an invitation's token and sends it again.
func (h *Handler) ResendInvitation(ctx context.Context, req identityapi.ResendInvitationRequestObject) (identityapi.ResendInvitationResponseObject, error) {
	inv, err := h.invitations.Resend(ctx, req.Id.String())
	if err != nil {
		return nil, err
	}
	view, err := invitationView(inv, h.invitations.Now())
	if err != nil {
		return nil, err
	}
	return identityapi.ResendInvitation200JSONResponse(view), nil
}

// RevokeInvitation revokes a pending invitation.
func (h *Handler) RevokeInvitation(ctx context.Context, req identityapi.RevokeInvitationRequestObject) (identityapi.RevokeInvitationResponseObject, error) {
	if err := h.invitations.Revoke(ctx, req.Id.String()); err != nil {
		return nil, err
	}
	return identityapi.RevokeInvitation204Response{}, nil
}

// AcceptInvitation accepts an invitation as the session's user.
func (h *Handler) AcceptInvitation(ctx context.Context, req identityapi.AcceptInvitationRequestObject) (identityapi.AcceptInvitationResponseObject, error) {
	user, ok := authctx.UserFrom(ctx)
	if !ok {
		return nil, domain.ErrSessionInvalid()
	}
	if req.Body == nil {
		return nil, errBodyRequired()
	}
	accepted, err := h.invitations.Accept(ctx, user, req.Body.Token)
	if err != nil {
		return nil, err
	}
	tenantID, err := uuid(accepted.TenantID)
	if err != nil {
		return nil, err
	}
	return identityapi.AcceptInvitation200JSONResponse{TenantId: tenantID, Role: accepted.Role}, nil
}

func invitationView(inv usecases.Invitation, now time.Time) (identityapi.Invitation, error) {
	id, err := uuid(inv.ID)
	if err != nil {
		return identityapi.Invitation{}, err
	}
	out := identityapi.Invitation{
		// The invitee's email, to members who may invite: the API boundary reveals it.
		Id: id, Email: inv.Email.Reveal(), Role: inv.Role, ExpiresAt: inv.ExpiresAt,
		Expired: inv.Expired(now), CreatedAt: inv.CreatedAt,
	}
	if inv.InvitedBy != "" {
		by, err := uuid(inv.InvitedBy)
		if err != nil {
			return identityapi.Invitation{}, err
		}
		out.InvitedBy = &by
	}
	return out, nil
}
