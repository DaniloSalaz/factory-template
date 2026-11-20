package schematest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// querier is satisfied by *pgx.Conn and pgx.Tx, so the checks run inside the self-test
// transaction too.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ----------------------------------------------------------------------------------
// Tables with tenant_id (design, section 7.3)
// ----------------------------------------------------------------------------------

type tableInfo struct {
	Name    string
	RLS     bool
	Force   bool
	Comment string
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

var (
	// pg_policies shows expressions deparsed, e.g. current_setting('app.x'::text, true).
	missingOKSetting = regexp.MustCompile(`current_setting\([^)]*,\s*true\s*\)`)
	nullIf           = regexp.MustCompile(`(?i)\bnullif\s*\(`)
)

// loadTenantTables discovers every table of the public schema that has a tenant_id
// column; nothing is listed by hand.
func loadTenantTables(ctx context.Context, q querier) ([]tableInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       coalesce(obj_description(c.oid, 'pg_class'), '')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind IN ('r', 'p')
		  AND EXISTS (SELECT 1 FROM pg_attribute a
		              WHERE a.attrelid = c.oid AND a.attname = 'tenant_id'
		                AND a.attnum > 0 AND NOT a.attisdropped)
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
func tableViolations(t tableInfo, policies []policyInfo) []string {
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
		if missingOKSetting.MatchString(p.Qual) || missingOKSetting.MatchString(p.WithCheck) {
			add("policy %q uses current_setting(..., true), which hides a missing variable instead of failing: use current_setting('...')", p.Name)
		}
		if nullIf.MatchString(p.Qual) || nullIf.MatchString(p.WithCheck) {
			add("policy %q uses NULLIF, which turns a missing variable into NULL instead of an error: remove it", p.Name)
		}
	}

	if isolation == nil {
		add("missing policy tenant_isolation: CREATE POLICY tenant_isolation ON %s FOR ALL USING (tenant_id = current_setting('app.tenant_id')::uuid) WITH CHECK (same)", t.Name)
	} else {
		if isolation.Permissive != "PERMISSIVE" {
			add("tenant_isolation must be PERMISSIVE")
		}
		if isolation.Cmd != "ALL" {
			add("tenant_isolation must be FOR ALL, not FOR %s", isolation.Cmd)
		}
		if !strings.Contains(isolation.Qual, "app.tenant_id") || !strings.Contains(isolation.WithCheck, "app.tenant_id") {
			add("tenant_isolation must compare tenant_id with current_setting('app.tenant_id') in both USING and WITH CHECK")
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
	out := map[string][]string{}
	for _, t := range tables {
		if v := tableViolations(t, policies); len(v) > 0 {
			out[t.Name] = v
		}
	}
	return out, tables, nil
}

func TestSchema_TenantTables(t *testing.T) {
	t.Parallel()

	conn := connect(t, testDB.OwnerURL)
	violations, tables, err := schemaViolations(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("no table with tenant_id was discovered; the discovery query is broken")
	}
	names := make([]string, 0, len(tables))
	for _, tbl := range tables {
		names = append(names, tbl.Name)
	}
	t.Logf("tables with tenant_id: %s", strings.Join(names, ", "))

	for _, vs := range violations {
		for _, v := range vs {
			t.Error(v)
		}
	}
}

// The checks above are only worth something if they fail on misbuilt tables. These are
// created inside a transaction that is always rolled back.
func TestSchema_DetectsMisbuiltTables(t *testing.T) {
	t.Parallel()

	const isolation = `CREATE POLICY tenant_isolation ON %[1]s FOR ALL
		USING (tenant_id = current_setting('app.tenant_id')::uuid)
		WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid)`
	const ownScope = `CREATE POLICY own_scope ON %[1]s AS RESTRICTIVE FOR ALL
		USING (current_setting('app.scope') = 'tenant')`
	const forced = `ALTER TABLE %[1]s ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s FORCE ROW LEVEL SECURITY`

	tests := []struct {
		table string
		ddl   []string
		want  string // substring of the expected violation; empty means none
	}{
		{"zz_good_own", []string{forced, isolation, ownScope}, ""},
		{"zz_good_shared", []string{forced, isolation, `COMMENT ON TABLE %[1]s IS 'scope: tenant-shared'`}, ""},
		{"zz_no_rls", []string{isolation, ownScope}, "row level security is not enabled"},
		{"zz_no_force", []string{`ALTER TABLE %[1]s ENABLE ROW LEVEL SECURITY`, isolation, ownScope}, "not forced"},
		{"zz_no_isolation", []string{forced, ownScope}, "missing policy tenant_isolation"},
		{"zz_isolation_select", []string{forced, `CREATE POLICY tenant_isolation ON %[1]s FOR SELECT
			USING (tenant_id = current_setting('app.tenant_id')::uuid)`, ownScope}, "tenant_isolation must be FOR ALL"},
		{"zz_own_permissive", []string{forced, isolation, `CREATE POLICY own_scope ON %[1]s AS PERMISSIVE FOR ALL
			USING (current_setting('app.scope') = 'tenant')`}, "own_scope must be AS RESTRICTIVE"},
		{"zz_own_select", []string{forced, isolation, `CREATE POLICY own_scope ON %[1]s AS RESTRICTIVE FOR SELECT
			USING (current_setting('app.scope') = 'tenant')`}, "own_scope must be FOR ALL"},
		{"zz_no_scope", []string{forced, isolation}, "no declared scope"},
		{"zz_extra_permissive", []string{forced, isolation, ownScope, `CREATE POLICY open_reads ON %[1]s FOR SELECT USING (true)`}, `extra permissive policy "open_reads"`},
		{"zz_missing_ok", []string{forced, `CREATE POLICY tenant_isolation ON %[1]s FOR ALL
			USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid)`, ownScope}, "current_setting(..., true)"},
		{"zz_nullif", []string{forced, `CREATE POLICY tenant_isolation ON %[1]s FOR ALL
			USING (tenant_id = NULLIF(current_setting('app.tenant_id'), '')::uuid)
			WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id'), '')::uuid)`, ownScope}, "uses NULLIF"},
	}

	ctx := t.Context()
	conn := connect(t, testDB.OwnerURL)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for _, tc := range tests {
		stmts := append([]string{`CREATE TABLE %[1]s (id uuid PRIMARY KEY, tenant_id uuid NOT NULL)`}, tc.ddl...)
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, fmt.Sprintf(stmt, tc.table)); err != nil {
				t.Fatalf("%s: %v", tc.table, err)
			}
		}
	}

	violations, _, err := schemaViolations(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			got := violations[tc.table]
			if tc.want == "" {
				if len(got) > 0 {
					t.Fatalf("well-built table reported: %v", got)
				}
				return
			}
			if !slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, tc.want) }) {
				t.Fatalf("want a violation containing %q, got %v", tc.want, got)
			}
		})
	}
}

