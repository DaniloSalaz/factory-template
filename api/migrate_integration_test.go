//go:build integration

package main

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5"

	platformmigrations "chaskicode.dev/saas/platform/migrations"
	"chaskicode.dev/saas/tests/testkit"
)

// startDB runs a database migrated with the sets `api migrate` applies, in its order.
func startDB(t *testing.T) *testkit.DB {
	t.Helper()
	return testkit.Start(t, migrationSets()...)
}

// `api migrate` exits 0 when everything is applied and non-zero on any failure, without
// starting the server. testkit.New already applied the embedded migrations through the
// same db.Migrate, so here there is nothing left to apply.
func TestMigrateCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx := t.Context()
	pg := startDB(t)

	embedded, err := fs.Glob(platformmigrations.Set().FS, "*.sql")
	if err != nil || len(embedded) == 0 {
		t.Fatalf("embedded platform migrations: %v %v", embedded, err)
	}
	sets := migrationSets()
	if len(sets) != 2 || sets[0].Name != "platform" || sets[1].Name != "app" {
		t.Fatalf("api migrate must apply the platform's set and then the app's, got %v", sets)
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

	// Both sets keep their own version table, the platform's with goose's default name
	// so that databases migrated before the split see no change.
	conn := connect(t, pg.OwnerURL)
	for _, table := range []string{"goose_db_version", "goose_app_db_version"} {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("version table %s does not exist", table)
		}
	}
}

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })
	return conn
}
