package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/authz"
)

// Tenancy turns an authenticated user and the tenant a request names into the request's
// Principal (design, 5.4.2 to 5.4.4).
type Tenancy struct {
	store   Store
	catalog *authz.Catalog
	now     func() time.Time
}

// NewTenancy builds Tenancy. now is the clock; nil means time.Now.
func NewTenancy(store Store, catalog *authz.Catalog, now func() time.Time) *Tenancy {
	if now == nil {
		now = time.Now
	}
	return &Tenancy{store: store, catalog: catalog, now: now}
}

// Entered is a user acting in a tenant. UnknownRole is true when the membership's role
// is not in the catalog: the principal then has no permissions, and the caller logs it.
type Entered struct {
	Principal   authctx.Principal
	UnknownRole bool
}

// Enter returns the principal of user in the tenant rawTenantID names. Every reason to
// refuse it (a malformed id, no such tenant, no membership, a suspended tenant, a
// disabled user) is the same domain.ErrTenantForbidden, with the reason as its cause
// for the logs. A role whose MFA is missing or too old answers
// domain.ErrMFAVerificationRequired, or domain.ErrMFAEnrollmentRequired if the user has
// no verified factor yet.
func (t *Tenancy) Enter(ctx context.Context, user authctx.User, rawTenantID string) (Entered, error) {
	tenantID, err := authctx.ParseID(rawTenantID)
	if err != nil {
		return Entered{}, forbidden(errors.New("malformed tenant id"))
	}
	var m Membership
	var found bool
	err = t.store.AsUser(ctx, user.ID(), func(ctx context.Context, r Repo) error {
		var err error
		m, found, err = r.Membership(ctx, tenantID)
		return err
	})
	if err != nil {
		return Entered{}, fmt.Errorf("membership: %w", err)
	}
	switch {
	case !found:
		return Entered{}, forbidden(errors.New("no membership in the tenant, or no such tenant"))
	case !m.TenantActive:
		return Entered{}, forbidden(errors.New("the tenant is not active"))
	case m.UserDisabled:
		return Entered{}, forbidden(errors.New("the user is disabled"))
	}

	role, known := t.catalog.Role(m.Role)
	if known {
		mfaAt, _ := user.MFAAuthTime()
		if !role.MFASatisfied(mfaAt, t.now()) {
			return Entered{}, t.mfaRequired(ctx, user)
		}
	}
	// An unknown role has no permissions in the catalog, and so the own scope.
	p, err := authctx.NewPrincipal(user.ID(), tenantID, m.Role, t.catalog.Permissions(m.Role)...)
	if err != nil {
		return Entered{}, forbidden(fmt.Errorf("principal: %w", err))
	}
	return Entered{Principal: p, UnknownRole: !known}, nil
}

// mfaRequired tells the front end what the user must do: verify a factor they have, or
// enroll one first.
func (t *Tenancy) mfaRequired(ctx context.Context, user authctx.User) error {
	var verified bool
	err := t.store.AsUser(ctx, user.ID(), func(ctx context.Context, r Repo) error {
		factors, err := r.MFAFactors(ctx)
		verified = hasVerified(factors)
		return err
	})
	if err != nil {
		return fmt.Errorf("mfa factors: %w", err)
	}
	if verified {
		return domain.ErrMFAVerificationRequired()
	}
	return domain.ErrMFAEnrollmentRequired()
}

func forbidden(cause error) error {
	e, _ := apperr.As(domain.ErrTenantForbidden())
	return apperr.Wrap(cause, e.Kind, e.Code, e.Message)
}
