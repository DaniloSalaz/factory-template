package main

import (
	"io/fs"
	"testing"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/db/dbtest"
	"github.com/DaniloSalaz/factory-template/api/migrations"
)

// `api migrate` exits 0 when everything is applied and non-zero on any failure, without
// starting the server. dbtest.New already applied the embedded migrations through the
// same db.Migrate, so here there is nothing left to apply.
func TestMigrateCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx := t.Context()
	pg, err := dbtest.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Container.Terminate(ctx) })

	embedded, err := fs.Glob(migrations.FS(), "*.sql")
	if err != nil || len(embedded) == 0 {
		t.Fatalf("embedded migrations: %v %v", embedded, err)
	}

	for _, tc := range []struct {
		name, env, url string
		want           int
	}{
		{"up to date", "production", pg.OwnerURL, 0},
		{"no configuration", "production", "", 1},
		{"database unreachable", "production", "postgres://app_owner:x@127.0.0.1:1/factory?connect_timeout=2", 1},
		// app_runtime owns nothing: it cannot even create goose's version table.
		{"not the owner", "production", pg.RuntimeURL, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.env)
			t.Setenv("MIGRATIONS_DATABASE_URL", tc.url)
			t.Setenv("LOG_LEVEL", "error")
			if got := migrateCommand(ctx); got != tc.want {
				t.Fatalf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}
