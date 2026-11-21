package usecases

import (
	"context"
	"fmt"
	"time"

	"chaskicode.dev/saas/modules/identity/domain"
	"chaskicode.dev/saas/platform/authctx"
	"chaskicode.dev/saas/platform/authz"
)

// Accounts answers what a user may know about their own account (design, section 10).
type Accounts struct {
	store   Store
	catalog *authz.Catalog
}

// NewAccounts builds Accounts.
func NewAccounts(store Store, catalog *authz.Catalog) *Accounts {
	return &Accounts{store: store, catalog: catalog}
}

// Me is the current user with every tenant they belong to and their MFA factors.
type Me struct {
	User        Profile
	Memberships []TenantMembership
	Factors     []MFAFactor
	// SessionMFAAt is when this session last verified a factor; zero: never.
	SessionMFAAt time.Time
}

// MFAEnrolled reports whether the user has a verified factor.
func (m Me) MFAEnrolled() bool { return hasVerified(m.Factors) }

// Me returns the session's user, their memberships (each with whether its role
// requires MFA) and factors, read in one transaction as that user: the functions behind
// them only ever return the acting user's own.
func (a *Accounts) Me(ctx context.Context, cur Current) (Me, error) {
	userID := cur.UserID
	var me Me
	err := a.store.AsUser(ctx, userID, func(ctx context.Context, r Repo) error {
		p, found, err := r.UserByID(ctx, userID)
		if err != nil {
			return fmt.Errorf("user: %w", err)
		}
		if !found {
			return domain.ErrSessionInvalid()
		}
		ms, err := r.MembershipsForUser(ctx)
		if err != nil {
			return fmt.Errorf("memberships: %w", err)
		}
		for i := range ms {
			role, ok := a.catalog.Role(ms[i].Role)
			ms[i].MFARequired = ok && role.RequiresMFA
		}
		factors, err := r.MFAFactors(ctx)
		if err != nil {
			return fmt.Errorf("mfa factors: %w", err)
		}
		me = Me{User: p, Memberships: ms, Factors: factors, SessionMFAAt: cur.MFAAuthTime}
		return nil
	})
	if err != nil {
		return Me{}, fmt.Errorf("me: %w", err)
	}
	return me, nil
}

// CurrentTenant is the tenant a principal acts in, with its role and permissions.
type CurrentTenant struct {
	Tenant    Tenant
	Principal authctx.Principal
}

// CurrentTenant returns the context's principal and its tenant, read under RLS in the
// tenant's own transaction. It needs no permission: every member may know their role.
func (a *Accounts) CurrentTenant(ctx context.Context) (CurrentTenant, error) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return CurrentTenant{}, domain.ErrTenantForbidden()
	}
	var t Tenant
	err := a.store.InTenant(ctx, p, func(ctx context.Context, r Repo) error {
		var found bool
		var err error
		t, found, err = r.TenantByID(ctx, p.TenantID())
		if err != nil {
			return fmt.Errorf("tenant: %w", err)
		}
		if !found {
			return domain.ErrTenantForbidden()
		}
		return nil
	})
	if err != nil {
		return CurrentTenant{}, fmt.Errorf("current tenant: %w", err)
	}
	return CurrentTenant{Tenant: t, Principal: p}, nil
}
