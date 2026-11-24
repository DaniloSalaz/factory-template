//go:build integration

package db_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"chaskicode.dev/saas/core/kit/db"
)

// Each test uses its own version tables and zz_mig_* tables (no tenant_id, so the
// schema rules of the package's other tests ignore them) on the shared database.

func migration(sql string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("-- +goose Up\n" + sql + "\n")}
}

func tableExists(t *testing.T, conn *pgx.Conn, table string) bool {
	t.Helper()

	var exists bool
	if err := conn.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

func versions(t *testing.T, conn *pgx.Conn, table string) []int64 {
	t.Helper()

	rows, err := conn.Query(t.Context(), "SELECT version_id FROM "+pgx.Identifier{table}.Sanitize()+" ORDER BY id")
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatalf("collect %s: %v", table, err)
	}
	return got
}

func connectOwner(t *testing.T) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), testDB.OwnerURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })
	return conn
}

// A set without migrations (the template's app set) applies nothing, without error, and
// still gets its version table, as goose creates it: only the version 0 row.
func TestMigrate_EmptySet(t *testing.T) {
	t.Parallel()

	conn := connectOwner(t)
	// Like app/migrations: only the Go file that embeds the directory, which goose ignores.
	set := db.MigrationSet{Name: "empty", FS: fstest.MapFS{"migrations.go": {Data: []byte("package migrations\n")}}, Table: "goose_test_empty_version", AllowEmpty: true}
	for range 2 { // the second run finds the table and does nothing
		results, err := db.Migrate(t.Context(), testDB.OwnerURL, []db.MigrationSet{set})
		if err != nil {
			t.Fatalf("migrate an empty set: %v", err)
		}
		if len(results) != 1 || results[0].Set != "empty" || len(results[0].Applied) != 0 {
			t.Fatalf("results = %+v, want one empty result for set empty", results)
		}
		if got := versions(t, conn, set.Table); len(got) != 1 || got[0] != 0 {
			t.Fatalf("%s holds versions %v, want only 0", set.Table, got)
		}
	}
}

// Without AllowEmpty, a set with no migrations is an error: goose reports a directory
// that does not exist the same way, and "migrating" it would leave the database without
// its schema. Nothing is created, and the sets after it do not run.
func TestMigrate_EmptySetNotAllowed(t *testing.T) {
	t.Parallel()

	conn := connectOwner(t)
	sets := []db.MigrationSet{
		{Name: "wrongpath", FS: os.DirFS("testdata/does-not-exist"), Table: "goose_test_wrongpath_version"},
		{Name: "next", FS: fstest.MapFS{"00001_a.sql": migration("CREATE TABLE zz_mig_next (id int);")}, Table: "goose_test_next_version"},
	}
	results, err := db.Migrate(t.Context(), testDB.OwnerURL, sets)
	if err == nil || !strings.Contains(err.Error(), "set wrongpath has no migrations") {
		t.Fatalf("err = %v, want the set named as having no migrations", err)
	}
	if want := []db.MigrationResult{{Set: "wrongpath"}}; !equalResults(results, want) {
		t.Fatalf("results = %+v, want %+v", results, want)
	}
	for _, table := range []string{"goose_test_wrongpath_version", "goose_test_next_version", "zz_mig_next"} {
		if tableExists(t, conn, table) {
			t.Errorf("%s was created", table)
		}
	}
}

// Two sets with the same numbering do not collide: each one keeps its own table.
func TestMigrate_SetsKeepTheirOwnVersions(t *testing.T) {
	t.Parallel()

	conn := connectOwner(t)
	sets := []db.MigrationSet{
		{Name: "first", FS: fstest.MapFS{"00001_a.sql": migration("CREATE TABLE zz_mig_first (id int PRIMARY KEY);")}, Table: "goose_test_first_version"},
		{Name: "second", FS: fstest.MapFS{"00001_a.sql": migration("CREATE TABLE zz_mig_second (id int REFERENCES zz_mig_first (id));"), "00002_b.sql": migration("SELECT 1;")}, Table: "goose_test_second_version"},
	}
	results, err := db.Migrate(t.Context(), testDB.OwnerURL, sets)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	want := []db.MigrationResult{{Set: "first", Applied: []int64{1}}, {Set: "second", Applied: []int64{1, 2}}}
	if !equalResults(results, want) {
		t.Fatalf("results = %+v, want %+v", results, want)
	}
	if got := versions(t, conn, "goose_test_second_version"); len(got) != 3 {
		t.Fatalf("second set versions = %v, want 0, 1, 2", got)
	}

	// Nothing pending: every set reports nothing applied.
	results, err = db.Migrate(t.Context(), testDB.OwnerURL, sets)
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	want = []db.MigrationResult{{Set: "first"}, {Set: "second"}}
	if !equalResults(results, want) {
		t.Fatalf("second run results = %+v, want %+v", results, want)
	}
}

// A failure stops the run: the sets after it never start, so the app's migrations never
// run on top of a platform that did not finish.
func TestMigrate_StopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()

	conn := connectOwner(t)
	sets := []db.MigrationSet{
		{Name: "broken", FS: fstest.MapFS{
			"00001_ok.sql":  migration("CREATE TABLE zz_mig_broken (id int);"),
			"00002_bad.sql": migration("SELECT * FROM zz_mig_does_not_exist;"),
		}, Table: "goose_test_broken_version"},
		{Name: "after", FS: fstest.MapFS{"00001_a.sql": migration("CREATE TABLE zz_mig_after (id int);")}, Table: "goose_test_after_version"},
	}
	results, err := db.Migrate(t.Context(), testDB.OwnerURL, sets)
	if err == nil {
		t.Fatal("migrate succeeded with a broken migration")
	}
	if !strings.Contains(err.Error(), "apply broken migrations") || !strings.Contains(err.Error(), "zz_mig_does_not_exist") {
		t.Errorf("error does not name the set and the cause: %v", err)
	}
	want := []db.MigrationResult{{Set: "broken", Applied: []int64{1}}}
	if !equalResults(results, want) {
		t.Fatalf("results = %+v, want %+v", results, want)
	}
	if tableExists(t, conn, "goose_test_after_version") || tableExists(t, conn, "zz_mig_after") {
		t.Fatal("the set after the failure ran")
	}
}

// Misconfigured sets are refused before connecting: the URL here is not even valid.
func TestMigrate_RejectsInvalidSets(t *testing.T) {
	t.Parallel()

	empty := fstest.MapFS{}
	for _, tc := range []struct {
		name string
		sets []db.MigrationSet
		want string
	}{
		{"no set", nil, "no migration set"},
		{"no name", []db.MigrationSet{{FS: empty, Table: "t"}}, "has no name"},
		{"no table", []db.MigrationSet{{Name: "a", FS: empty}}, "has no version table"},
		{"no file system", []db.MigrationSet{{Name: "a", Table: "t"}}, "has no file system"},
		{"repeated name", []db.MigrationSet{{Name: "a", FS: empty, Table: "t1"}, {Name: "a", FS: empty, Table: "t2"}}, "is repeated"},
		{"shared table", []db.MigrationSet{{Name: "a", FS: empty, Table: "t"}, {Name: "b", FS: empty, Table: "t"}}, "used by two sets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			results, err := db.Migrate(t.Context(), "not a url", tc.sets)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if results != nil {
				t.Fatalf("results = %+v, want none", results)
			}
		})
	}
}

func equalResults(got, want []db.MigrationResult) bool {
	return slices.EqualFunc(got, want, func(a, b db.MigrationResult) bool {
		return a.Set == b.Set && slices.Equal(a.Applied, b.Applied)
	})
}
