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

// ----------------------------------------------------------------------------------
// Tables with tenant_id, and tenants itself (design, section 7.3; rules in rules.go)
// ----------------------------------------------------------------------------------

func TestSchema_TenantTables(t *testing.T) {
	t.Parallel()

	conn := connect(t, testDB.OwnerURL)
	violations, tables, err := schemaViolations(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("no tenant table was discovered; the discovery query is broken")
	}
	names := make([]string, 0, len(tables))
	for _, tbl := range tables {
		names = append(names, tbl.Name)
	}
	if !slices.Contains(names, "tenants") {
		t.Error("tenants was not discovered; the discovery query is broken")
	}
	t.Logf("tenant tables: %s", strings.Join(names, ", "))

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
	const shared = `COMMENT ON TABLE %[1]s IS 'scope: tenant-shared'`
	// Like memberships' own_write: an extra restrictive policy only narrows access.
	const ownWrite = `CREATE POLICY own_write ON %[1]s AS RESTRICTIVE FOR UPDATE
		USING (current_setting('app.scope') = 'tenant')`
	// An access table: scope own reads its rows but only scope tenant writes them. The
	// table has no user column; id stands in for it.
	const access = `COMMENT ON TABLE %[1]s IS 'scope: access'`
	const accessScope = `CREATE POLICY own_scope ON %[1]s AS RESTRICTIVE FOR ALL
		USING (current_setting('app.scope') = 'tenant' OR id = current_setting('app.user_id')::uuid)`
	const writeInsert = `CREATE POLICY own_write_insert ON %[1]s AS RESTRICTIVE FOR INSERT
		WITH CHECK (current_setting('app.scope') = 'tenant')`
	const writeUpdate = `CREATE POLICY own_write_update ON %[1]s AS RESTRICTIVE FOR UPDATE
		USING (current_setting('app.scope') = 'tenant')`
	const writeDelete = `CREATE POLICY own_write_delete ON %[1]s AS RESTRICTIVE FOR DELETE
		USING (current_setting('app.scope') = 'tenant')`

	tests := []struct {
		table string
		ddl   []string
		want  string // substring of the expected violation; empty means none
	}{
		{"zz_good_own", []string{forced, isolation, ownScope}, ""},
		{"zz_good_shared", []string{forced, isolation, `COMMENT ON TABLE %[1]s IS 'scope: tenant-shared'`}, ""},
		// Foreign keys between tables with tenant_id pair tenant_id with tenant_id. The
		// parent comes first: the children reference it. NOT VALID skips the check of
		// existing rows, which would query the parent under its forced RLS without
		// variables; the key is in pg_constraint all the same.
		{"zz_fk_parent", []string{forced, isolation, shared, `ALTER TABLE %[1]s ADD UNIQUE (tenant_id, id)`}, ""},
		{"zz_fk_composite", []string{forced, isolation, shared, `ALTER TABLE %[1]s ADD COLUMN parent_id uuid,
			ADD FOREIGN KEY (tenant_id, parent_id) REFERENCES zz_fk_parent (tenant_id, id) NOT VALID`}, ""},
		{"zz_fk_simple", []string{forced, isolation, shared, `ALTER TABLE %[1]s ADD COLUMN parent_id uuid,
			ADD FOREIGN KEY (parent_id) REFERENCES zz_fk_parent (id) NOT VALID`}, "does not pair tenant_id with tenant_id"},
		{"zz_fk_swapped", []string{forced, isolation, shared, `ALTER TABLE %[1]s ADD COLUMN parent_id uuid,
			ADD FOREIGN KEY (tenant_id, parent_id) REFERENCES zz_fk_parent (id, tenant_id) NOT VALID`}, "does not pair tenant_id with tenant_id"},
		{"zz_fk_to_users", []string{forced, isolation, shared, `ALTER TABLE %[1]s ADD COLUMN user_id uuid,
			ADD FOREIGN KEY (user_id) REFERENCES users (id) NOT VALID`}, ""},
		{"zz_fk_to_tenants", []string{forced, isolation, shared, `ALTER TABLE %[1]s ADD FOREIGN KEY (tenant_id) REFERENCES tenants (id) NOT VALID`}, ""},
		{"zz_no_rls", []string{isolation, ownScope}, "row level security is not enabled"},
		{"zz_no_force", []string{`ALTER TABLE %[1]s ENABLE ROW LEVEL SECURITY`, isolation, ownScope}, "not forced"},
		{"zz_no_isolation", []string{forced, ownScope}, "missing policy tenant_isolation"},
		{"zz_isolation_select", []string{forced, `CREATE POLICY tenant_isolation ON %[1]s FOR SELECT
			USING (tenant_id = current_setting('app.tenant_id')::uuid)`, ownScope}, "tenant_isolation must be FOR ALL"},
		{"zz_own_permissive", []string{forced, isolation, `CREATE POLICY own_scope ON %[1]s AS PERMISSIVE FOR ALL
			USING (current_setting('app.scope') = 'tenant')`}, "own_scope must be AS RESTRICTIVE"},
		{"zz_own_select", []string{forced, isolation, `CREATE POLICY own_scope ON %[1]s AS RESTRICTIVE FOR SELECT
			USING (current_setting('app.scope') = 'tenant')`}, "own_scope must be FOR ALL"},
		{"zz_good_own_write", []string{forced, isolation, ownScope, ownWrite}, ""},
		// An extra restrictive policy is not a declared scope, and is held to the same
		// expression rules as any other.
		{"zz_only_own_write", []string{forced, isolation, ownWrite}, "no declared scope"},
		{"zz_own_write_missing_ok", []string{forced, isolation, ownScope, `CREATE POLICY own_write ON %[1]s AS RESTRICTIVE FOR UPDATE
			USING (current_setting('app.scope', true) = 'tenant')`}, `policy "own_write" uses current_setting(..., true)`},
		{"zz_own_write_nullif", []string{forced, isolation, ownScope, `CREATE POLICY own_write ON %[1]s AS RESTRICTIVE FOR UPDATE
			USING (NULLIF(current_setting('app.scope'), '') = 'tenant')`}, `policy "own_write" uses NULLIF`},
		{"zz_no_scope", []string{forced, isolation}, "no declared scope"},
		{"zz_good_access", []string{forced, isolation, accessScope, access, writeInsert, writeUpdate, writeDelete}, ""},
		{"zz_access_no_insert", []string{forced, isolation, accessScope, access, writeUpdate, writeDelete}, "lets scope own INSERT"},
		// Without it a client moves its access row to another case.
		{"zz_access_no_update", []string{forced, isolation, accessScope, access, writeInsert, writeDelete}, "lets scope own UPDATE"},
		{"zz_access_no_delete", []string{forced, isolation, accessScope, access, writeInsert, writeUpdate}, "lets scope own DELETE"},
		{"zz_access_no_own_scope", []string{forced, isolation, access, writeInsert, writeUpdate, writeDelete}, `access table ("scope: access") without own_scope`},
		{"zz_access_weak_update", []string{forced, isolation, accessScope, access, writeInsert, writeDelete,
			`CREATE POLICY own_write_update ON %[1]s AS RESTRICTIVE FOR UPDATE USING (true)`}, "lets scope own UPDATE"},
		{"zz_access_insert_wrong_cmd", []string{forced, isolation, accessScope, access, writeUpdate, writeDelete,
			`CREATE POLICY own_write_insert ON %[1]s AS RESTRICTIVE FOR UPDATE WITH CHECK (current_setting('app.scope') = 'tenant')`}, "lets scope own INSERT"},
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

// tenants is checked by its id column. Its policy and RLS are broken inside a
// transaction that is always rolled back. Not parallel: the DDL locks tenants
// exclusively, and the parallel tests (which start only after the sequential ones)
// would queue behind it, the concurrency test mistaking that wait for the one it expects.
func TestSchema_DetectsMisbuiltTenants(t *testing.T) {

	tests := []struct {
		name string
		ddl  []string
		want string
	}{
		{"no isolation", []string{`DROP POLICY tenant_isolation ON tenants`}, "missing policy tenant_isolation: CREATE POLICY tenant_isolation ON tenants FOR ALL USING (id = "},
		{"isolation on another column", []string{`DROP POLICY tenant_isolation ON tenants`,
			`CREATE POLICY tenant_isolation ON tenants FOR ALL
			 USING (slug = current_setting('app.tenant_id')) WITH CHECK (slug = current_setting('app.tenant_id'))`},
			"tenant_isolation must compare id with current_setting('app.tenant_id')"},
		{"not forced", []string{`ALTER TABLE tenants NO FORCE ROW LEVEL SECURITY`}, "not forced"},
		{"no declared scope", []string{`COMMENT ON TABLE tenants IS NULL`}, "no declared scope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := connect(t, testDB.OwnerURL).Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			for _, stmt := range tc.ddl {
				if _, err := tx.Exec(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}

			violations, _, err := schemaViolations(ctx, tx)
			if err != nil {
				t.Fatal(err)
			}
			got := violations["tenants"]
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

// app_runtime writes only what the design needs (section 7.1): tenants and memberships
// are created only through create_tenant_with_owner and accept_invitation, users and
// identities are deleted only by app_admin, and UPDATE is granted per column. Every
// statement runs with the RLS variables of a tenant the user owns and targets rows
// that would otherwise accept it (a tenant with no children, a user that is not yet a
// member, rows of that tenant), so only the privilege decides. Writing a column onto
// itself is enough: the privilege is checked on the SET list, not on the new value.
func TestSchema_RuntimeWritePrivileges(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	owned, empty := f.tenant("active"), f.tenant("active")
	owner, outsider := f.user(true, false), f.user(true, false)
	membership := f.member(owned, owner, "owner")
	invitation := f.invitation(owned, "invitee@example.com", "")
	f.exec(`INSERT INTO user_identities (user_id, provider, subject, email_at_link)
		VALUES ($1, 'workos', $2, $3)`, outsider.id, "subject-"+outsider.id, outsider.email)
	session := f.scalar(`INSERT INTO sessions (token_hash, user_id, auth_time, idle_expires_at, absolute_expires_at)
		VALUES (sha256(uuidv7()::text::bytea), $1, now(), now() + interval '7 days', now() + interval '30 days')
		RETURNING id::text`, outsider.id)
	factor := f.scalar(`INSERT INTO mfa_factors (user_id, provider, provider_factor_id)
		VALUES ($1::uuid, 'workos', 'auth_factor_' || $1::text) RETURNING id::text`, outsider.id)
	f.exec(`INSERT INTO mfa_challenges (factor_id, session_id, provider_challenge_id, expires_at)
		VALUES ($1::uuid, $2::uuid, 'auth_challenge_' || $1::text, now() + interval '5 minutes')`, factor, session)

	const denied = "42501"
	tests := []struct {
		name      string
		sql       string
		args      []any
		wantState string // "" means it must succeed and affect one row
	}{
		{"insert tenant", `INSERT INTO tenants (slug, name) VALUES ('forged-' || replace(uuidv7()::text, '-', ''), 'Forged')`, nil, denied},
		{"delete tenant", `DELETE FROM tenants WHERE id = $1`, []any{empty}, denied},
		{"update tenant status", `UPDATE tenants SET status = 'suspended' WHERE id = $1`, []any{owned}, denied},
		{"update tenant name", `UPDATE tenants SET name = 'Renamed', updated_at = now() WHERE id = $1`, []any{owned}, ""},

		{"insert membership", `INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'member')`, []any{owned, outsider.id}, denied},
		{"update membership user", `UPDATE memberships SET user_id = $2 WHERE id = $1`, []any{membership, outsider.id}, denied},
		{"update membership tenant", `UPDATE memberships SET tenant_id = tenant_id WHERE id = $1`, []any{membership}, denied},
		{"update membership role", `UPDATE memberships SET role = 'admin', updated_at = now() WHERE id = $1`, []any{membership}, ""},

		{"update invitation accepted_at", `UPDATE invitations SET accepted_at = now() WHERE token_hash = $1`, []any{invitation}, denied},
		{"update invitation role", `UPDATE invitations SET role = 'owner' WHERE token_hash = $1`, []any{invitation}, denied},
		{"update invitation email", `UPDATE invitations SET email = 'other@example.com' WHERE token_hash = $1`, []any{invitation}, denied},
		{"update invitation inviter", `UPDATE invitations SET invited_by = $2 WHERE token_hash = $1`, []any{invitation, outsider.id}, denied},
		{"update invitation tenant", `UPDATE invitations SET tenant_id = tenant_id WHERE token_hash = $1`, []any{invitation}, denied},
		{"resend and revoke invitation", `UPDATE invitations SET token_hash = sha256(uuidv7()::text::bytea),
			expires_at = now() + interval '7 days', revoked_at = now(), updated_at = now() WHERE token_hash = $1`, []any{invitation}, ""},

		{"delete user", `DELETE FROM users WHERE id = $1`, []any{outsider.id}, denied},
		{"update user disabled_at", `UPDATE users SET disabled_at = NULL WHERE id = $1`, []any{outsider.id}, denied},
		// Issue #4: a new email always comes with the provider's verification of it.
		{"update user email and email_verified", `UPDATE users SET email = email, email_verified = email_verified WHERE id = $1`, []any{outsider.id}, ""},
		{"update user display_name", `UPDATE users SET display_name = 'x' WHERE id = $1`, []any{outsider.id}, denied},
		{"update user email and last login", `UPDATE users SET email = email, last_login_at = now(), updated_at = now() WHERE id = $1`, []any{outsider.id}, ""},

		{"delete user identity", `DELETE FROM user_identities WHERE user_id = $1`, []any{outsider.id}, denied},
		{"update user identity user", `UPDATE user_identities SET user_id = user_id WHERE user_id = $1`, []any{outsider.id}, denied},
		{"update user identity email", `UPDATE user_identities SET email_at_link = email_at_link WHERE user_id = $1`, []any{outsider.id}, denied},

		{"update session user", `UPDATE sessions SET user_id = user_id WHERE id = $1`, []any{session}, denied},
		{"update session auth_time", `UPDATE sessions SET auth_time = now() WHERE id = $1`, []any{session}, denied},
		{"update session absolute expiry", `UPDATE sessions SET absolute_expires_at = now() WHERE id = $1`, []any{session}, denied},
		{"update session provider session", `UPDATE sessions SET provider_session_id = 'x' WHERE id = $1`, []any{session}, denied},
		// Only mark_session_mfa gives a session MFA, and it does so in a new session with a
		// new token (design, 5.2): app_runtime never writes either column.
		{"update session mfa_auth_time", `UPDATE sessions SET mfa_auth_time = now() WHERE id = $1`, []any{session}, denied},
		{"update session token", `UPDATE sessions SET token_hash = sha256(uuidv7()::text::bytea) WHERE id = $1`, []any{session}, denied},

		// MFA tables: only their SECURITY DEFINER functions touch them (migration 00004).
		{"select mfa factors", `SELECT 1 FROM mfa_factors WHERE id = $1`, []any{factor}, denied},
		{"insert mfa factor", `INSERT INTO mfa_factors (user_id, provider, provider_factor_id) VALUES ($1, 'workos', 'forged')`, []any{outsider.id}, denied},
		{"verify mfa factor", `UPDATE mfa_factors SET verified_at = now() WHERE id = $1`, []any{factor}, denied},
		{"delete mfa factor", `DELETE FROM mfa_factors WHERE id = $1`, []any{factor}, denied},
		{"select mfa challenges", `SELECT 1 FROM mfa_challenges WHERE factor_id = $1`, []any{factor}, denied},
		{"complete mfa challenge", `UPDATE mfa_challenges SET consumed_at = now(), completed_at = now() WHERE factor_id = $1`, []any{factor}, denied},
		{"touch and revoke session", `UPDATE sessions SET last_seen_at = now(), idle_expires_at = now(),
			revoked_at = now(), updated_at = now() WHERE id = $1`, []any{session}, ""},
		{"delete session", `DELETE FROM sessions WHERE id = $1`, []any{session}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Rolled back, so the cases do not see each other's writes.
			ctx := t.Context()
			tx, err := connect(t, testDB.RuntimeURL).Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			for _, v := range tenantVars(owned, owner.id, "tenant") {
				if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, v.name, v.value); err != nil {
					t.Fatalf("set %s: %v", v.name, err)
				}
			}

			tag, err := tx.Exec(ctx, tc.sql, tc.args...)
			if tc.wantState == "" {
				if err != nil {
					t.Fatalf("%s as app_runtime: %v", tc.name, err)
				}
				if tag.RowsAffected() != 1 {
					t.Fatalf("%s affected %d rows, want 1", tc.name, tag.RowsAffected())
				}
				return
			}
			if code := sqlState(err); code != tc.wantState {
				t.Fatalf("%s as app_runtime: got %v (SQLSTATE %q), want insufficient_privilege %s", tc.name, err, code, tc.wantState)
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
