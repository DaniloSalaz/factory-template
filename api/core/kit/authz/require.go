package authz

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"chaskicode.dev/saas/core/kit/apperr"
	"chaskicode.dev/saas/core/kit/authctx"
	"chaskicode.dev/saas/core/kit/db"
	"chaskicode.dev/saas/core/kit/telemetry"
)

// auditTimeout bounds the denial's audit, which also runs if the request is cancelled.
const auditTimeout = 5 * time.Second

// AuditDenied is the audit action of a refused permission (auth-y-tenancy §9).
const AuditDenied = "authz.denied"

// Auditor records a refused permission in the principal's tenant, in a transaction of
// its own: Require hands it a db.Independent context, so it may be called inside the
// caller's transaction and the event survives that transaction's rollback.
type Auditor interface {
	RecordDenied(ctx context.Context, p authctx.Principal, perm authctx.Permission) error
}

// Authorizer is the permission check of the use cases (auth-y-tenancy §5.4.5). Handlers never
// call it: a depguard rule keeps it out of adapters/http.
type Authorizer struct {
	auditor Auditor
	metrics *telemetry.Metrics
	logger  *slog.Logger
}

// NewAuthorizer builds an Authorizer that records denials with auditor and counts them
// in authz.denied, by tenant (metrics may be nil).
func NewAuthorizer(auditor Auditor, metrics *telemetry.Metrics, logger *slog.Logger) *Authorizer {
	return &Authorizer{auditor: auditor, metrics: metrics, logger: logger}
}

// ErrDenied is returned when the principal lacks a permission.
func ErrDenied() error {
	return apperr.Forbidden(AuditDenied, "You do not have permission to do this.")
}

// Require returns nil if the context's principal holds perm. Otherwise it records
// authz.denied in the tenant's audit trail and returns ErrDenied; if the event cannot be
// recorded, it logs why and still refuses. It may be called anywhere in the use case,
// inside its transaction too: the event is written in an independent transaction on
// its own connection, so the caller's rollback does not erase it. Without a principal
// (a use case reached without the tenant middleware) it refuses with an internal error.
func (a *Authorizer) Require(ctx context.Context, perm authctx.Permission) error {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return apperr.Wrap(errors.New("no principal in the context"), apperr.KindInternal,
			"authz.no_principal", "internal error")
	}
	if p.Has(perm) {
		return nil
	}
	a.metrics.AuthzDenied(ctx, p.TenantID())
	auditCtx, cancel := context.WithTimeout(db.Independent(context.WithoutCancel(ctx)), auditTimeout)
	defer cancel()
	if err := a.auditor.RecordDenied(auditCtx, p, perm); err != nil {
		a.logger.ErrorContext(ctx, "record authz.denied",
			slog.String("permission", string(perm)), slog.Any("error", err))
	}
	return ErrDenied()
}