// ----------------------------------------------------------------------------------
// SECURITY DEFINER functions (design, section 7.4)
// ----------------------------------------------------------------------------------

type functionInfo struct {
	Name           string
	Owner          string
	Config         []string
	ArgNames       []string
	PublicExecute  bool
	ExecuteGranted []string // roles other than the owner and PUBLIC with EXECUTE
}

const pinnedSearchPath = "search_path=pg_catalog, extensions, pg_temp"

var userIDParam = regexp.MustCompile(`(^|_)user_id$`)

func loadSecurityDefiners(ctx context.Context, q querier) ([]functionInfo, error) {
	// A NULL proacl means default privileges, which include EXECUTE for PUBLIC.
	rows, err := q.Query(ctx, `
		SELECT p.proname,
		       pg_get_userbyid(p.proowner),
		       coalesce(p.proconfig, '{}'),
		       coalesce(p.proargnames, '{}'),
		       p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a
		                                   WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE'),
		       ARRAY(SELECT pg_get_userbyid(a.grantee)
		             FROM aclexplode(coalesce(p.proacl, '{}')) a
		             WHERE a.privilege_type = 'EXECUTE' AND a.grantee NOT IN (0, p.proowner)
		             ORDER BY 1)
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE p.prosecdef AND n.nspname = 'public'
		ORDER BY p.proname`)
	if err != nil {
		return nil, fmt.Errorf("query security definer functions: %w", err)
	}
	fns, err := pgx.CollectRows(rows, pgx.RowToStructByPos[functionInfo])
	if err != nil {
		return nil, fmt.Errorf("collect security definer functions: %w", err)
	}
	return fns, nil
}

func functionViolations(f functionInfo) []string {
	var out []string
	add := func(format string, args ...any) {
		out = append(out, fmt.Sprintf("[security-definer] function %s: ", f.Name)+fmt.Sprintf(format, args...))
	}
	if f.Owner != "app_security" {
		add("owned by %s: ALTER FUNCTION ... OWNER TO app_security (after granting EXECUTE)", f.Owner)
	}
	if !slices.Contains(f.Config, pinnedSearchPath) {
		add("search_path is not pinned: add SET %s", strings.Replace(pinnedSearchPath, "=", " = ", 1))
	}
	if f.PublicExecute {
		add("EXECUTE is granted to PUBLIC: REVOKE EXECUTE ON FUNCTION ... FROM PUBLIC")
	}
	if !slices.Equal(f.ExecuteGranted, []string{"app_runtime"}) {
		add("EXECUTE must be granted to app_runtime only, got %v", f.ExecuteGranted)
	}
	for _, arg := range f.ArgNames {
		if userIDParam.MatchString(arg) {
			add("parameter %q: the acting user comes from current_setting('app.user_id'), never from a parameter", arg)
		}
	}
	return out
}

