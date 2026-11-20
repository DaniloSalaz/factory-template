package authz

import (
	"context"
	"errors"
	"log/slog"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
)

// AuditDenied is the audit action of a refused permission (design, section 9).
const AuditDenied = "authz.denied"

// Auditor records a refused permission in the principal's tenant. It runs its own
// transaction, so Require must be called before the use case opens one.
type Auditor interface {
	RecordDenied(ctx context.Context, p authctx.Principal, perm authctx.Permission) error
}

// Authorizer is the permission check of the use cases (design, 5.4.5). Handlers never
// call it: a depguard rule keeps it out of adapters/http.
type Authorizer struct {
	auditor Auditor
	logger  *slog.Logger
}

// NewAuthorizer builds an Authorizer that records denials with auditor.
func NewAuthorizer(auditor Auditor, logger *slog.Logger) *Authorizer {
	return &Authorizer{auditor: auditor, logger: logger}
}

// ErrDenied is returned when the principal lacks a permission.
func ErrDenied() error {
	return apperr.Forbidden(AuditDenied, "You do not have permission to do this.")
}

// Require returns nil if the context's principal holds perm. Otherwise it records
// authz.denied in the tenant's audit trail and returns ErrDenied; if the event cannot be
// recorded, it logs why and still refuses. Without a principal (a use case reached
// without the tenant middleware) it refuses with an internal error.
func (a *Authorizer) Require(ctx context.Context, perm authctx.Permission) error {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return apperr.Wrap(errors.New("no principal in the context"), apperr.KindInternal,
			"authz.no_principal", "internal error")
	}
	if p.Has(perm) {
		return nil
	}
	if err := a.auditor.RecordDenied(ctx, p, perm); err != nil {
		a.logger.ErrorContext(ctx, "record authz.denied",
			slog.String("permission", string(perm)), slog.Any("error", err))
	}
	return ErrDenied()
}
