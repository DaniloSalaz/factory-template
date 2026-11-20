package httpx_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
)

func TestResponseErrorHandler_WritesProblemWithoutInternals(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", nil)

	httpx.ResponseErrorHandler(logger)(rec, req, errors.New("secret detail"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != httpx.ProblemContentType {
		t.Fatalf("content-type = %q", ct)
	}
	var p httpx.Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := httpx.Problem{Type: "about:blank", Title: "Internal Server Error", Status: 500, Instance: "/boom"}
	if p != want {
		t.Fatalf("problem = %+v, want %+v", p, want)
	}
}
