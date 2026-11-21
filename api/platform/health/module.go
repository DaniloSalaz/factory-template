// Package health is the module's public API: the only package other code may import from it.
package health

import (
	"log/slog"
	"net/http"
	"time"

	healthapi "chaskicode.dev/saas/gen/openapi/health"
	healthdb "chaskicode.dev/saas/gen/sqlc/health"
	healthhttp "chaskicode.dev/saas/modules/health/adapters/http"
	"chaskicode.dev/saas/modules/health/adapters/postgres"
	"chaskicode.dev/saas/modules/health/app"
	"chaskicode.dev/saas/platform/httpx"
)

// readinessTimeout keeps /readyz under typical orchestrator probe timeouts.
const readinessTimeout = 2 * time.Second

// Deps are the module's external dependencies.
type Deps struct {
	DB     healthdb.DBTX
	Logger *slog.Logger
	// Version is the running build, which /healthz reports.
	Version string
}

// Module wires the health module.
type Module struct {
	handler *healthhttp.Handler
	logger  *slog.Logger
}

// New wires adapters and use cases.
func New(deps Deps) *Module {
	readiness := usecases.NewCheckReadiness(postgres.NewChecker(deps.DB), readinessTimeout)
	return &Module{
		handler: healthhttp.NewHandler(readiness, deps.Version),
		logger:  deps.Logger,
	}
}

// Register mounts the module's routes on mux.
func (m *Module) Register(mux *http.ServeMux) {
	strict := healthapi.NewStrictHandlerWithOptions(m.handler, nil, healthapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  httpx.RequestErrorHandler(m.logger),
		ResponseErrorHandlerFunc: httpx.ResponseErrorHandler(m.logger),
	})
	healthapi.HandlerWithOptions(strict, healthapi.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: httpx.RequestErrorHandler(m.logger),
	})
}
