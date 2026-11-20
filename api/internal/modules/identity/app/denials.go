package app

import (
	"context"
	"fmt"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authz"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/clientip"
)

// DenialAuditor records authz.denied in the principal's tenant, with the principal as
// the actor (design, section 9). It implements authz.Auditor.
type DenialAuditor struct {
	store Store
}

var _ authz.Auditor = DenialAuditor{}

// NewDenialAuditor builds a DenialAuditor.
func NewDenialAuditor(store Store) DenialAuditor {
	return DenialAuditor{store: store}
}

// RecordDenied implements authz.Auditor.
func (d DenialAuditor) RecordDenied(ctx context.Context, p authctx.Principal, perm authctx.Permission) error {
	e := AuditEvent{
		Action: authz.AuditDenied, TargetType: "permission", TargetID: string(perm),
		Metadata: map[string]string{"role": p.Role()},
	}
	if prefix, ok := clientip.PrefixFrom(ctx); ok {
		e.IPPrefix = prefix
	}
	err := d.store.InTenant(ctx, p, func(ctx context.Context, r Repo) error {
		return r.RecordTenantAudit(ctx, p.TenantID(), e)
	})
	if err != nil {
		return fmt.Errorf("record %s: %w", authz.AuditDenied, err)
	}
	return nil
}
