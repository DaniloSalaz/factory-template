//go:build integration

package db_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"chaskicode.dev/saas/tests/db"
	"chaskicode.dev/saas/tests/testkit"
	"chaskicode.dev/saas/tests/testkit/migrationsets"
)

// The rules of the schema (auth-y-tenancy §7.3 and §7.4) and the snapshot of
// app_runtime's privileges, applied to the database `api migrate` builds: the
// platform's migrations and the app's. Package db holds the rules and proves each one
// catches a misbuilt object.
func TestSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	pg := testkit.Start(t, migrationsets.All()...)

	t.Run("tenant tables", func(t *testing.T) {
		t.Parallel()

		conn := connect(t, pg.OwnerURL)
		names, err := db.TenantTableNames(t.Context(), conn)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(names, "tenants") {
			t.Fatalf("tenants was not discovered (got %v); the discovery query is broken", names)
		}
		t.Logf("tenant tables: %s", strings.Join(names, ", "))

		violations, err := db.TenantTableViolations(t.Context(), conn)
		if err != nil {
			t.Fatal(err)
		}
		for _, vs := range violations {
			for _, v := range vs {
				t.Error(v)
			}
		}
	})

	t.Run("security definer functions", func(t *testing.T) {
		t.Parallel()

		names, violations, err := db.SecurityDefinerViolations(t.Context(), connect(t, pg.OwnerURL))
		if err != nil {
			t.Fatal(err)
		}
		if len(names) == 0 {
			t.Fatal("no SECURITY DEFINER function was discovered; the discovery query is broken")
		}
		t.Logf("SECURITY DEFINER functions: %s", strings.Join(names, ", "))
		for _, v := range violations {
			t.Error(v)
		}
	})

	t.Run("runtime privileges snapshot", func(t *testing.T) {
		t.Parallel()

		got, err := db.RuntimePrivileges(t.Context(), connect(t, pg.SuperURL))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(db.RuntimePrivilegesGolden)
		if err != nil {
			t.Fatalf("read %s: %v", db.RuntimePrivilegesGolden, err)
		}
		if diff := db.PrivilegeDiff(db.GoldenLines(raw), got); diff != "" {
			t.Fatalf("[runtime-privileges] the privileges of app_runtime differ from %s\n"+
				"(+ granted but not listed, - listed but not granted):\n\n%s\n"+
				"Either the migration is wrong, or the change is intended: then update the file by hand "+
				"and justify it in the PR, it is a security decision (AGENTS.md). Current privileges:\n\n%s\n",
				db.RuntimePrivilegesGolden, diff, strings.Join(got, "\n"))
		}
	})
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
