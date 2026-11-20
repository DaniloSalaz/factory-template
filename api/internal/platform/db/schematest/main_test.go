// Package schematest checks the database schema and its row-level security against a
// real Postgres, bootstrapped and migrated like any environment. structure_test.go reads
// the catalogs (pg_class, pg_policies, pg_proc), checks the rules of the design
// (sections 7.3 and 7.4) and proves each check catches a misbuilt object.
package schematest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/db/dbtest"
)

// testDB is shared by every test in the package: starting Postgres once keeps the suite
// fast, and tests isolate themselves with their own tenants, users and transactions.
var testDB *dbtest.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	db, err := dbtest.New(ctx)
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
