// Package http exposes the health module over HTTP via the generated OpenAPI server.
package http

import (
	"context"
	"fmt"

	healthapi "chaskicode.dev/saas/gen/openapi/health"
	"chaskicode.dev/saas/modules/health/app"
)

var _ healthapi.StrictServerInterface = (*Handler)(nil)

// Handler implements the generated strict server interface. Errors are returned, not
// written: the strict server hands them to httpx.WriteProblem.
type Handler struct {
	readiness *app.CheckReadiness
	version   string
}

// NewHandler builds a Handler; version is the running build.
func NewHandler(readiness *app.CheckReadiness, version string) *Handler {
	return &Handler{readiness: readiness, version: version}
}

// GetHealthz reports that the process is alive, and which build it runs; it deliberately
// checks no dependencies.
func (h *Handler) GetHealthz(context.Context, healthapi.GetHealthzRequestObject) (healthapi.GetHealthzResponseObject, error) {
	return healthapi.GetHealthz200JSONResponse{Status: healthapi.LivenessStatusOk, Version: h.version}, nil
}

// GetReadyz reports whether the service and its dependencies can take traffic.
func (h *Handler) GetReadyz(ctx context.Context, _ healthapi.GetReadyzRequestObject) (healthapi.GetReadyzResponseObject, error) {
	if err := h.readiness.Execute(ctx); err != nil {
		return nil, fmt.Errorf("check readiness: %w", err)
	}
	return healthapi.GetReadyz200JSONResponse{Status: healthapi.StatusStatusOk}, nil
}
