//go:build integration

package db_test

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/db"
	schemarules "chaskicode.dev/saas/tests/db"
)

// Integration tests of the three transactions, run as app_runtime against the real
// migrations plus the example tables of testdata/migrations. Fixtures are written by
// the superuser, outside RLS.

// A query under RLS without the variables fails, typically with 42704 in a new session
// and 22P02 in a reused one (design, 7.2). Which of the two is not the contract.
var failClosed = []string{"42704", "22P02"}

// newDB returns a DB over its own app_runtime pool. maxConns 1 pins every transaction
// to the same connection; 0 keeps pgx's default.
func newDB(t *testing.T, maxConns int32) (*db.DB, *pgxpool.Pool) {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(testDB.RuntimeURL)
	if err != nil {
		t.Fatalf("parse runtime url: %v", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool), pool
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

// ----------------------------------------------------------------------------------
// Fixtures (superuser, no RLS)
// ----------------------------------------------------------------------------------

type fixtures struct {
	t     *testing.T
	super *pgx.Conn
}

func newFixtures(t *testing.T) fixtures {
	t.Helper()
	return fixtures{t: t, super: connect(t, testDB.SuperURL)}
}

func (f fixtures) scalar(sql string, args ...any) string {
	f.t.Helper()
	var v string
	if err := f.super.QueryRow(f.t.Context(), sql, args...).Scan(&v); err != nil {
		f.t.Fatalf("fixture %q: %v", sql, err)
	}
	return v
}

func (f fixtures) tenant() string {
	f.t.Helper()
	return f.scalar(`INSERT INTO tenants (slug, name)
		VALUES ('t-' || replace(uuidv7()::text, '-', ''), 'Test tenant') RETURNING id::text`)
}

func (f fixtures) user() string {
	f.t.Helper()
	return f.scalar(`INSERT INTO users (email, email_verified)
		VALUES (replace(uuidv7()::text, '-', '') || '@example.com', true) RETURNING id::text`)
}

func (f fixtures) member(tenant, user, role string) {
	f.t.Helper()
	f.scalar(`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING id::text`,
		tenant, user, role)
}

func (f fixtures) note(tenant, owner string) string {
	f.t.Helper()
	return f.scalar(`INSERT INTO notes (tenant_id, owner_user_id, body) VALUES ($1, $2, 'note') RETURNING id::text`,
		tenant, owner)
}

// caseWithAccess creates a case and an access row for each user.
func (f fixtures) caseWithAccess(tenant string, users ...string) string {
	f.t.Helper()
	id := f.scalar(`INSERT INTO cases (tenant_id, title) VALUES ($1, 'case') RETURNING id::text`, tenant)
	for _, u := range users {
		f.scalar(`INSERT INTO case_access (tenant_id, case_id, user_id) VALUES ($1, $2, $3) RETURNING id::text`,
			tenant, id, u)
	}
	return id
}

func (f fixtures) document(tenant, caseID string) string {
	f.t.Helper()
	return f.scalar(`INSERT INTO case_documents (tenant_id, case_id, name) VALUES ($1, $2, 'doc') RETURNING id::text`,
		tenant, caseID)
}

// ----------------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------------

// principalCtx is what the session and tenant middlewares will leave in the context: a
// User and the Principal of that same user.
func principalCtx(t *testing.T, user, tenant string, perms ...authctx.Permission) context.Context {
	t.Helper()

	p, err := authctx.NewPrincipal(user, tenant, "member", perms...)
	if err != nil {
		t.Fatal(err)
	}
	return authctx.WithPrincipal(userCtx(t, user), p)
}

func userCtx(t *testing.T, user string) context.Context {
	t.Helper()

	u, err := authctx.NewUser(user, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return authctx.WithUser(t.Context(), u)
}

// ids returns the first column of every row, sorted.
func ids(ctx context.Context, q db.Querier, sql string) ([]string, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	slices.Sort(got)
	return got, nil
}

// countRows reads every row rather than a count, so rows streamed before a late error
// are caught too.
func countRows(ctx context.Context, q db.Querier, sql string) (int, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

func sorted(s ...string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func wantFailClosed(t *testing.T, n int, err error) {
	t.Helper()
	if !slices.Contains(failClosed, sqlState(err)) {
		t.Errorf("got error %v (SQLSTATE %q), want one of %v", err, sqlState(err), failClosed)
	}
	if n != 0 {
		t.Errorf("returned %d rows before failing, want 0", n)
	}
}

var errRollback = errors.New("roll back on purpose")

// ----------------------------------------------------------------------------------
// The example tables
// ----------------------------------------------------------------------------------

// The example tables are the pattern apps copy, so they must pass the same structure
// test as the platform's own tables.
func TestExampleTablesFollowTheRules(t *testing.T) {
	t.Parallel()

	conn := connect(t, testDB.OwnerURL)
	violations, err := schemarules.TenantTableViolations(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, vs := range violations {
		for _, v := range vs {
			t.Error(v)
		}
	}

	var n int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name = 'tenant_id' AND table_name = ANY($1)`,
		[]string{"notes", "cases", "case_access", "case_documents"}).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("found %d of the 4 example tables with tenant_id; the test migration did not run", n)
	}
}

// ----------------------------------------------------------------------------------
// WithTenantTx
// ----------------------------------------------------------------------------------

func TestWithTenantTx_IsolatesTenants(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	a, b := f.tenant(), f.tenant()
	userA, userB := f.user(), f.user()
	f.member(a, userA, "owner")
	f.member(b, userB, "owner")
	noteA, noteB := f.note(a, userA), f.note(b, userB)
	caseA, caseB := f.caseWithAccess(a, userA), f.caseWithAccess(b, userB)
	docA, docB := f.document(a, caseA), f.document(b, caseB)

	d, _ := newDB(t, 0)
	ctx := principalCtx(t, userA, a, authctx.PermScopeTenant)

	t.Run("reads only its tenant", func(t *testing.T) {
		err := d.WithTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
			for _, tc := range []struct {
				sql  string
				want []string
			}{
				{`SELECT id::text FROM notes`, []string{noteA}},
				{`SELECT id::text FROM cases`, []string{caseA}},
				{`SELECT case_id::text FROM case_access`, []string{caseA}},
				{`SELECT id::text FROM case_documents`, []string{docA}},
				{`SELECT id::text FROM tenants`, []string{a}},
			} {
				got, err := ids(ctx, q, tc.sql)
				if err != nil {
					return err
				}
				if !slices.Equal(got, tc.want) {
					t.Errorf("%s: got %v, want %v", tc.sql, got, tc.want)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cannot change B forcing its ids", func(t *testing.T) {
		err := d.WithTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
			for _, tc := range []struct{ sql, id string }{
				{`UPDATE notes SET body = 'changed' WHERE id = $1`, noteB},
				{`UPDATE cases SET title = 'changed' WHERE id = $1`, caseB},
				{`UPDATE case_documents SET name = 'changed' WHERE id = $1`, docB},
				{`DELETE FROM case_documents WHERE id = $1`, docB},
				{`DELETE FROM case_access WHERE case_id = $1`, caseB},
				{`DELETE FROM notes WHERE id = $1`, noteB},
				{`DELETE FROM cases WHERE id = $1`, caseB},
			} {
				tag, err := q.Exec(ctx, tc.sql, tc.id)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 0 {
					t.Errorf("%s affected %d rows of tenant B", tc.sql, tag.RowsAffected())
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	// 42501: the row violates tenant_isolation's WITH CHECK. 23503: the composite key
	// refuses a child of A that points at B's case.
	for _, tc := range []struct {
		name string
		sql  string
		args []any
		want string
	}{
		{"note in B", `INSERT INTO notes (tenant_id, owner_user_id, body) VALUES ($1, $2, 'x')`, []any{b, userA}, "42501"},
		{"case in B", `INSERT INTO cases (tenant_id, title) VALUES ($1, 'x')`, []any{b}, "42501"},
		{"access to B's case", `INSERT INTO case_access (tenant_id, case_id, user_id) VALUES ($1, $2, $3)`, []any{b, caseB, userA}, "42501"},
		{"document in B's case", `INSERT INTO case_documents (tenant_id, case_id, name) VALUES ($1, $2, 'x')`, []any{b, caseB}, "42501"},
		{"A's document in B's case", `INSERT INTO case_documents (tenant_id, case_id, name) VALUES ($1, $2, 'x')`, []any{a, caseB}, "23503"},
	} {
		t.Run("cannot insert "+tc.name, func(t *testing.T) {
			err := d.WithTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
				_, err := q.Exec(ctx, tc.sql, tc.args...)
				return err
			})
			if sqlState(err) != tc.want {
				t.Fatalf("got %v (SQLSTATE %q), want %s", err, sqlState(err), tc.want)
			}
		})
	}

	// Seen from outside RLS, B is untouched.
	got := f.scalar(`SELECT concat_ws('/',
		(SELECT body FROM notes WHERE id = $1),
		(SELECT title FROM cases WHERE id = $2),
		(SELECT name FROM case_documents WHERE id = $3),
		(SELECT count(*) FROM case_access WHERE tenant_id = $4),
		(SELECT count(*) FROM notes WHERE tenant_id = $4),
		(SELECT count(*) FROM case_documents WHERE tenant_id = $4))`, noteB, caseB, docB, b)
	if want := "note/case/doc/1/1/1"; got != want {
		t.Fatalf("tenant B after A's attempts: %s, want %s", got, want)
	}
}

// Without a Principal nothing runs: fn is not called and no connection is taken.
func TestWithTenantTx_RequiresPrincipal(t *testing.T) {
	t.Parallel()

	d, pool := newDB(t, 1)
	for name, ctx := range map[string]context.Context{
		"no identity": t.Context(),
		"only a user": userCtx(t, newFixtures(t).user()),
		"zero value":  authctx.WithPrincipal(t.Context(), authctx.Principal{}),
	} {
		called := false
		err := d.WithTenantTx(ctx, func(context.Context, db.Querier) error {
			called = true
			return nil
		})
		if !errors.Is(err, db.ErrNoPrincipal) {
			t.Errorf("%s: got %v, want ErrNoPrincipal", name, err)
		}
		if called {
			t.Errorf("%s: fn ran without a Principal", name)
		}
	}
	if n := pool.Stat().AcquireCount(); n != 0 {
		t.Fatalf("%d connections were acquired without a Principal", n)
	}
}

// set_config(..., true) scopes the variables to the transaction: the pool hands the same
// connection to the next caller without them (design, 7.2 and 11).
func TestVariablesDoNotOutliveTheTransaction(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	a, b := f.tenant(), f.tenant()
	userA, userB := f.user(), f.user()
	noteA, noteB := f.note(a, userA), f.note(b, userB)

	d, pool := newDB(t, 1)
	backendPID := func(ctx context.Context, q db.Querier) (int, error) {
		var pid int
		err := q.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid)
		return pid, err
	}

	var pids []int
	for _, step := range []struct{ tenant, user, note string }{{a, userA, noteA}, {b, userB, noteB}, {a, userA, noteA}} {
		err := d.WithTenantTx(principalCtx(t, step.user, step.tenant), func(ctx context.Context, q db.Querier) error {
			pid, err := backendPID(ctx, q)
			if err != nil {
				return err
			}
			pids = append(pids, pid)
			got, err := ids(ctx, q, `SELECT id::text FROM notes`)
			if err != nil {
				return err
			}
			if !slices.Equal(got, []string{step.note}) {
				t.Errorf("tenant %s sees notes %v, want only %s", step.tenant, got, step.note)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Straight on the pool, outside any helper: the same connection, no variables left.
	ctx := t.Context()
	pid, err := backendPID(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	pids = append(pids, pid)
	if distinct := slices.Compact(slices.Clone(pids)); len(distinct) != 1 {
		t.Fatalf("the transactions ran on backends %v; the test needs a single connection", pids)
	}

	var tenantVar, userVar, scopeVar string
	if err := pool.QueryRow(ctx, `SELECT coalesce(current_setting('app.tenant_id', true), ''),
		coalesce(current_setting('app.user_id', true), ''),
		coalesce(current_setting('app.scope', true), '')`).Scan(&tenantVar, &userVar, &scopeVar); err != nil {
		t.Fatal(err)
	}
	if tenantVar != "" || userVar != "" || scopeVar != "" {
		t.Errorf("variables survived the transaction: tenant %q, user %q, scope %q", tenantVar, userVar, scopeVar)
	}

	n, err := countRows(ctx, pool, `SELECT id FROM notes`)
	wantFailClosed(t, n, err)

	// Nor does a WithUserTx or WithAnonTx on that connection inherit the tenant.
	for name, run := range map[string]func(context.Context, db.TxFunc) error{
		"WithUserTx": d.WithUserTx,
		"WithAnonTx": d.WithAnonTx,
	} {
		var n int
		err := run(userCtx(t, userA), func(ctx context.Context, q db.Querier) error {
			var err error
			n, err = countRows(ctx, q, `SELECT id FROM notes`)
			return err
		})
		t.Run(name, func(t *testing.T) { wantFailClosed(t, n, err) })
	}
}

// Scope own (no scope:tenant) sees its own notes and only the cases, access rows and
// documents it has access to; scope tenant sees them all (design, 7.3).
func TestWithTenantTx_OwnScope(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tnt := f.tenant()
	lawyer, clientX, clientY := f.user(), f.user(), f.user()
	f.member(tnt, lawyer, "lawyer")
	f.member(tnt, clientX, "client")
	f.member(tnt, clientY, "client")
	noteL, noteX, noteY := f.note(tnt, lawyer), f.note(tnt, clientX), f.note(tnt, clientY)
	c1 := f.caseWithAccess(tnt, clientX, lawyer)
	c2 := f.caseWithAccess(tnt, clientY)
	c3 := f.caseWithAccess(tnt)
	d1, d2, d3 := f.document(tnt, c1), f.document(tnt, c2), f.document(tnt, c3)

	d, _ := newDB(t, 0)
	asClient := principalCtx(t, clientX, tnt, "cases:read")
	asLawyer := principalCtx(t, lawyer, tnt, "cases:read", authctx.PermScopeTenant)

	reads := []struct {
		sql         string
		client, all []string
	}{
		{`SELECT id::text FROM notes`, []string{noteX}, sorted(noteL, noteX, noteY)},
		{`SELECT id::text FROM cases`, []string{c1}, sorted(c1, c2, c3)},
		{`SELECT user_id::text FROM case_access`, []string{clientX}, sorted(clientX, lawyer, clientY)},
		{`SELECT id::text FROM case_documents`, []string{d1}, sorted(d1, d2, d3)},
	}
	for _, who := range []struct {
		name   string
		ctx    context.Context
		lawyer bool
	}{{"client", asClient, false}, {"lawyer", asLawyer, true}} {
		t.Run(who.name+" reads", func(t *testing.T) {
			err := d.WithTenantTx(who.ctx, func(ctx context.Context, q db.Querier) error {
				for _, r := range reads {
					got, err := ids(ctx, q, r.sql)
					if err != nil {
						return err
					}
					want := r.client
					if who.lawyer {
						want = r.all
					}
					if !slices.Equal(got, sorted(want...)) {
						t.Errorf("%s: got %v, want %v", r.sql, got, want)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("client cannot touch what it does not see", func(t *testing.T) {
		err := d.WithTenantTx(asClient, func(ctx context.Context, q db.Querier) error {
			for _, tc := range []struct{ sql, id string }{
				{`UPDATE notes SET body = 'changed' WHERE id = $1`, noteY},
				{`DELETE FROM notes WHERE id = $1`, noteL},
				{`UPDATE cases SET title = 'changed' WHERE id = $1`, c3},
				{`DELETE FROM case_documents WHERE id = $1`, d2},
				// Its own access row: visible, but only scope tenant removes access.
				{`DELETE FROM case_access WHERE case_id = $1`, c1},
			} {
				tag, err := q.Exec(ctx, tc.sql, tc.id)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 0 {
					t.Errorf("%s affected %d rows", tc.sql, tag.RowsAffected())
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"a note owned by another user", `INSERT INTO notes (tenant_id, owner_user_id, body) VALUES ($1, $2, 'x')`, []any{tnt, clientY}},
		{"access to a case for itself", `INSERT INTO case_access (tenant_id, case_id, user_id) VALUES ($1, $2, $3)`, []any{tnt, c3, clientX}},
		{"a document in a case without access", `INSERT INTO case_documents (tenant_id, case_id, name) VALUES ($1, $2, 'x')`, []any{tnt, c2}},
	} {
		t.Run("client cannot insert "+tc.name, func(t *testing.T) {
			err := d.WithTenantTx(asClient, func(ctx context.Context, q db.Querier) error {
				_, err := q.Exec(ctx, tc.sql, tc.args...)
				return err
			})
			if sqlState(err) != "42501" {
				t.Fatalf("got %v (SQLSTATE %q), want 42501", err, sqlState(err))
			}
		})
	}

	// The same writes are allowed where they belong (rolled back to keep the fixtures).
	for _, tc := range []struct {
		name string
		ctx  context.Context
		sql  string
		args []any
	}{
		{"client writes its own note", asClient, `INSERT INTO notes (tenant_id, owner_user_id, body) VALUES ($1, $2, 'x')`, []any{tnt, clientX}},
		{"lawyer grants access", asLawyer, `INSERT INTO case_access (tenant_id, case_id, user_id) VALUES ($1, $2, $3)`, []any{tnt, c3, clientX}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := d.WithTenantTx(tc.ctx, func(ctx context.Context, q db.Querier) error {
				if _, err := q.Exec(ctx, tc.sql, tc.args...); err != nil {
					return err
				}
				return errRollback
			})
			if !errors.Is(err, errRollback) {
				t.Fatalf("%s: %v", tc.sql, err)
			}
		})
	}
}

func TestWithTenantTx_SystemPrincipal(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tnt, owner := f.tenant(), f.user()
	f.member(tnt, owner, "owner")
	n1, n2 := f.note(tnt, owner), f.note(tnt, owner)
	if got := f.scalar(`SELECT count(*)::text FROM memberships WHERE user_id = $1`, authctx.SystemUserID); got != "0" {
		t.Fatalf("the system user has %s memberships, want none", got)
	}

	p, err := authctx.SystemPrincipal(tnt, "notes:read")
	if err != nil {
		t.Fatal(err)
	}
	// A job has no session: only the Principal is in the context.
	ctx := authctx.WithPrincipal(t.Context(), p)

	d, _ := newDB(t, 0)
	var auditID string
	err = d.WithTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
		got, err := ids(ctx, q, `SELECT id::text FROM notes`)
		if err != nil {
			return err
		}
		if !slices.Equal(got, sorted(n1, n2)) {
			t.Errorf("system principal sees notes %v, want %v", got, sorted(n1, n2))
		}
		return q.QueryRow(ctx, `SELECT record_audit($1, 'job.ran')::text`, tnt).Scan(&auditID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.scalar(`SELECT concat_ws('/', tenant_id, actor_user_id) FROM audit_log WHERE id = $1`, auditID); got != tnt+"/"+authctx.SystemUserID {
		t.Fatalf("audit event: %s, want tenant/system user", got)
	}
}

// ----------------------------------------------------------------------------------
// WithUserTx and WithAnonTx
// ----------------------------------------------------------------------------------

func TestWithUserTx(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tnt, u := f.tenant(), f.user()
	f.member(tnt, u, "owner")
	f.note(tnt, u)
	ctx := userCtx(t, u)
	d, _ := newDB(t, 0)

	t.Run("calls auth_memberships_for_user", func(t *testing.T) {
		err := d.WithUserTx(ctx, func(ctx context.Context, q db.Querier) error {
			got, err := ids(ctx, q, `SELECT tenant_id::text FROM auth_memberships_for_user()`)
			if err != nil {
				return err
			}
			if !slices.Equal(got, []string{tnt}) {
				t.Errorf("memberships of the user: %v, want [%s]", got, tnt)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	for _, table := range []string{"notes", "cases", "case_access", "case_documents", "memberships", "tenants"} {
		t.Run("cannot read "+table, func(t *testing.T) {
			var n int
			err := d.WithUserTx(ctx, func(ctx context.Context, q db.Querier) error {
				var err error
				n, err = countRows(ctx, q, `SELECT id FROM `+table)
				return err
			})
			wantFailClosed(t, n, err)
		})
	}
}

func TestWithAnonTx(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tnt := f.tenant()
	d, _ := newDB(t, 0)

	t.Run("records an audit event without tenant", func(t *testing.T) {
		var id string
		err := d.WithAnonTx(t.Context(), func(ctx context.Context, q db.Querier) error {
			return q.QueryRow(ctx, `SELECT record_audit(NULL, 'auth.login_failed')::text`).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.scalar(`SELECT (tenant_id IS NULL AND actor_user_id IS NULL)::text FROM audit_log WHERE id = $1`, id); got != "true" {
			t.Fatal("the anonymous event has a tenant or an actor")
		}
	})

	t.Run("cannot record a tenant's event", func(t *testing.T) {
		err := d.WithAnonTx(t.Context(), func(ctx context.Context, q db.Querier) error {
			_, err := q.Exec(ctx, `SELECT record_audit($1, 'tenant.event')`, tnt)
			return err
		})
		e, ok := apperr.As(db.MapError(err))
		if !ok || e.Code != "audit.tenant_not_set" || e.Kind != apperr.KindInternal {
			t.Fatalf("got %v, want TN031 mapped to audit.tenant_not_set", err)
		}
	})

	for name, sql := range map[string]string{
		"a business table":  `SELECT id FROM notes`,
		"a platform table":  `SELECT id FROM memberships`,
		"a user's function": `SELECT tenant_id FROM auth_memberships_for_user()`,
	} {
		t.Run("cannot read "+name, func(t *testing.T) {
			var n int
			err := d.WithAnonTx(t.Context(), func(ctx context.Context, q db.Querier) error {
				var err error
				n, err = countRows(ctx, q, sql)
				return err
			})
			wantFailClosed(t, n, err)
		})
	}
}

// ----------------------------------------------------------------------------------
// Commit, rollback and nesting
// ----------------------------------------------------------------------------------

func TestTx_CommitAndRollback(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tnt, u := f.tenant(), f.user()
	f.member(tnt, u, "owner")
	ctx := principalCtx(t, u, tnt, authctx.PermScopeTenant)
	// One connection: a transaction that leaked it would block every later case.
	d, _ := newDB(t, 1)

	insert := func(ctx context.Context, q db.Querier, body string) error {
		_, err := q.Exec(ctx, `INSERT INTO notes (tenant_id, owner_user_id, body) VALUES ($1, $2, $3)`, tnt, u, body)
		return err
	}
	stored := func(body string) bool {
		return f.scalar(`SELECT count(*)::text FROM notes WHERE tenant_id = $1 AND body = $2`, tnt, body) == "1"
	}
	timeout := func(t *testing.T) context.Context {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		t.Cleanup(cancel)
		return ctx
	}

	t.Run("commits when fn returns nil", func(t *testing.T) {
		if err := d.WithTenantTx(timeout(t), func(ctx context.Context, q db.Querier) error {
			return insert(ctx, q, "committed")
		}); err != nil {
			t.Fatal(err)
		}
		if !stored("committed") {
			t.Fatal("not committed")
		}
	})

	t.Run("rolls back when fn fails", func(t *testing.T) {
		errBoom := errors.New("boom")
		err := d.WithTenantTx(timeout(t), func(ctx context.Context, q db.Querier) error {
			if err := insert(ctx, q, "on error"); err != nil {
				return err
			}
			return errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("got %v, want fn's error", err)
		}
		if stored("on error") {
			t.Fatal("committed although fn failed")
		}
	})

	t.Run("rolls back and re-panics", func(t *testing.T) {
		func() {
			defer func() {
				if r := recover(); r != "boom" {
					t.Fatalf("recovered %v, want the original panic", r)
				}
			}()
			_ = d.WithTenantTx(timeout(t), func(ctx context.Context, q db.Querier) error {
				if err := insert(ctx, q, "on panic"); err != nil {
					return err
				}
				panic("boom")
			})
			t.Fatal("the panic was swallowed")
		}()
		if stored("on panic") {
			t.Fatal("committed although fn panicked")
		}
	})

	t.Run("rolls back on Goexit", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = d.WithTenantTx(timeout(t), func(ctx context.Context, q db.Querier) error {
				if err := insert(ctx, q, "on goexit"); err != nil {
					return err
				}
				runtime.Goexit()
				return nil
			})
		}()
		<-done
		if stored("on goexit") {
			t.Fatal("committed although fn exited the goroutine")
		}
	})

	t.Run("does not commit a failed transaction", func(t *testing.T) {
		err := d.WithTenantTx(timeout(t), func(ctx context.Context, q db.Querier) error {
			if err := insert(ctx, q, "swallowed"); err != nil {
				return err
			}
			_, _ = q.Exec(ctx, `SELECT 1 / 0`) // fn ignores the error; the transaction is aborted
			return nil
		})
		if err == nil {
			t.Fatal("WithTenantTx reported success for an aborted transaction")
		}
		if stored("swallowed") {
			t.Fatal("committed an aborted transaction")
		}
	})

	t.Run("the connection is free afterwards", func(t *testing.T) {
		if err := d.WithTenantTx(timeout(t), func(ctx context.Context, q db.Querier) error {
			_, err := q.Exec(ctx, `SELECT 1`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTx_NestingIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tnt, u := f.tenant(), f.user()
	f.member(tnt, u, "owner")
	// Both a User and a Principal, so any helper would accept the context on its own.
	ctx := principalCtx(t, u, tnt, authctx.PermScopeTenant)
	d, _ := newDB(t, 0)

	helpers := map[string]func(context.Context, db.TxFunc) error{
		"WithTenantTx": d.WithTenantTx,
		"WithUserTx":   d.WithUserTx,
		"WithAnonTx":   d.WithAnonTx,
	}
	for outerName, outer := range helpers {
		for innerName, inner := range helpers {
			t.Run(outerName+"/"+innerName, func(t *testing.T) {
				t.Parallel()

				innerRan := false
				err := outer(ctx, func(ctx context.Context, _ db.Querier) error {
					return inner(ctx, func(context.Context, db.Querier) error {
						innerRan = true
						return nil
					})
				})
				if !errors.Is(err, db.ErrNestedTx) {
					t.Fatalf("got %v, want ErrNestedTx", err)
				}
				if innerRan {
					t.Fatal("the nested fn ran")
				}
			})
		}
	}
}

func TestTx_Independent(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tnt, u := f.tenant(), f.user()
	f.member(tnt, u, "owner")
	ctx := principalCtx(t, u, tnt, authctx.PermScopeTenant)
	// Two connections: the outer transaction keeps one while the independent one runs.
	d, _ := newDB(t, 2)
	insert := func(ctx context.Context, q db.Querier, body string) error {
		_, err := q.Exec(ctx, `INSERT INTO notes (tenant_id, owner_user_id, body) VALUES ($1, $2, $3)`, tnt, u, body)
		return err
	}
	stored := func(body string) bool {
		return f.scalar(`SELECT count(*)::text FROM notes WHERE tenant_id = $1 AND body = $2`, tnt, body) == "1"
	}

	t.Run("survives the outer rollback", func(t *testing.T) {
		errBoom := errors.New("boom")
		err := d.WithTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
			if err := insert(ctx, q, "outer"); err != nil {
				return err
			}
			if err := d.WithTenantTx(db.Independent(ctx), func(ctx context.Context, q db.Querier) error {
				return insert(ctx, q, "independent")
			}); err != nil {
				return err
			}
			return errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("got %v, want the outer error", err)
		}
		if stored("outer") || !stored("independent") {
			t.Fatalf("outer stored %v (want false), independent stored %v (want true)", stored("outer"), stored("independent"))
		}
	})

	t.Run("nesting is refused again inside it", func(t *testing.T) {
		err := d.WithTenantTx(db.Independent(ctx), func(ctx context.Context, _ db.Querier) error {
			return d.WithTenantTx(ctx, func(context.Context, db.Querier) error { return nil })
		})
		if !errors.Is(err, db.ErrNestedTx) {
			t.Fatalf("got %v, want ErrNestedTx", err)
		}
	})

	t.Run("does not wait forever on the outer transaction's locks", func(t *testing.T) {
		start := time.Now()
		err := d.WithTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
			// The independent insert needs a KEY SHARE lock on this row for its foreign key.
			if _, err := q.Exec(ctx, `SELECT id FROM tenants WHERE id = $1 FOR UPDATE`, tnt); err != nil {
				return err
			}
			return d.WithTenantTx(db.Independent(ctx), func(ctx context.Context, q db.Querier) error {
				return insert(ctx, q, "blocked")
			})
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("got %v, want lock_not_available (55P03)", err)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Fatalf("took %s", d)
		}
	})
}
