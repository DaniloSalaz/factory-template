package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
)

func TestChain_FirstIsOutermost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mws  []string
		want []string
	}{
		{"no middlewares", nil, []string{"handler"}},
		{"one", []string{"a"}, []string{"a>", "handler", "<a"}},
		{"three", []string{"a", "b", "c"}, []string{"a>", "b>", "c>", "handler", "<c", "<b", "<a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var trace []string
			mws := make([]httpx.Middleware, 0, len(tc.mws))
			for _, name := range tc.mws {
				mws = append(mws, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						trace = append(trace, name+">")
						next.ServeHTTP(w, r)
						trace = append(trace, "<"+name)
					})
				})
			}
			h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				trace = append(trace, "handler")
			}), mws...)

			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			if !slices.Equal(trace, tc.want) {
				t.Fatalf("trace = %v, want %v", trace, tc.want)
			}
		})
	}
}
