package httpx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"chaskicode.dev/saas/platform/apperr"
	"chaskicode.dev/saas/platform/config"
	"chaskicode.dev/saas/platform/httpx"
	"chaskicode.dev/saas/platform/logx"
)

func TestWriteProblem(t *testing.T) {
	t.Parallel()

	field := apperr.FieldError{Field: "email", Code: "required", Message: "email is required"}
	tests := []struct {
		name      string
		err       error
		requestID string
		want      httpx.Problem
		wantLog   string // substring expected in the log; empty means nothing is logged
		leak      string // must not appear in the response body
	}{
		{
			name: "not found",
			err:  apperr.NotFound("user_not_found", "User not found."),
			want: httpx.Problem{Type: "about:blank", Title: "Not Found", Status: 404, Detail: "User not found.", Code: "user_not_found"},
		},
		{
			name: "conflict wrapped with fmt",
			err:  fmt.Errorf("create user: %w", apperr.Conflict("email_taken", "Email already registered.")),
			want: httpx.Problem{Type: "about:blank", Title: "Conflict", Status: 409, Detail: "Email already registered.", Code: "email_taken"},
		},
		{
			name: "validation with fields",
			err:  apperr.Validation("invalid_input", "Invalid input.", field),
			want: httpx.Problem{Type: "about:blank", Title: "Unprocessable Entity", Status: 422, Detail: "Invalid input.", Code: "invalid_input", Errors: []apperr.FieldError{field}},
		},
		{
			name: "unauthorized",
			err:  apperr.Unauthorized("no_session", "Login required."),
			want: httpx.Problem{Type: "about:blank", Title: "Unauthorized", Status: 401, Detail: "Login required.", Code: "no_session"},
		},
		{
			name: "forbidden",
			err:  apperr.Forbidden("not_owner", "Not allowed."),
			want: httpx.Problem{Type: "about:blank", Title: "Forbidden", Status: 403, Detail: "Not allowed.", Code: "not_owner"},
		},
		{
			name:      "rate limited with request id",
			err:       apperr.RateLimited("too_many_requests", "Slow down.", 0),
			requestID: "req-42",
			want:      httpx.Problem{Type: "about:blank", Title: "Too Many Requests", Status: 429, Detail: "Slow down.", Code: "too_many_requests", RequestID: "req-42"},
		},
		{
			name: "bad request",
			err:  apperr.BadRequest("malformed_json", "Body is not valid JSON."),
			want: httpx.Problem{Type: "about:blank", Title: "Bad Request", Status: 400, Detail: "Body is not valid JSON.", Code: "malformed_json"},
		},
		{
			name:    "unavailable is logged with its cause",
			err:     apperr.Wrap(errors.New("dial tcp: connection refused"), apperr.KindUnavailable, "db_unavailable", "Database unavailable."),
			want:    httpx.Problem{Type: "about:blank", Title: "Service Unavailable", Status: 503, Detail: "Database unavailable.", Code: "db_unavailable"},
			wantLog: "connection refused",
			leak:    "connection refused",
		},
		{
			name: "body too large",
			err:  fmt.Errorf("read body: %w", &http.MaxBytesError{Limit: 1024}),
			want: httpx.Problem{Type: "about:blank", Title: "Request Entity Too Large", Status: 413, Detail: "The request body exceeds the limit of 1024 bytes.", Code: "request_too_large"},
		},
		{
			name: "body too large wins over the BadRequest that wraps it",
			err:  apperr.Wrap(&http.MaxBytesError{Limit: 1024}, apperr.KindBadRequest, "invalid_request", "bad"),
			want: httpx.Problem{Type: "about:blank", Title: "Request Entity Too Large", Status: 413, Detail: "The request body exceeds the limit of 1024 bytes.", Code: "request_too_large"},
		},
		{
			name:    "plain error is a generic 500",
			err:     errors.New("boom"),
			want:    httpx.Problem{Type: "about:blank", Title: "Internal Server Error", Status: 500, Detail: "An unexpected error occurred.", Code: "internal"},
			wantLog: "boom",
			leak:    "boom",
		},
		{
			name:      "internal apperr hides message and cause",
			err:       apperr.Wrap(errors.New("pq: deadlock detected"), apperr.KindInternal, "db_deadlock", "storage failed"),
			requestID: "req-43",
			want:      httpx.Problem{Type: "about:blank", Title: "Internal Server Error", Status: 500, Detail: "An unexpected error occurred.", Code: "internal", RequestID: "req-43"},
			wantLog:   "pq: deadlock detected",
			leak:      "deadlock",
		},
		{
			name:    "unknown kind fails closed",
			err:     &apperr.Error{Kind: apperr.Kind(99), Code: "weird", Message: "weird"},
			want:    httpx.Problem{Type: "about:blank", Title: "Internal Server Error", Status: 500, Detail: "An unexpected error occurred.", Code: "internal"},
			wantLog: "weird",
			leak:    "weird",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			logger := logx.New(&logs, config.Log{Level: slog.LevelDebug, Format: config.LogFormatJSON})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/users/7", nil)
			if tc.requestID != "" {
				req = req.WithContext(logx.WithRequestID(req.Context(), tc.requestID))
			}
			rec := httptest.NewRecorder()

			httpx.WriteProblem(rec, req, tc.err, logger)

			if rec.Code != tc.want.Status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want.Status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != httpx.ProblemContentType {
				t.Fatalf("content-type = %q, want %q", ct, httpx.ProblemContentType)
			}
			body := rec.Body.String()
			if tc.leak != "" && strings.Contains(body, tc.leak) {
				t.Fatalf("body leaks %q: %s", tc.leak, body)
			}

			var got httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			tc.want.Instance = "/users/7"
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("problem =\n%+v\nwant\n%+v", got, tc.want)
			}

			switch {
			case tc.wantLog == "" && logs.Len() > 0:
				t.Fatalf("unexpected log for a client error: %s", logs.String())
			case tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog):
				t.Fatalf("log does not contain %q: %s", tc.wantLog, logs.String())
			case tc.requestID != "" && tc.wantLog != "" && !strings.Contains(logs.String(), tc.requestID):
				t.Fatalf("log does not contain request id: %s", logs.String())
			}
		})
	}
}

