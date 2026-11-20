package db_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/db/dbtest"
)

// testDB is shared by the package's integration tests: the real migrations plus the
// example business tables of testdata/migrations. Tests isolate themselves with their
// own tenants and users.
var testDB *dbtest.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	db, err := dbtest.New(ctx, "testdata/migrations")
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
