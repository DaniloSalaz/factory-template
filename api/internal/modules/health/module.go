// Package health is the module's public API: the only package other code may import from it.
package health

import (
	"log/slog"
	"net/http"
	"time"

	healthapi "github.com/DaniloSalaz/factory-template/api/gen/openapi/health"
	healthdb "github.com/DaniloSalaz/factory-template/api/gen/sqlc/health"
	healthhttp "github.com/DaniloSalaz/factory-template/api/internal/modules/health/adapters/http"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/health/adapters/postgres"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/health/app"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
)

// readinessTimeout keeps /readyz under typical orchestrator probe timeouts.
const readinessTimeout = 2 * time.Second

// Deps are the module's external dependencies.
type Deps struct {
	DB     healthdb.DBTX
	Logger *slog.Logger
}

// Module wires the health module.
type Module struct {
	handler *healthhttp.Handler
	logger  *slog.Logger
}

// New wires adapters and use cases.
func New(deps Deps) *Module {
	readiness := app.NewCheckReadiness(postgres.NewChecker(deps.DB), readinessTimeout)
	return &Module{
		handler: healthhttp.NewHandler(readiness, deps.Logger),
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
