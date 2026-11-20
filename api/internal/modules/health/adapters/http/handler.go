// Package http exposes the health module over HTTP via the generated OpenAPI server.
package http

import (
	"context"
	"log/slog"
	"net/http"

	healthapi "github.com/DaniloSalaz/factory-template/api/gen/openapi/health"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/health/app"
)

var _ healthapi.StrictServerInterface = (*Handler)(nil)

// Handler implements the generated strict server interface.
type Handler struct {
	readiness *app.CheckReadiness
	logger    *slog.Logger
}

// NewHandler builds a Handler.
func NewHandler(readiness *app.CheckReadiness, logger *slog.Logger) *Handler {
	return &Handler{readiness: readiness, logger: logger}
}

// GetHealthz reports that the process is alive; it deliberately checks no dependencies.
func (h *Handler) GetHealthz(context.Context, healthapi.GetHealthzRequestObject) (healthapi.GetHealthzResponseObject, error) {
	return healthapi.GetHealthz200JSONResponse{Status: healthapi.Ok}, nil
}

// GetReadyz reports whether the service and its dependencies can take traffic.
func (h *Handler) GetReadyz(ctx context.Context, _ healthapi.GetReadyzRequestObject) (healthapi.GetReadyzResponseObject, error) {
	readiness := h.readiness.Execute(ctx)
	if readiness.IsReady() {
		return healthapi.GetReadyz200JSONResponse{Status: healthapi.Ok}, nil
	}

	h.logger.WarnContext(ctx, "not ready", slog.String("reason", readiness.Reason()), slog.Any("error", readiness.Cause()))
	detail := readiness.Reason()
	instance := "/readyz"
	return healthapi.GetReadyz503ApplicationProblemPlusJSONResponse{
		Type:     "about:blank",
		Title:    http.StatusText(http.StatusServiceUnavailable),
		Status:   http.StatusServiceUnavailable,
		Detail:   &detail,
		Instance: &instance,
	}, nil
}
