package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKnownBodyFields(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	var got string
	echo := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("PATCH /tenant", echo)
	mux.HandleFunc("POST /free", echo)
	h := Chain(mux, BodyLimit(64), KnownBodyFields(mux, map[string][]string{"PATCH /tenant": {"name"}}, slog.New(slog.DiscardHandler)))

	tests := []struct {
		name, method, path, body string
		status                   int
		unknown                  []string
	}{
		{"declared fields pass, body intact", http.MethodPatch, "/tenant", `{"name":"Acme"}`, http.StatusNoContent, nil},
		{"unknown fields are refused", http.MethodPatch, "/tenant", `{"name":"Acme","slug":"x","zeta":1}`, http.StatusBadRequest, []string{"slug", "zeta"}},
		{"field names are case-sensitive", http.MethodPatch, "/tenant", `{"Name":"Acme"}`, http.StatusBadRequest, []string{"Name"}},
		{"not an object is left to the decoder", http.MethodPatch, "/tenant", `["slug"]`, http.StatusNoContent, nil},
		{"routes outside the table are not checked", http.MethodPost, "/free", `{"anything":true}`, http.StatusNoContent, nil},
		{"over the body limit is 413", http.MethodPatch, "/tenant", `{"name":"` + strings.Repeat("a", 100) + `"}`, http.StatusRequestEntityTooLarge, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got = ""
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader(tc.body)))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.status == http.StatusNoContent && got != tc.body {
				t.Fatalf("handler read %q, want the whole body %q", got, tc.body)
			}
			if tc.unknown == nil {
				return
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != "unknown_field" || len(p.Errors) != len(tc.unknown) {
				t.Fatalf("problem = %s (%v)", rec.Body, err)
			}
			for i, f := range tc.unknown {
				if p.Errors[i].Field != f {
					t.Fatalf("errors = %+v, want fields %v", p.Errors, tc.unknown)
				}
			}
		})
	}
}
