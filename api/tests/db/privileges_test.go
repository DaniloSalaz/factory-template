//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
)

// The snapshot is only worth something if it sees what a migration could slip in. The
// objects are created by the superuser inside a transaction that is always rolled back;
// the superuser does not share app_owner's default privileges, so its function keeps
// a NULL ACL, which grants EXECUTE to PUBLIC.
func TestSchema_RuntimePrivilegesSnapshotDetectsChanges(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	tx, err := connect(t, testDB.SuperURL).Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	before, err := loadRuntimePrivileges(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE FUNCTION public.zz_default_acl() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`,
		`GRANT UPDATE (disabled_at) ON public.users TO app_runtime`,
		`GRANT TRUNCATE ON public.audit_log TO PUBLIC`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	after, err := loadRuntimePrivileges(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}

	got := PrivilegeDiff(before, after)
	for _, want := range []string{
		"+ PUBLIC function public.zz_default_acl() EXECUTE",
		"+ app_runtime column public.users.disabled_at UPDATE",
		"+ PUBLIC table public.audit_log TRUNCATE",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diff does not contain %q:\n%s", want, got)
		}
	}
}
