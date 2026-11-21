package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"chaskicode.dev/saas/platform/kit/db/schematest"
)

// The rules of the schema (design, sections 7.3 and 7.4) and the snapshot of
// app_runtime's privileges, applied to the database `api migrate` builds: the
// platform's migrations and the app's. They live in package main because nothing under
// platform/ may import the app ([platform-imports-app]); schematest holds the rules and
// proves each one catches a misbuilt object.
func TestSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	pg := startDB(t)

	t.Run("tenant tables", func(t *testing.T) {
		t.Parallel()

		conn := connect(t, pg.OwnerURL)
		names, err := schematest.TenantTableNames(t.Context(), conn)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(names, "tenants") {
			t.Fatalf("tenants was not discovered (got %v); the discovery query is broken", names)
		}
		t.Logf("tenant tables: %s", strings.Join(names, ", "))

		violations, err := schematest.TenantTableViolations(t.Context(), conn)
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

		names, violations, err := schematest.SecurityDefinerViolations(t.Context(), connect(t, pg.OwnerURL))
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

		got, err := schematest.RuntimePrivileges(t.Context(), connect(t, pg.SuperURL))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(schematest.RuntimePrivilegesGolden)
		if err != nil {
			t.Fatalf("read %s: %v", schematest.RuntimePrivilegesGolden, err)
		}
		if diff := schematest.PrivilegeDiff(schematest.GoldenLines(raw), got); diff != "" {
			t.Fatalf("[runtime-privileges] the privileges of app_runtime differ from %s\n"+
				"(+ granted but not listed, - listed but not granted):\n\n%s\n"+
				"Either the migration is wrong, or the change is intended: then update the file by hand "+
				"and justify it in the PR, it is a security decision (AGENTS.md). Current privileges:\n\n%s\n",
				schematest.RuntimePrivilegesGolden, diff, strings.Join(got, "\n"))
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
