package usecases

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/authz"
	"chaskicode.dev/saas/platform/kit/pii"
	"chaskicode.dev/saas/platform/kit/ratelimit"
	"chaskicode.dev/saas/platform/kit/telemetry"
)

// Invitations creates, lists, resends, revokes and accepts invitations (design, 5.6).
type Invitations struct {
	store     TenantStore
	authz     Authorizer
	members   *Members // its catalog view: role and offerable
	sender    InvitationSender
	invite    ratelimit.Limiter // creations and resends, per user
	accept    ratelimit.Limiter // accepts, per user
	metrics   *telemetry.Metrics
	frontBase string
	logger    *slog.Logger
	now       func() time.Time
}

// InvitationsConfig is what Invitations needs besides its ports.
type InvitationsConfig struct {
	// FrontBaseURL is where the front end lives; links are FrontBaseURL/invite#token=….
	FrontBaseURL  string
	InviteLimiter ratelimit.Limiter
	AcceptLimiter ratelimit.Limiter
	// Metrics counts invitation emails sent and failed (invitation.email); nil records
	// nothing.
	Metrics *telemetry.Metrics
}

// NewInvitations builds Invitations. now is the clock; nil means time.Now.
func NewInvitations(store TenantStore, authorizer Authorizer, members *Members, sender InvitationSender,
	cfg InvitationsConfig, logger *slog.Logger, now func() time.Time,
) *Invitations {
	if now == nil {
		now = time.Now
	}
	return &Invitations{
		store: store, authz: authorizer, members: members, sender: sender,
		invite: cfg.InviteLimiter, accept: cfg.AcceptLimiter, metrics: cfg.Metrics, frontBase: cfg.FrontBaseURL, logger: logger, now: now,
	}
}

// Revocation reasons recorded in invitation.revoked by Go; the member functions record
// inviter_role_changed, inviter_removed and inviter_left.
const (
	revokedByMember       = "revoked"
	revokedExpiredReplace = "expired_replaced"
)

// Create invites email to the principal's tenant with role (members:invite). The role
// must exist and be one the actor may offer (domain.CanOffer). An email of a member is
// refused, a pending unexpired invitation too (resend it); an expired one is revoked and
// replaced. The link is sent once the transaction commits.
func (in *Invitations) Create(ctx context.Context, rawEmail, role string) (Invitation, error) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return Invitation{}, domain.ErrTenantForbidden()
	}
	if err := in.authz.Require(ctx, authz.PermMembersInvite); err != nil {
		return Invitation{}, fmt.Errorf("create invitation: %w", err)
	}
	if err := allow(ctx, in.invite, "invite:"+p.UserID()); err != nil {
		return Invitation{}, err
	}
	email, err := domain.NormalizeInviteEmail(rawEmail)
	if err != nil {
		return Invitation{}, err
	}
	if err := in.canOffer(p, role); err != nil {
		return Invitation{}, err
	}
	token, err := domain.NewRandom()
	if err != nil {
		return Invitation{}, fmt.Errorf("invitation token: %w", err)
	}

	var created Invitation
	var tenant Tenant
	var actor Member
	err = in.store.InTenant(ctx, p, func(ctx context.Context, r TenantRepo) error {
		var err error
		if tenant, actor, err = in.lockAsActor(ctx, r, p); err != nil {
			return err
		}
		member, err := r.EmailIsMember(ctx, p.TenantID(), pii.Email(email))
		if err != nil {
			return err
		}
		if member {
			return domain.ErrInvitationAlreadyMember()
		}
		pending, found, err := r.PendingInvitationByEmail(ctx, p.TenantID(), pii.Email(email))
		if err != nil {
			return err
		}
		if found {
			if pending.ExpiresAt.After(in.now()) {
				return domain.ErrInvitationPending()
			}
			if err := in.revoke(ctx, r, p, pending, revokedExpiredReplace); err != nil {
				return err
			}
		}
		created, err = r.CreateInvitation(ctx, NewInvitation{
			TenantID: p.TenantID(), Email: pii.Email(email), Role: role, TokenHash: domain.HashToken(token),
			InvitedBy: p.UserID(), ExpiresAt: in.now().Add(domain.InvitationTTL),
		})
		if err != nil {
			return err
		}
		return in.audit(ctx, r, p, AuditInvitationCreated, created, nil)
	})
	if err != nil {
		return Invitation{}, fmt.Errorf("create invitation: %w", err)
	}
	in.send(ctx, created, token, tenant.Name, actor)
	return created, nil
}

