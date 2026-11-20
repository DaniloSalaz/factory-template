package app

import (
	"context"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/health/domain"
)

// CheckReadiness decides whether the service is ready to receive traffic.
type CheckReadiness struct {
	db      DBChecker
	timeout time.Duration
}

// NewCheckReadiness builds the use case; timeout bounds each dependency check so probes never hang.
func NewCheckReadiness(db DBChecker, timeout time.Duration) *CheckReadiness {
	return &CheckReadiness{db: db, timeout: timeout}
}

// Execute runs the dependency checks.
func (uc *CheckReadiness) Execute(ctx context.Context) domain.Readiness {
	ctx, cancel := context.WithTimeout(ctx, uc.timeout)
	defer cancel()

	if err := uc.db.CheckDB(ctx); err != nil {
		return domain.NotReady("database unavailable", err)
	}
	return domain.Ready()
}
