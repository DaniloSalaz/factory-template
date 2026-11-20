package health_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/health"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db/dbtest"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
)

func TestReadyz_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx := t.Context()

	// Bootstrapped and migrated like a real environment; the app connects as app_runtime.
	db := dbtest.Start(t)
	ctr := db.Container
	dsn := db.RuntimeURL

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	mux := http.NewServeMux()
	health.New(health.Deps{DB: pool, Logger: slog.New(slog.DiscardHandler)}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Run("200 when the database is up", func(t *testing.T) {
		res := get(t, srv.URL+"/readyz")
		if res.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.status)
		}
	})

	if err := ctr.Stop(ctx, nil); err != nil {
		t.Fatalf("stop postgres: %v", err)
	}

	t.Run("503 problem when the database is down", func(t *testing.T) {
		res := get(t, srv.URL+"/readyz")
		if res.status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", res.status)
		}
		if ct := res.header.Get("Content-Type"); ct != httpx.ProblemContentType {
			t.Fatalf("content-type = %q, want %q", ct, httpx.ProblemContentType)
		}
		var p httpx.Problem
		if err := json.Unmarshal(res.body, &p); err != nil {
			t.Fatalf("decode problem: %v", err)
		}
		if p.Status != http.StatusServiceUnavailable || p.Code != "health.db_unavailable" || p.Title == "" || p.Type == "" {
			t.Fatalf("unexpected problem: %+v", p)
		}
	})
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func get(t *testing.T, url string) response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{status: res.StatusCode, header: res.Header, body: body}
}
