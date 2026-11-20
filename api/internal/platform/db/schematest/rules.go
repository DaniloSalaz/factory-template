package schematest

// The rules of the design for tables with tenant_id (section 7.3), outside the _test
// files so that other packages' tests can hold their own tables to them: the example
// tables of platform/db live only in that package's test database.

import (
	"context"
	"fmt"
	"regexp"
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

const tenantSharedComment = "scope: tenant-shared"

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

	var own []policyInfo
	var isolation *policyInfo
	for _, p := range policies {
		if p.Table != t.Name {
			continue
		}
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
	return out
}

func schemaViolations(ctx context.Context, q querier) (map[string][]string, []tableInfo, error) {
	tables, err := loadTenantTables(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	policies, err := loadPolicies(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	rules := newExprRules()
	out := map[string][]string{}
	for _, t := range tables {
		if v := tableViolations(t, policies, rules); len(v) > 0 {
			out[t.Name] = v
		}
	}
	return out, tables, nil
}

// TenantTableViolations applies the rules of section 7.3 to every table with tenant_id
// (and tenants) that q can see, keyed by table. Each message names the rule and the fix.
func TenantTableViolations(ctx context.Context, q querier) (map[string][]string, error) {
	violations, _, err := schemaViolations(ctx, q)
	return violations, err
}
