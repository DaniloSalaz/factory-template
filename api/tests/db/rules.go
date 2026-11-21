package db

// The rules of the design for tables with tenant_id (section 7.3), outside the _test
// files so that other packages' tests can hold their own tables to them: the example
// tables of platform/kit/db live only in that package's test database.

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// querier is satisfied by *pgx.Conn and pgx.Tx, so the checks run inside the self-test
// transaction too.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ----------------------------------------------------------------------------------
// Tables with tenant_id, and tenants itself (design, section 7.3)
// ----------------------------------------------------------------------------------

type tableInfo struct {
	Name    string
	RLS     bool
	Force   bool
	Comment string
	// Column that tenant_isolation compares with app.tenant_id: tenant_id, or id in
	// tenants, whose rows are the tenants themselves.
	IsolationColumn string
}

type policyInfo struct {
	Table      string
	Name       string
	Permissive string // PERMISSIVE or RESTRICTIVE
	Cmd        string // ALL, SELECT, INSERT, UPDATE, DELETE
	Qual       string
	WithCheck  string
}

const (
	tenantSharedComment = "scope: tenant-shared"
	// An access table (case_access) grants access to other rows. Scope own may read its
	// own access rows but never write them, or it could grant itself any row.
	accessComment = "scope: access"
)

// tenantScopeOnly is "current_setting('app.scope') = 'tenant'" as pg_policies deparses
// it, without spaces.
const tenantScopeOnly = "(current_setting('app.scope'::text)='tenant'::text)"

// exprRules are the expressions a policy may not contain. pg_policies shows them
// deparsed, e.g. current_setting('app.x'::text, true).
type exprRules struct {
	missingOKSetting *regexp.Regexp
	nullIf           *regexp.Regexp
}

func newExprRules() exprRules {
	return exprRules{
		missingOKSetting: regexp.MustCompile(`current_setting\([^)]*,\s*true\s*\)`),
		nullIf:           regexp.MustCompile(`(?i)\bnullif\s*\(`),
	}
}

// loadTenantTables discovers every table of the public schema that has a tenant_id
// column, plus tenants; nothing else is listed by hand.
func loadTenantTables(ctx context.Context, q querier) ([]tableInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       coalesce(obj_description(c.oid, 'pg_class'), ''),
		       CASE WHEN c.relname = 'tenants' THEN 'id' ELSE 'tenant_id' END
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind IN ('r', 'p')
		  AND (c.relname = 'tenants'
		       OR EXISTS (SELECT 1 FROM pg_attribute a
		                  WHERE a.attrelid = c.oid AND a.attname = 'tenant_id'
		                    AND a.attnum > 0 AND NOT a.attisdropped))
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("query tenant tables: %w", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowToStructByPos[tableInfo])
	if err != nil {
		return nil, fmt.Errorf("collect tenant tables: %w", err)
	}
	return tables, nil
}

