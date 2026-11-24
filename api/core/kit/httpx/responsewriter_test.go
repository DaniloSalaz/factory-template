package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseWriter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		write      func(w http.ResponseWriter)
		wantStatus int
		wantBytes  int64
		wantSent   int // status seen by the underlying writer; 0 skips the check
	}{
		{"nothing written defaults to 200", func(http.ResponseWriter) {}, 200, 0, 200},
		{"implicit 200 on write", func(w http.ResponseWriter) { _, _ = w.Write([]byte("hello")) }, 200, 5, 200},
		{"explicit status", func(w http.ResponseWriter) { w.WriteHeader(http.StatusCreated); _, _ = w.Write([]byte("ab")) }, 201, 2, 201},
		{"second WriteHeader is ignored", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound)
			w.WriteHeader(http.StatusInternalServerError)
		}, 404, 0, 404},
		{"WriteHeader after Write is ignored", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("x"))
			w.WriteHeader(http.StatusInternalServerError)
		}, 200, 1, 200},
		// httptest.ResponseRecorder keeps the first code it sees, 1xx included, so only the
		// wrapper's own state is checked here.
		{"1xx does not count as the final status", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusEarlyHints)
			w.WriteHeader(http.StatusNoContent)
		}, 204, 0, 0},
		{"bytes accumulate", func(w http.ResponseWriter) { _, _ = w.Write([]byte("ab")); _, _ = w.Write([]byte("cde")) }, 200, 5, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			rw := wrapResponseWriter(rec)
			tc.write(rw)

			if rw.Status() != tc.wantStatus || rw.bytes != tc.wantBytes {
				t.Fatalf("status=%d bytes=%d, want %d %d", rw.Status(), rw.bytes, tc.wantStatus, tc.wantBytes)
			}
			if tc.wantSent != 0 && rec.Code != tc.wantSent {
				t.Fatalf("underlying status = %d, want %d", rec.Code, tc.wantSent)
			}
		})
	}
}

func TestResponseWriter_WrapIsIdempotent(t *testing.T) {
	t.Parallel()

	rw := wrapResponseWriter(httptest.NewRecorder())
	if wrapResponseWriter(rw) != rw {
		t.Fatal("wrapping a *responseWriter must return the same instance")
	}
}

func TestResponseWriter_ResponseControllerReachesUnderlyingWriter(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)
	if rw.headerWritten() {
		t.Fatal("header reported as written before any write")
	}
	if err := http.NewResponseController(rw).Flush(); err != nil {
		t.Fatalf("Flush through wrapper: %v", err)
	}
	if !rec.Flushed {
		t.Fatal("underlying recorder was not flushed")
	}
	if !rw.headerWritten() && rec.Code != http.StatusOK {
		t.Fatalf("unexpected state after flush: code=%d", rec.Code)
	}
}