func TestStrictErrorHandlers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		handler    func(*slog.Logger) func(http.ResponseWriter, *http.Request, error)
		err        error
		wantStatus int
		wantCode   string
		leak       string
	}{
		{"request decode error is 400 without echoing input", httpx.RequestErrorHandler, errors.New(`invalid value "s3cr3t" for id`), 400, "invalid_request", "s3cr3t"},
		{"response error is translated", httpx.ResponseErrorHandler, apperr.NotFound("x_not_found", "Not found."), 404, "x_not_found", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
			tc.handler(slog.New(slog.DiscardHandler))(rec, req, tc.err)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.leak != "" && strings.Contains(rec.Body.String(), tc.leak) {
				t.Fatalf("body leaks input: %s", rec.Body.String())
			}
			var p httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != tc.wantCode {
				t.Fatalf("code = %q (err %v), want %q", p.Code, err, tc.wantCode)
			}
		})
	}
}

func TestWriteProblem_RetryAfter(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"whole seconds", apperr.RateLimited("rate_limit.exceeded", "Slow down.", 30*time.Second), "30"},
		{"a fraction rounds up", apperr.RateLimited("rate_limit.exceeded", "Slow down.", 1500*time.Millisecond), "2"},
		{"wrapped", fmt.Errorf("verify: %w", apperr.RateLimited("mfa.rate_limited", "Slow down.", time.Minute)), "60"},
		{"unknown wait sends none", apperr.RateLimited("rate_limit.exceeded", "Slow down.", 0), ""},
		{"only on 429", apperr.Conflict("x", "y"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			httpx.WriteProblem(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil), tc.err, logger)
			if got := rec.Header().Get("Retry-After"); got != tc.want {
				t.Fatalf("Retry-After = %q, want %q", got, tc.want)
			}
		})
	}
}
