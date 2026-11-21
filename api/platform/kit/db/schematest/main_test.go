// Package schematest checks the database schema and its row-level security against a
// real Postgres, bootstrapped and migrated like any environment:
//
//   - rules.go (tables with tenant_id, section 7.3), functions.go (SECURITY DEFINER
//     functions, 7.4) and privileges.go (the snapshot of app_runtime's privileges) hold
//     the rules. package main applies them to the whole schema, the platform's and the
//     app's migrations (api/schema_integration_test.go), because nothing under
//     platform/ may import the app's;
//   - structure_test.go and privileges_test.go prove each rule catches a misbuilt
//     object, on the platform's migrations;
//   - behavior_test.go runs SQL as app_runtime and checks what RLS and the SECURITY
//     DEFINER functions actually allow.
package schematest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"

	"chaskicode.dev/saas/platform/kit/db/dbtest"
)

// testDB is shared by every test in the package: starting Postgres once keeps the suite
// fast, and tests isolate themselves with their own tenants, users and transactions.
var testDB *dbtest.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	db, err := dbtest.New(ctx, dbtest.PlatformFromSource())
	if err != nil {
		if db != nil && db.Container != nil {
			_ = testcontainers.TerminateContainer(db.Container)
		}
		fmt.Fprintln(os.Stderr, "start test database:", err)
		os.Exit(1)
	}
	testDB = db

	code := m.Run()
	_ = testcontainers.TerminateContainer(db.Container)
	os.Exit(code)
}

// connect opens a dedicated connection (not a pool), so tests control exactly which
// session their transactions run on.
func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })
	return conn
}
