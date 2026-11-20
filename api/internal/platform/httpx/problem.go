// Package httpx holds HTTP plumbing shared by every module.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
)

// ProblemContentType is the media type defined by RFC 9457.
const ProblemContentType = "application/problem+json"

const (
	internalCode   = "internal"
	internalDetail = "An unexpected error occurred."
)

// Problem is an RFC 9457 problem details object with this API's extension members.
type Problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Detail    string              `json:"detail,omitempty"`
	Instance  string              `json:"instance,omitempty"`
	Code      string              `json:"code,omitempty"`
	Errors    []apperr.FieldError `json:"errors,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
}

// WriteProblem translates err into a problem+json response. apperr errors expose their
// code, message and field errors; anything else, and KindInternal, becomes a generic 500
// whose real cause is only logged.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	ctx := r.Context()
	p := problemFor(err)
	p.Instance = r.URL.Path
	if id, ok := logx.RequestIDFrom(ctx); ok {
		p.RequestID = id
	}

	if p.Status >= http.StatusInternalServerError {
		logger.ErrorContext(ctx, "request failed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", p.Status),
			slog.Any("error", err),
		)
	}

	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(p.Status)
	if encErr := json.NewEncoder(w).Encode(p); encErr != nil {
		logger.WarnContext(ctx, "write problem response", slog.Any("error", encErr))
	}
}

// RequestErrorHandler answers requests the generated server could not decode. The decoder
// error may echo client input, so it is not returned in the body.
func RequestErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		WriteProblem(w, r, apperr.Wrap(err, apperr.KindBadRequest, "invalid_request",
			"The request is malformed or has invalid parameters."), logger)
	}
}

// ResponseErrorHandler answers errors returned by strict handlers.
func ResponseErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		WriteProblem(w, r, err, logger)
	}
}

func problemFor(err error) Problem {
	// Checked first: the generated server wraps body read errors in a BadRequest, which
	// would otherwise hide the more precise 413.
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return Problem{
			Type:   "about:blank",
			Title:  http.StatusText(http.StatusRequestEntityTooLarge),
			Status: http.StatusRequestEntityTooLarge,
			Detail: fmt.Sprintf("The request body exceeds the limit of %d bytes.", tooLarge.Limit),
			Code:   "request_too_large",
		}
	}

	e, ok := apperr.As(err)
	status := http.StatusInternalServerError
	if ok {
		status = statusFor(e.Kind)
	}
	if !ok || status == http.StatusInternalServerError {
		return Problem{
			Type:   "about:blank",
			Title:  http.StatusText(http.StatusInternalServerError),
			Status: http.StatusInternalServerError,
			Detail: internalDetail,
			Code:   internalCode,
		}
	}
	return Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: e.Message,
		Code:   e.Code,
		Errors: e.Fields,
	}
}

func statusFor(kind apperr.Kind) int {
	switch kind {
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindValidation:
		return http.StatusUnprocessableEntity
	case apperr.KindUnauthorized:
		return http.StatusUnauthorized
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindRateLimited:
		return http.StatusTooManyRequests
	case apperr.KindBadRequest:
		return http.StatusBadRequest
	case apperr.KindUnavailable:
		return http.StatusServiceUnavailable
	case apperr.KindInternal:
		return http.StatusInternalServerError
	}
	// No default case, so the exhaustive linter flags any Kind added without a status.
	// Unknown values (a converted integer) still fail closed.
	return http.StatusInternalServerError
}
