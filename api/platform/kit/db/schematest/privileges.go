package schematest

// The snapshot of app_runtime's privileges, outside the _test files so that package
// main compares it with RuntimePrivilegesGolden on a database with the platform's and
// the app's migrations.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RuntimePrivilegesGolden lists every privilege app_runtime holds, the platform's and the
// app's; its path is relative to api/, where package main compares it with a database
// migrated with both sets. Changing it is a security decision that the PR justifies
// (AGENTS.md); there is deliberately no flag to rewrite it, so every change is a hand
// edit visible in the diff.
const RuntimePrivilegesGolden = "platform/kit/db/schematest/testdata/runtime_privileges.golden"

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

// RuntimePrivileges returns every privilege app_runtime can use, one per line, in the
// format of RuntimePrivilegesGolden.
func RuntimePrivileges(ctx context.Context, q querier) ([]string, error) {
	return loadRuntimePrivileges(ctx, q)
}

// GoldenLines returns the non-empty lines of the golden file, without # comments.
func GoldenLines(raw []byte) []string {
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

// PrivilegeDiff compares two sets of privilege lines. Order does not matter: each line
// is one privilege, so a set difference is the whole story.
func PrivilegeDiff(want, got []string) string {
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
