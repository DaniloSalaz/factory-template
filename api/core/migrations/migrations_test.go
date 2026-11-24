package migrations_test

import (
	"bytes"
	"io/fs"
	"slices"
	"testing"

	"chaskicode.dev/saas/core/migrations"
	"chaskicode.dev/saas/tests/testkit"
)

// The tests of core/kit migrate with testkit.CoreFromSource, which reads this
// directory from disk because kit cannot import this package. It must be the very set
// the binary embeds: same name, same version table, same files.
func TestSet_MatchesCoreFromSource(t *testing.T) {
	t.Parallel()

	embedded, source := migrations.Set(), testkit.CoreFromSource()
	if embedded.Name != source.Name || embedded.Table != source.Table {
		t.Fatalf("embedded set %s/%s, testkit set %s/%s", embedded.Name, embedded.Table, source.Name, source.Table)
	}
	if embedded.AllowEmpty || source.AllowEmpty {
		t.Fatal("the core's set must not allow being empty: finding no migration means a wrong path")
	}
	if embedded.Table != "goose_db_version" {
		t.Fatalf("the core's version table is %s: it must stay goose_db_version, the one existing databases have", embedded.Table)
	}

	want, err := fs.Glob(embedded.FS, "*.sql")
	if err != nil || len(want) == 0 {
		t.Fatalf("embedded migrations: %v %v", want, err)
	}
	got, err := fs.Glob(source.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(want, got) {
		t.Fatalf("embedded %v, on disk %v", want, got)
	}
	for _, name := range want {
		a, errA := fs.ReadFile(embedded.FS, name)
		b, errB := fs.ReadFile(source.FS, name)
		if errA != nil || errB != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs between the binary and the disk (%v, %v)", name, errA, errB)
		}
	}
}
