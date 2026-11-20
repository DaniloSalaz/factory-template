package app

import (
	"context"
	"fmt"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
)

// Accounts answers what a user may know about their own account (design, section 10).
type Accounts struct {
	store Store
}

// NewAccounts builds Accounts.
func NewAccounts(store Store) *Accounts {
	return &Accounts{store: store}
}

// Me is the current user with every tenant they belong to.
type Me struct {
	User        Profile
	Memberships []TenantMembership
}

// Me returns userID's profile and memberships, read in one transaction as that user:
// auth_memberships_for_user only ever returns the acting user's own.
func (a *Accounts) Me(ctx context.Context, userID string) (Me, error) {
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
		me = Me{User: p, Memberships: ms}
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
