package schematest

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// runtimePrivilegesGolden lists every privilege app_runtime holds. Changing it is a
// security decision that the PR justifies (AGENTS.md); there is deliberately no flag to
// rewrite it, so every change is a hand edit visible in the diff.
const runtimePrivilegesGolden = "testdata/runtime_privileges.golden"

// runtimePrivilegesQuery reads every privilege app_runtime can use, granted to it or to
// PUBLIC, on the database, the schemas, the relations, their columns and the functions.
// A NULL ACL means the object's default privileges (acldefault), which for functions
// include EXECUTE for PUBLIC: reading only explicit ACLs would miss exactly that.
// System schemas and objects that belong to an extension are left out: they depend on
// the Postgres and extension versions, not on our migrations.
const runtimePrivilegesQuery = `
WITH grantees AS (
  SELECT 0::oid AS oid, 'PUBLIC' AS name
  UNION ALL SELECT oid, rolname FROM pg_roles WHERE rolname = 'app_runtime'
),
app_schemas AS (
  SELECT oid, nspname, nspowner, nspacl FROM pg_namespace
  WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
),
acl AS (
  SELECT 'database' AS kind, '' AS object, a.grantee, a.privilege_type, a.is_grantable
  FROM pg_database d, aclexplode(coalesce(d.datacl, acldefault('d', d.datdba))) a
  WHERE d.datname = current_database()
  UNION ALL
  SELECT 'schema', format('%I', n.nspname), a.grantee, a.privilege_type, a.is_grantable
  FROM app_schemas n, aclexplode(coalesce(n.nspacl, acldefault('n', n.nspowner))) a
  UNION ALL
  SELECT CASE c.relkind WHEN 'S' THEN 'sequence' ELSE 'table' END,
         format('%I.%I', n.nspname, c.relname), a.grantee, a.privilege_type, a.is_grantable
  FROM pg_class c
  JOIN app_schemas n ON n.oid = c.relnamespace,
  aclexplode(coalesce(c.relacl, acldefault((CASE c.relkind WHEN 'S' THEN 's' ELSE 'r' END)::"char", c.relowner))) a
  WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
    AND NOT EXISTS (SELECT 1 FROM pg_depend d
                    WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
  UNION ALL
  SELECT 'column', format('%I.%I.%I', n.nspname, c.relname, att.attname),
         a.grantee, a.privilege_type, a.is_grantable
  FROM pg_attribute att
  JOIN pg_class c ON c.oid = att.attrelid
  JOIN app_schemas n ON n.oid = c.relnamespace,
  aclexplode(att.attacl) a
  WHERE att.attnum > 0 AND NOT att.attisdropped AND att.attacl IS NOT NULL
  UNION ALL
  SELECT 'function', format('%I.%I(%s)', n.nspname, p.proname, pg_get_function_identity_arguments(p.oid)),
         a.grantee, a.privilege_type, a.is_grantable
  FROM pg_proc p
  JOIN app_schemas n ON n.oid = p.pronamespace,
  aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
  WHERE NOT EXISTS (SELECT 1 FROM pg_depend d
                    WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
)
SELECT concat_ws(' ', g.name, acl.kind, nullif(acl.object, ''), acl.privilege_type,
                 CASE WHEN acl.is_grantable THEN 'WITH GRANT OPTION' END)
FROM acl JOIN grantees g ON g.oid = acl.grantee
ORDER BY 1`

func loadRuntimePrivileges(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.Query(ctx, runtimePrivilegesQuery)
	if err != nil {
		return nil, fmt.Errorf("query privileges: %w", err)
	}
	privileges, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("collect privileges: %w", err)
	}
	return privileges, nil
}

func TestSchema_RuntimePrivilegesSnapshot(t *testing.T) {
	t.Parallel()

	got, err := loadRuntimePrivileges(t.Context(), connect(t, testDB.SuperURL))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(runtimePrivilegesGolden)
	if err != nil {
		t.Fatalf("read %s: %v", runtimePrivilegesGolden, err)
	}
	want := goldenLines(raw)

	if diff := privilegeDiff(want, got); diff != "" {
		t.Fatalf("[runtime-privileges] the privileges of app_runtime differ from %s\n"+
			"(+ granted but not listed, - listed but not granted):\n\n%s\n"+
			"Either the migration is wrong, or the change is intended: then update the file by hand "+
			"and justify it in the PR, it is a security decision (AGENTS.md). Current privileges:\n\n%s\n",
			runtimePrivilegesGolden, diff, strings.Join(got, "\n"))
	}
}

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

	got := privilegeDiff(before, after)
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

// goldenLines returns the non-empty lines of the golden file, without # comments.
func goldenLines(raw []byte) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines
}

// privilegeDiff compares two sets of privilege lines. Order does not matter: each line
// is one privilege, so a set difference is the whole story.
func privilegeDiff(want, got []string) string {
	var out []string
	for _, line := range got {
		if !slices.Contains(want, line) {
			out = append(out, "+ "+line)
		}
	}
	for _, line := range want {
		if !slices.Contains(got, line) {
			out = append(out, "- "+line)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(a[2:], b[2:]) })
	if len(out) == 0 {
		return ""
	}
	return fmt.Sprintln(strings.Join(out, "\n"))
}
