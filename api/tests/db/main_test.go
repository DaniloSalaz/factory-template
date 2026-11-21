//go:build integration

package db

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"

	"chaskicode.dev/saas/tests/testkit"
	"chaskicode.dev/saas/tests/testkit/migrationsets"
)

// testDB is shared by every test in the package: starting Postgres once keeps the suite
// fast, and tests isolate themselves with their own tenants, users and transactions.
var testDB *testkit.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	db, err := testkit.New(ctx, migrationsets.All()...)
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