// Resend gives a pending invitation a new token and a new expiry, and sends the new
// link (members:invite and the same rule as Create on its role). The old link stops
// working. The inviter stays the one who created it.
func (in *Invitations) Resend(ctx context.Context, id string) (Invitation, error) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return Invitation{}, domain.ErrTenantForbidden()
	}
	if err := in.authz.Require(ctx, authz.PermMembersInvite); err != nil {
		return Invitation{}, fmt.Errorf("resend invitation: %w", err)
	}
	if err := allow(ctx, in.invite, "invite:"+p.UserID()); err != nil {
		return Invitation{}, err
	}
	token, err := domain.NewRandom()
	if err != nil {
		return Invitation{}, fmt.Errorf("invitation token: %w", err)
	}
	var inv Invitation
	var tenant Tenant
	var actor Member
	err = in.pending(ctx, p, id, func(ctx context.Context, r TenantRepo, t Tenant, a Member, found Invitation) error {
		inv, tenant, actor = found, t, a
		inv.ExpiresAt = in.now().Add(domain.InvitationTTL)
		if err := r.RotateInvitation(ctx, inv.ID, domain.HashToken(token), inv.ExpiresAt); err != nil {
			return err
		}
		return in.audit(ctx, r, p, AuditInvitationResent, inv, nil)
	})
	if err != nil {
		return Invitation{}, fmt.Errorf("resend invitation: %w", err)
	}
	in.send(ctx, inv, token, tenant.Name, actor)
	return inv, nil
}

// Revoke revokes a pending invitation (members:invite and the same rule as Create on
// its role).
func (in *Invitations) Revoke(ctx context.Context, id string) error {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return domain.ErrTenantForbidden()
	}
	if err := in.authz.Require(ctx, authz.PermMembersInvite); err != nil {
		return fmt.Errorf("revoke invitation: %w", err)
	}
	err := in.pending(ctx, p, id, func(ctx context.Context, r TenantRepo, _ Tenant, _ Member, inv Invitation) error {
		return in.revoke(ctx, r, p, inv, revokedByMember)
	})
	if err != nil {
		return fmt.Errorf("revoke invitation: %w", err)
	}
	return nil
}

// InvitationPage is a page of pending invitations; Next is "" on the last.
type InvitationPage struct {
	Invitations []Invitation
	Next        string
}

// List returns a page of the tenant's pending invitations, expired ones included
// (members:invite), oldest first. cursor and limit work as in Members.List.
func (in *Invitations) List(ctx context.Context, cursor string, limit int) (InvitationPage, error) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return InvitationPage{}, domain.ErrTenantForbidden()
	}
	if err := in.authz.Require(ctx, authz.PermMembersInvite); err != nil {
		return InvitationPage{}, fmt.Errorf("list invitations: %w", err)
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return InvitationPage{}, err
	}
	limit = pageSize(limit)
	var page InvitationPage
	err = in.store.InTenant(ctx, p, func(ctx context.Context, r TenantRepo) error {
		invs, err := r.ListInvitations(ctx, p.TenantID(), after, limit+1)
		if err != nil {
			return err
		}
		if len(invs) > limit {
			invs = invs[:limit]
			page.Next = encodeCursor(invs[limit-1].ID)
		}
		page.Invitations = invs
		return nil
	})
	if err != nil {
		return InvitationPage{}, fmt.Errorf("list invitations: %w", err)
	}
	return page, nil
}

// Expired reports whether inv can no longer be accepted, at now.
func (inv Invitation) Expired(now time.Time) bool { return !inv.ExpiresAt.After(now) }

// Now is the clock Invitations judges expiry with.
func (in *Invitations) Now() time.Time { return in.now() }

// Accept accepts, as user, the invitation of token. Everything is revalidated in the
// database: the email must be the user's and verified, the invitation pending and not
// expired, the tenant active and the user not a member yet.
func (in *Invitations) Accept(ctx context.Context, user authctx.User, token string) (AcceptedInvitation, error) {
	if err := allow(ctx, in.accept, "accept:"+user.ID()); err != nil {
		return AcceptedInvitation{}, err
	}
	if !domain.WellFormed(token) {
		return AcceptedInvitation{}, domain.ErrInvitationNotFound()
	}
	var accepted AcceptedInvitation
	err := in.store.AsUser(ctx, user.ID(), func(ctx context.Context, r TenantRepo) error {
		var err error
		accepted, err = r.AcceptInvitation(ctx, domain.HashToken(token), ipPrefix(ctx))
		return err
	})
	if err != nil {
		return AcceptedInvitation{}, fmt.Errorf("accept invitation: %w", err)
	}
	return accepted, nil
}