func loadPolicies(ctx context.Context, q querier) ([]policyInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT tablename, policyname, permissive, cmd,
		       coalesce(qual, ''), coalesce(with_check, '')
		FROM pg_policies
		WHERE schemaname = 'public'
		ORDER BY tablename, policyname`)
	if err != nil {
		return nil, fmt.Errorf("query policies: %w", err)
	}
	policies, err := pgx.CollectRows(rows, pgx.RowToStructByPos[policyInfo])
	if err != nil {
		return nil, fmt.Errorf("collect policies: %w", err)
	}
	return policies, nil
}

// tableViolations applies the rules of section 7.3 to one table. Messages say what is
// wrong and how to fix it.
func tableViolations(t tableInfo, policies []policyInfo, rules exprRules) []string {
	var out []string
	add := func(format string, args ...any) {
		out = append(out, fmt.Sprintf("[rls] table %s: ", t.Name)+fmt.Sprintf(format, args...))
	}

	if !t.RLS {
		add("row level security is not enabled: ALTER TABLE %s ENABLE ROW LEVEL SECURITY", t.Name)
	}
	if !t.Force {
		add("row level security is not forced, so the table owner bypasses it: ALTER TABLE %s FORCE ROW LEVEL SECURITY", t.Name)
	}

	var own, mine []policyInfo
	var isolation *policyInfo
	for _, p := range policies {
		if p.Table != t.Name {
			continue
		}
		mine = append(mine, p)
		switch p.Name {
		case "tenant_isolation":
			isolation = &p
		case "own_scope":
			own = append(own, p)
		}
		if p.Permissive == "PERMISSIVE" && p.Name != "tenant_isolation" {
			add("extra permissive policy %q is ORed with tenant_isolation and can open other tenants' rows: make it AS RESTRICTIVE or remove it", p.Name)
		}
		if rules.missingOKSetting.MatchString(p.Qual) || rules.missingOKSetting.MatchString(p.WithCheck) {
			add("policy %q uses current_setting(..., true), which hides a missing variable instead of failing: use current_setting('...')", p.Name)
		}
		if rules.nullIf.MatchString(p.Qual) || rules.nullIf.MatchString(p.WithCheck) {
			add("policy %q uses NULLIF, which turns a missing variable into NULL instead of an error: remove it", p.Name)
		}
	}

	if isolation == nil {
		add("missing policy tenant_isolation: CREATE POLICY tenant_isolation ON %s FOR ALL USING (%s = current_setting('app.tenant_id')::uuid) WITH CHECK (same)", t.Name, t.IsolationColumn)
	} else {
		if isolation.Permissive != "PERMISSIVE" {
			add("tenant_isolation must be PERMISSIVE")
		}
		if isolation.Cmd != "ALL" {
			add("tenant_isolation must be FOR ALL, not FOR %s", isolation.Cmd)
		}
		// Deparsed as "(<column> = (current_setting('app.tenant_id'::text))::uuid)".
		compares := func(expr string) bool {
			return strings.Contains(expr, "app.tenant_id") && strings.Contains(expr, "("+t.IsolationColumn+" = ")
		}
		if !compares(isolation.Qual) || !compares(isolation.WithCheck) {
			add("tenant_isolation must compare %s with current_setting('app.tenant_id') in both USING and WITH CHECK", t.IsolationColumn)
		}
	}

	shared := t.Comment == tenantSharedComment
	switch {
	case shared && len(own) > 0:
		add("declares both %q and an own_scope policy: keep only one", tenantSharedComment)
	case len(own) > 0:
		for _, p := range own {
			if p.Permissive != "RESTRICTIVE" {
				add("own_scope must be AS RESTRICTIVE; a permissive one is ORed with tenant_isolation and opens every tenant's rows")
			}
			if p.Cmd != "ALL" {
				add("own_scope must be FOR ALL, not FOR %s, or writes escape the scope", p.Cmd)
			}
		}
	case !shared:
		add("no declared scope: add COMMENT ON TABLE %s IS '%s' or a RESTRICTIVE FOR ALL own_scope policy (design, section 7.3)", t.Name, tenantSharedComment)
	}

	if t.Comment == accessComment {
		if len(own) == 0 {
			add("access table (%q) without own_scope: add a RESTRICTIVE FOR ALL own_scope policy on its user column", accessComment)
		}
		for _, cmd := range []string{"INSERT", "UPDATE", "DELETE"} {
			if !slices.ContainsFunc(mine, func(p policyInfo) bool { return writeForTenantScopeOnly(p, cmd) }) {
				clause := "USING"
				if cmd == "INSERT" {
					clause = "WITH CHECK"
				}
				add("access table (%q) lets scope own %s its access rows and grant itself access: "+
					"CREATE POLICY own_write_%s ON %s AS RESTRICTIVE FOR %s %s (current_setting('app.scope') = 'tenant')",
					accessComment, cmd, strings.ToLower(cmd), t.Name, cmd, clause)
			}
		}
	}
	return out
}

// writeForTenantScopeOnly reports whether p is a restrictive policy for cmd that allows
// it only with scope tenant. An INSERT is decided by WITH CHECK, a DELETE by USING, and
// an UPDATE is stopped by either. FOR ALL policies do not count: on an access table the
// only one is own_scope, which must let scope own read its rows.
func writeForTenantScopeOnly(p policyInfo, cmd string) bool {
	if p.Permissive != "RESTRICTIVE" || p.Cmd != cmd {
		return false
	}
	using := strings.ReplaceAll(p.Qual, " ", "") == tenantScopeOnly
	check := strings.ReplaceAll(p.WithCheck, " ", "") == tenantScopeOnly
	switch cmd {
	case "INSERT":
		return check
	case "DELETE":
		return using
	default:
		return using || check
	}
}

// foreignKeyInfo is a foreign key between two tables that both have tenant_id. Keys
// towards tables without it (tenants, users) are not loaded: they cannot cross tenants.
type foreignKeyInfo struct {
	Name       string
	Table      string
	Columns    []string
	RefTable   string
	RefColumns []string
}

// loadTenantForeignKeys discovers, from pg_constraint, every foreign key from a table
// with tenant_id to another table with tenant_id. Columns keep the key's order, so
// Columns[i] references RefColumns[i].
func loadTenantForeignKeys(ctx context.Context, q querier) ([]foreignKeyInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT c.conname, src.relname,
		       ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum, i)
		             JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		             ORDER BY k.i),
		       dst.relname,
		       ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(attnum, i)
		             JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.attnum
		             ORDER BY k.i)
		FROM pg_constraint c
		JOIN pg_class src ON src.oid = c.conrelid
		JOIN pg_namespace sn ON sn.oid = src.relnamespace
		JOIN pg_class dst ON dst.oid = c.confrelid
		JOIN pg_namespace dn ON dn.oid = dst.relnamespace
		WHERE c.contype = 'f' AND sn.nspname = 'public' AND dn.nspname = 'public'
		  AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = src.oid
		              AND a.attname = 'tenant_id' AND a.attnum > 0 AND NOT a.attisdropped)
		  AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = dst.oid
		              AND a.attname = 'tenant_id' AND a.attnum > 0 AND NOT a.attisdropped)
		ORDER BY src.relname, c.conname`)
	if err != nil {
		return nil, fmt.Errorf("query foreign keys: %w", err)
	}
	fks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[foreignKeyInfo])
	if err != nil {
		return nil, fmt.Errorf("collect foreign keys: %w", err)
	}
	return fks, nil
}

