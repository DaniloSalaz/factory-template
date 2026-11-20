// Package httpx holds HTTP server plumbing shared by every module.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ProblemContentType is the media type defined by RFC 9457.
const ProblemContentType = "application/problem+json"

// Problem is an RFC 9457 problem details object.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// NewProblem returns a Problem of type about:blank whose title is the standard status text.
func NewProblem(status int, detail string) Problem {
	return Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}
}

// WriteProblem writes p as the response body with the problem+json content type.
func WriteProblem(w http.ResponseWriter, logger *slog.Logger, p Problem) {
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(p); err != nil {
		logger.Error("write problem response", slog.Any("error", err))
	}
}

// RequestErrorHandler answers requests the generated server could not decode with a 400 problem.
func RequestErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		WriteProblem(w, logger, Problem{
			Type:     "about:blank",
			Title:    http.StatusText(http.StatusBadRequest),
			Status:   http.StatusBadRequest,
			Detail:   err.Error(),
			Instance: r.URL.Path,
		})
	}
}

// ResponseErrorHandler logs handler failures and answers with a 500 problem without leaking internals.
func ResponseErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		logger.ErrorContext(r.Context(), "handler failed", slog.String("path", r.URL.Path), slog.Any("error", err))
		p := NewProblem(http.StatusInternalServerError, "")
		p.Instance = r.URL.Path
		WriteProblem(w, logger, p)
	}
}