// pending runs fn on the tenant's pending invitation id, with the tenant locked and the
// actor checked as in Create.
func (in *Invitations) pending(ctx context.Context, p authctx.Principal, id string,
	fn func(context.Context, TenantRepo, Tenant, Member, Invitation) error,
) error {
	invID, err := authctx.ParseID(id)
	if err != nil {
		return domain.ErrInvitationNotFound()
	}
	return in.store.InTenant(ctx, p, func(ctx context.Context, r TenantRepo) error {
		tenant, actor, err := in.lockAsActor(ctx, r, p)
		if err != nil {
			return err
		}
		inv, found, err := r.InvitationByID(ctx, p.TenantID(), invID)
		switch {
		case err != nil:
			return err
		case !found:
			return domain.ErrInvitationNotFound()
		case !inv.AcceptedAt.IsZero():
			return domain.ErrInvitationAccepted()
		case !inv.RevokedAt.IsZero():
			return domain.ErrInvitationRevoked()
		}
		if err := in.canOffer(p, inv.Role); err != nil {
			return err
		}
		return fn(ctx, r, tenant, actor, inv)
	})
}

// lockAsActor share-locks the tenant and checks that the actor's role is still the one
// the request started with: a demotion in progress either finishes first (and this
// request is refused) or waits for this transaction (and revokes what it writes). It
// returns the tenant and the actor's membership.
func (in *Invitations) lockAsActor(ctx context.Context, r TenantRepo, p authctx.Principal) (Tenant, Member, error) {
	if err := r.ShareLockTenant(ctx, p.TenantID()); err != nil {
		return Tenant{}, Member{}, err
	}
	actor, found, err := r.MemberByUser(ctx, p.TenantID(), p.UserID())
	if err != nil {
		return Tenant{}, Member{}, err
	}
	if !found || actor.Role != p.Role() {
		return Tenant{}, Member{}, domain.ErrMembershipChanged()
	}
	t, found, err := r.TenantByID(ctx, p.TenantID())
	if err != nil {
		return Tenant{}, Member{}, err
	}
	if !found {
		return Tenant{}, Member{}, domain.ErrTenantForbidden()
	}
	return t, actor, nil
}

func (in *Invitations) canOffer(p authctx.Principal, role string) error {
	offered := in.members.role(role)
	if !offered.Known {
		return domain.ErrInvitationUnknownRole()
	}
	if !domain.CanOffer(in.members.role(p.Role()), offered) {
		return domain.ErrInvitationRoleNotOfferable()
	}
	return nil
}

func (in *Invitations) revoke(ctx context.Context, r TenantRepo, p authctx.Principal, inv Invitation, reason string) error {
	if err := r.RevokeInvitation(ctx, inv.ID); err != nil {
		return err
	}
	return in.audit(ctx, r, p, AuditInvitationRevoked, inv, map[string]string{"reason": reason})
}

// audit records an invitation event. The invitee's email never goes in the metadata
// (design, section 9).
func (in *Invitations) audit(ctx context.Context, r TenantRepo, p authctx.Principal, action string, inv Invitation, extra map[string]string) error {
	meta := map[string]string{"role": inv.Role, "invited_by": inv.InvitedBy}
	for k, v := range extra {
		meta[k] = v
	}
	return r.RecordTenantAudit(ctx, p.TenantID(), AuditEvent{
		Action: action, TargetType: "invitation", TargetID: inv.ID, Metadata: meta, IPPrefix: ipPrefix(ctx),
	})
}

// send delivers the link after the commit. A failure only logs: the invitation exists
// and resending it is the remedy; there is no queue to retry from yet.
func (in *Invitations) send(ctx context.Context, inv Invitation, token, tenantName string, inviter Member) {
	name := inviter.DisplayName
	if name == "" {
		// The invitee should know who invites them; an email is all some users have.
		name = inviter.Email.Reveal()
	}
	msg := InvitationMessage{
		To: inv.Email, Link: pii.Secret(domain.InvitationLink(in.frontBase, token)),
		TenantName: tenantName, Role: inv.Role, InviterName: name,
	}
	if err := in.sender.Send(ctx, msg); err != nil {
		in.metrics.InvitationEmail(ctx, telemetry.ResultFailure)
		in.logger.ErrorContext(ctx, "send invitation: resend it to retry",
			slog.String("invitation_id", inv.ID), slog.Any("error", err))
		return
	}
	in.metrics.InvitationEmail(ctx, telemetry.ResultSuccess)
}

// allow counts one request of key against limiter.
func allow(ctx context.Context, limiter ratelimit.Limiter, key string) error {
	d, err := limiter.Allow(ctx, key)
	if err != nil {
		return apperr.Wrap(err, apperr.KindUnavailable, "rate_limit.unavailable", "The service is temporarily unavailable.")
	}
	if !d.Allowed {
		return apperr.RateLimited("rate_limit.exceeded", "Too many requests; try again later.", d.RetryAfter)
	}
	return nil
}