// foreignKeyViolation reports a key that does not pair tenant_id with tenant_id. RLS
// filters what is read, but a key on the parent's id alone lets a row of one tenant
// point at another tenant's row (design, 7.3; docs/platform/patterns.md).
func foreignKeyViolation(fk foreignKeyInfo) string {
	i := slices.Index(fk.Columns, "tenant_id")
	if len(fk.Columns) > 1 && i >= 0 && i < len(fk.RefColumns) && fk.RefColumns[i] == "tenant_id" {
		return ""
	}
	return fmt.Sprintf("[rls] table %s: foreign key %s (%s) -> %s (%s) does not pair tenant_id with tenant_id, "+
		"so a row can point at another tenant's row: use FOREIGN KEY (tenant_id, <parent>_id) REFERENCES %s (tenant_id, id) "+
		"with UNIQUE (tenant_id, id) on %s (docs/platform/patterns.md)",
		fk.Table, fk.Name, strings.Join(fk.Columns, ", "), fk.RefTable, strings.Join(fk.RefColumns, ", "), fk.RefTable, fk.RefTable)
}

// TenantTableViolations applies the rules of section 7.3 to every table with tenant_id
// (and tenants) that q can see, keyed by table. Each message names the rule and the fix.
func TenantTableViolations(ctx context.Context, q querier) (map[string][]string, error) {
	tables, err := loadTenantTables(ctx, q)
	if err != nil {
		return nil, err
	}
	policies, err := loadPolicies(ctx, q)
	if err != nil {
		return nil, err
	}
	rules := newExprRules()
	out := map[string][]string{}
	for _, t := range tables {
		if v := tableViolations(t, policies, rules); len(v) > 0 {
			out[t.Name] = v
		}
	}
	fks, err := loadTenantForeignKeys(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, fk := range fks {
		if v := foreignKeyViolation(fk); v != "" {
			out[fk.Table] = append(out[fk.Table], v)
		}
	}
	return out, nil
}

// TenantTableNames lists the tables TenantTableViolations checks. A caller asserts it is
// not empty: a broken discovery query would pass every rule.
func TenantTableNames(ctx context.Context, q querier) ([]string, error) {
	tables, err := loadTenantTables(ctx, q)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		names = append(names, t.Name)
	}
	return names, nil
}