func TestSchema_SecurityDefinerFunctions(t *testing.T) {
	t.Parallel()

	conn := connect(t, testDB.OwnerURL)
	fns, err := loadSecurityDefiners(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(fns) == 0 {
		t.Fatal("no SECURITY DEFINER function was discovered; the discovery query is broken")
	}
	names := make([]string, 0, len(fns))
	for _, f := range fns {
		names = append(names, f.Name)
		for _, v := range functionViolations(f) {
			t.Error(v)
		}
	}
	t.Logf("SECURITY DEFINER functions: %s", strings.Join(names, ", "))
}

func TestSchema_DetectsMisbuiltFunction(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	conn := connect(t, testDB.OwnerURL)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Everything wrong at once: owned by app_owner, no search_path, callable by PUBLIC
	// and taking the acting user as a parameter.
	for _, stmt := range []string{
		`CREATE FUNCTION zz_bad_definer(p_user_id uuid) RETURNS int
		 LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$`,
		`GRANT EXECUTE ON FUNCTION zz_bad_definer(uuid) TO PUBLIC`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("create misbuilt function: %v", err)
		}
	}

	fns, err := loadSecurityDefiners(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(fns, func(f functionInfo) bool { return f.Name == "zz_bad_definer" })
	if idx < 0 {
		t.Fatal("misbuilt function not discovered")
	}
	got := functionViolations(fns[idx])
	for _, want := range []string{"owned by app_owner", "search_path is not pinned", "granted to PUBLIC", "app_runtime only", `parameter "p_user_id"`} {
		t.Run(want, func(t *testing.T) {
			if !slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, want) }) {
				t.Fatalf("want a violation containing %q, got %v", want, got)
			}
		})
	}
}

// ----------------------------------------------------------------------------------
// Privileges of app_runtime
// ----------------------------------------------------------------------------------

func TestSchema_RuntimeCannotWriteAuditLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sql  string
	}{
		{"insert", `INSERT INTO audit_log (action) VALUES ('forged')`},
		{"update", `UPDATE audit_log SET action = 'rewritten'`},
		{"delete", `DELETE FROM audit_log`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// With the RLS variables set the policies pass, so only the missing table
			// privilege can stop the statement. Without them, the planner evaluates the
			// policy's current_setting first and fails with 42704 instead.
			ctx := t.Context()
			tx, err := connect(t, testDB.RuntimeURL).Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true),
				set_config('app.user_id', $1, true), set_config('app.scope', 'tenant', true)`,
				"00000000-0000-7000-8000-000000000001"); err != nil {
				t.Fatalf("set variables: %v", err)
			}

			_, err = tx.Exec(ctx, tc.sql)
			if code := sqlState(err); code != "42501" {
				t.Fatalf("%s as app_runtime: got %v (SQLSTATE %q), want insufficient_privilege 42501", tc.name, err, code)
			}
		})
	}
}

func TestSchema_RuntimeRoleHasNoPower(t *testing.T) {
	t.Parallel()

	conn := connect(t, testDB.SuperURL)
	tests := []struct {
		name string
		sql  string
	}{
		{"is superuser or bypasses RLS", `SELECT count(*) FROM pg_roles WHERE rolname = 'app_runtime' AND (rolsuper OR rolbypassrls)`},
		{"owns a relation", `SELECT count(*) FROM pg_class WHERE relowner = 'app_runtime'::regrole`},
		{"owns a function", `SELECT count(*) FROM pg_proc WHERE proowner = 'app_runtime'::regrole`},
		{"is a member of another role", `SELECT count(*) FROM pg_auth_members WHERE member = 'app_runtime'::regrole`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var n int
			if err := conn.QueryRow(t.Context(), tc.sql).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("app_runtime %s (%d rows)", tc.name, n)
			}
		})
	}
}

// The bootstrap runs once per environment and again whenever it changes, so it must be
// safe to repeat on a live database.
func TestSchema_BootstrapIsIdempotent(t *testing.T) {
	code, out, err := testDB.Container.Exec(t.Context(), []string{"/docker-entrypoint-initdb.d/initdb.sh"})
	if err != nil {
		t.Fatalf("run bootstrap again: %v", err)
	}
	if code != 0 {
		b, _ := io.ReadAll(out)
		t.Fatalf("second bootstrap exited %d:\n%s", code, b)
	}
}

func sqlState(err error) string {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState()
	}
	return ""
}
