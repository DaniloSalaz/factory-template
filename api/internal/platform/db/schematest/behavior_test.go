package schematest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Behavior tests: SQL run as app_runtime, the role the application uses, with the
// variables set the way WithTenantTx, WithUserTx and WithAnonTx will set them
// (set_config(..., true), so they last only for the transaction). Fixtures are created
// with the superuser, outside RLS, and every test uses its own tenants and users.

const systemUserID = "00000000-0000-7000-8000-000000000001"

type setting struct{ name, value string }

func tenantVars(tenant, user, scope string) []setting {
	return []setting{{"app.tenant_id", tenant}, {"app.user_id", user}, {"app.scope", scope}}
}

func userVars(user string) []setting { return []setting{{"app.user_id", user}} }

// inTx runs fn in a transaction on conn with the given variables. It commits when fn
// succeeds, so later transactions on the same connection see a reused session.
func inTx(t *testing.T, conn *pgx.Conn, vars []setting, fn func(tx pgx.Tx) error) error {
	t.Helper()

	ctx := t.Context()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, v := range vars {
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, v.name, v.value); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("set %s: %v", v.name, err)
		}
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return nil
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

func (f fixtures) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.super.Exec(f.t.Context(), sql, args...); err != nil {
		f.t.Fatalf("fixture %q: %v", sql, err)
	}
}

func (f fixtures) tenant(status string) string {
	f.t.Helper()
	return f.scalar(`INSERT INTO tenants (slug, name, status)
		VALUES ('t-' || replace(uuidv7()::text, '-', ''), 'Test tenant', $1) RETURNING id::text`, status)
}

type user struct{ id, email string }

func (f fixtures) user(verified, disabled bool) user {
	f.t.Helper()
	var u user
	err := f.super.QueryRow(f.t.Context(), `
		INSERT INTO users (email, email_verified, disabled_at)
		VALUES (replace(uuidv7()::text, '-', '') || '@Example.com', $1, CASE WHEN $2 THEN now() END)
		RETURNING id::text, email::text`, verified, disabled).Scan(&u.id, &u.email)
	if err != nil {
		f.t.Fatalf("fixture user: %v", err)
	}
	return u
}

func (f fixtures) member(tenant string, u user, role string) string {
	f.t.Helper()
	return f.scalar(`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING id::text`,
		tenant, u.id, role)
}

// invitation inserts an invitation and returns its token hash. extra is SQL applied to
// the new row (e.g. "accepted_at = now()").
func (f fixtures) invitation(tenant, email string, extra string) []byte {
	f.t.Helper()
	var hash []byte
	err := f.super.QueryRow(f.t.Context(), `
		INSERT INTO invitations (tenant_id, email, role, token_hash, expires_at)
		VALUES ($1, $2, 'member', sha256(uuidv7()::text::bytea), now() + interval '7 days')
		RETURNING token_hash`, tenant, email).Scan(&hash)
	if err != nil {
		f.t.Fatalf("fixture invitation: %v", err)
	}
	if extra != "" {
		f.exec(`UPDATE invitations SET `+extra+` WHERE token_hash = $1`, hash)
	}
	return hash
}

// twoTenants seeds tenants A and B with an owner each, plus an admin and a client in A,
// and one invitation and one audit event in each tenant.
type twoTenants struct {
	a, b                    string
	ownerA, ownerB, clientA user
	clientMembership        string
}

func seedTwoTenants(t *testing.T) twoTenants {
	t.Helper()

	f := newFixtures(t)
	s := twoTenants{a: f.tenant("active"), b: f.tenant("active")}
	s.ownerA, s.ownerB, s.clientA = f.user(true, false), f.user(true, false), f.user(true, false)
	f.member(s.a, s.ownerA, "owner")
	f.member(s.b, s.ownerB, "owner")
	f.member(s.a, f.user(true, false), "admin")
	s.clientMembership = f.member(s.a, s.clientA, "client")
	for _, tenant := range []string{s.a, s.b} {
		f.invitation(tenant, "invitee-"+tenant+"@example.com", "")
		f.exec(`INSERT INTO audit_log (tenant_id, action) VALUES ($1, 'test.event')`, tenant)
	}
	return s
}

func countRows(t *testing.T, tx pgx.Tx, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func wantSQLState(t *testing.T, err error, want string) {
	t.Helper()
	if got := sqlState(err); got != want {
		t.Fatalf("got error %v (SQLSTATE %q), want SQLSTATE %s", err, got, want)
	}
}

var rlsTables = []string{"memberships", "invitations", "audit_log"}

// ----------------------------------------------------------------------------------
// Row-level security
// ----------------------------------------------------------------------------------

func TestRLS_FailsClosedWithoutTenant(t *testing.T) {
	t.Parallel()

	s := seedTwoTenants(t)
	// Typically 42704 in a new session (the variable does not exist) and 22P02 in a reused
	// one (it is back to '' and the uuid cast fails). Which of the two is not the
	// contract; failing without returning a single row is.
	failClosed := []string{"42704", "22P02"}

	// selectAll reads every row rather than a count, so rows streamed before a late
	// error are caught too.
	selectAll := func(t *testing.T, conn *pgx.Conn, vars []setting, table string) (int, error) {
		t.Helper()
		var n int
		err := inTx(t, conn, vars, func(tx pgx.Tx) error {
			rows, err := tx.Query(t.Context(), `SELECT tenant_id FROM `+table)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				n++
			}
			return rows.Err()
		})
		return n, err
	}
	wantFailClosed := func(t *testing.T, n int, err error) {
		t.Helper()
		if !slices.Contains(failClosed, sqlState(err)) {
			t.Errorf("got error %v (SQLSTATE %q), want one of %v", err, sqlState(err), failClosed)
		}
		if n != 0 {
			t.Errorf("returned %d rows before failing, want 0", n)
		}
	}

	for _, table := range rlsTables {
		t.Run(table+"/new connection", func(t *testing.T) {
			t.Parallel()

			n, err := selectAll(t, connect(t, testDB.RuntimeURL), nil, table)
			wantFailClosed(t, n, err)
		})
		t.Run(table+"/reused connection", func(t *testing.T) {
			t.Parallel()

			conn := connect(t, testDB.RuntimeURL)
			if n, err := selectAll(t, conn, tenantVars(s.a, s.ownerA.id, "tenant"), table); err != nil || n == 0 {
				t.Fatalf("SELECT with app.tenant_id: %d rows, %v; want rows and no error", n, err)
			}
			n, err := selectAll(t, conn, nil, table)
			wantFailClosed(t, n, err)
		})
	}
}

func TestRLS_SameConnectionSeesOnlyCurrentTenant(t *testing.T) {
	t.Parallel()

	s := seedTwoTenants(t)
	conn := connect(t, testDB.RuntimeURL)
	for _, step := range []struct{ tenant, user string }{{s.a, s.ownerA.id}, {s.b, s.ownerB.id}, {s.a, s.ownerA.id}} {
		for _, table := range rlsTables {
			err := inTx(t, conn, tenantVars(step.tenant, step.user, "tenant"), func(tx pgx.Tx) error {
				rows, err := tx.Query(t.Context(), `SELECT DISTINCT tenant_id::text FROM `+table)
				if err != nil {
					return err
				}
				got, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				if !slices.Equal(got, []string{step.tenant}) {
					t.Errorf("%s with tenant %s sees tenants %v", table, step.tenant, got)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("%s: %v", table, err)
			}
		}
	}
}

func TestRLS_CrossTenantAccessIsBlocked(t *testing.T) {
	t.Parallel()

	s := seedTwoTenants(t)
	other := newFixtures(t).user(true, false)

	tests := []struct {
		name      string
		sql       string
		args      []any
		wantState string // SQLSTATE expected, or "" for success
		wantRows  int64  // rows affected (or counted) on success
	}{
		{"read B memberships", `SELECT count(*) FROM memberships WHERE tenant_id = $1`, []any{s.b}, "", 0},
		{"read B invitations", `SELECT count(*) FROM invitations WHERE tenant_id = $1`, []any{s.b}, "", 0},
		{"read B audit", `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, []any{s.b}, "", 0},
		// Without INSERT on memberships this fails on the privilege before RLS; the WITH
		// CHECK side of tenant_isolation is covered by moving a membership to B below.
		{"insert membership into B", `INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'member')`, []any{s.b, other.id}, "42501", 0},
		{"insert invitation into B", `INSERT INTO invitations (tenant_id, email, role, token_hash, expires_at)
			VALUES ($1, 'x@example.com', 'member', sha256('x'::bytea), now())`, []any{s.b}, "42501", 0},
		{"update B memberships", `UPDATE memberships SET role = 'owner' WHERE tenant_id = $1`, []any{s.b}, "", 0},
		{"move an A membership to B", `UPDATE memberships SET tenant_id = $1 WHERE user_id = $2`, []any{s.b, s.ownerA.id}, "42501", 0},
		{"delete B memberships", `DELETE FROM memberships WHERE tenant_id = $1`, []any{s.b}, "", 0},
		{"delete B invitations", `DELETE FROM invitations WHERE tenant_id = $1`, []any{s.b}, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			conn := connect(t, testDB.RuntimeURL)
			err := inTx(t, conn, tenantVars(s.a, s.ownerA.id, "tenant"), func(tx pgx.Tx) error {
				if tc.sql[:6] == "SELECT" {
					if n := countRows(t, tx, tc.sql, tc.args...); int64(n) != tc.wantRows {
						t.Errorf("tenant A counted %d rows of B, want %d", n, tc.wantRows)
					}
					return nil
				}
				tag, err := tx.Exec(t.Context(), tc.sql, tc.args...)
				if err == nil && tag.RowsAffected() != tc.wantRows {
					t.Errorf("tenant A affected %d rows of B, want %d", tag.RowsAffected(), tc.wantRows)
				}
				return err
			})
			if tc.wantState == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			wantSQLState(t, err, tc.wantState)
		})
	}

	// And B's data is untouched.
	f := newFixtures(t)
	if n := f.scalar(`SELECT count(*)::text FROM memberships WHERE tenant_id = $1 AND role = 'owner'`, s.b); n != "1" {
		t.Fatalf("tenant B owner memberships = %s, want 1", n)
	}
}

func TestRLS_OwnScope(t *testing.T) {
	t.Parallel()

	s := seedTwoTenants(t)

	t.Run("client sees only its own membership and no invitations or audit", func(t *testing.T) {
		conn := connect(t, testDB.RuntimeURL)
		err := inTx(t, conn, tenantVars(s.a, s.clientA.id, "own"), func(tx pgx.Tx) error {
			rows, err := tx.Query(t.Context(), `SELECT id::text FROM memberships`)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if !slices.Equal(ids, []string{s.clientMembership}) {
				t.Errorf("memberships = %v, want only %s", ids, s.clientMembership)
			}
			if n := countRows(t, tx, `SELECT count(*) FROM invitations`); n != 0 {
				t.Errorf("invitations = %d, want 0", n)
			}
			if n := countRows(t, tx, `SELECT count(*) FROM audit_log`); n != 0 {
				t.Errorf("audit_log = %d, want 0", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("client cannot delete someone else's membership", func(t *testing.T) {
		conn := connect(t, testDB.RuntimeURL)
		err := inTx(t, conn, tenantVars(s.a, s.clientA.id, "own"), func(tx pgx.Tx) error {
			tag, err := tx.Exec(t.Context(), `DELETE FROM memberships WHERE user_id <> $1`, s.clientA.id)
			if err == nil && tag.RowsAffected() != 0 {
				t.Errorf("deleted %d memberships of others, want 0", tag.RowsAffected())
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("scope tenant sees the whole tenant", func(t *testing.T) {
		conn := connect(t, testDB.RuntimeURL)
		err := inTx(t, conn, tenantVars(s.a, s.ownerA.id, "tenant"), func(tx pgx.Tx) error {
			if n := countRows(t, tx, `SELECT count(*) FROM memberships`); n != 3 {
				t.Errorf("memberships = %d, want 3", n)
			}
			if n := countRows(t, tx, `SELECT count(*) FROM invitations`); n != 1 {
				t.Errorf("invitations = %d, want 1", n)
			}
			if n := countRows(t, tx, `SELECT count(*) FROM audit_log`); n != 1 {
				t.Errorf("audit_log = %d, want 1", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ----------------------------------------------------------------------------------
// SECURITY DEFINER functions
// ----------------------------------------------------------------------------------

func TestFunc_AuthMembershipAnswersOnlyForCurrentUser(t *testing.T) {
	t.Parallel()

	s := seedTwoTenants(t)
	tests := []struct {
		name, user, tenant, wantRole string
	}{
		{"owner of A asks about A", s.ownerA.id, s.a, "owner"},
		{"owner of A asks about B", s.ownerA.id, s.b, ""},
		{"owner of B asks about B", s.ownerB.id, s.b, "owner"},
		{"client of A asks about A", s.clientA.id, s.a, "client"},
		{"system user has no membership", systemUserID, s.a, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			conn := connect(t, testDB.RuntimeURL)
			err := inTx(t, conn, userVars(tc.user), func(tx pgx.Tx) error {
				rows, err := tx.Query(t.Context(), `SELECT role FROM auth_membership($1)`, tc.tenant)
				if err != nil {
					return err
				}
				roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				switch {
				case tc.wantRole == "" && len(roles) != 0:
					t.Errorf("roles = %v, want none", roles)
				case tc.wantRole != "" && !slices.Equal(roles, []string{tc.wantRole}):
					t.Errorf("roles = %v, want [%s]", roles, tc.wantRole)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFunc_CreateTenantWithOwner(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	active := f.user(true, false)
	taken := f.scalar(`SELECT slug FROM tenants WHERE id = $1`, f.tenant("active"))

	tests := []struct {
		name, user, slug, wantState string
	}{
		{"active user becomes owner", active.id, "acme-" + active.id[:8], ""},
		{"disabled user", f.user(true, true).id, "disabled-" + active.id[:8], "TN002"},
		{"system user is disabled", systemUserID, "system-" + active.id[:8], "TN002"},
		{"unknown user", "01890000-0000-7000-8000-000000000000", "ghost-" + active.id[:8], "TN001"},
		{"slug taken", active.id, taken, "TN020"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var tenantID string
			conn := connect(t, testDB.RuntimeURL)
			err := inTx(t, conn, userVars(tc.user), func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT tenant_id::text FROM create_tenant_with_owner('Acme', $1)`, tc.slug).Scan(&tenantID)
			})
			if tc.wantState != "" {
				wantSQLState(t, err, tc.wantState)
				return
			}
			if err != nil {
				t.Fatalf("create_tenant_with_owner: %v", err)
			}
			if role := newFixtures(t).scalar(`SELECT role FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenantID, tc.user); role != "owner" {
				t.Fatalf("creator role = %q, want owner", role)
			}
		})
	}
}

func TestFunc_AcceptInvitation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(f fixtures) (u user, token []byte)
		wantState string
	}{
		{"matching verified email, any case", func(f fixtures) (user, []byte) {
			u := f.user(true, false)
			return u, f.invitation(f.tenant("active"), strings.ToUpper(u.email), "")
		}, ""},
		{"different email", func(f fixtures) (user, []byte) {
			return f.user(true, false), f.invitation(f.tenant("active"), "someone-else@example.com", "")
		}, "TN015"},
		{"unverified email", func(f fixtures) (user, []byte) {
			u := f.user(false, false)
			return u, f.invitation(f.tenant("active"), u.email, "")
		}, "TN015"},
		{"token already used", func(f fixtures) (user, []byte) {
			u := f.user(true, false)
			return u, f.invitation(f.tenant("active"), u.email, "accepted_at = now()")
		}, "TN011"},
		{"expired", func(f fixtures) (user, []byte) {
			u := f.user(true, false)
			return u, f.invitation(f.tenant("active"), u.email, "expires_at = now() - interval '1 second'")
		}, "TN013"},
		{"revoked", func(f fixtures) (user, []byte) {
			u := f.user(true, false)
			return u, f.invitation(f.tenant("active"), u.email, "revoked_at = now()")
		}, "TN012"},
		{"tenant suspended", func(f fixtures) (user, []byte) {
			u := f.user(true, false)
			return u, f.invitation(f.tenant("suspended"), u.email, "")
		}, "TN014"},
		{"already a member", func(f fixtures) (user, []byte) {
			u := f.user(true, false)
			tenant := f.tenant("active")
			f.member(tenant, u, "member")
			return u, f.invitation(tenant, u.email, "")
		}, "TN016"},
		{"disabled user", func(f fixtures) (user, []byte) {
			u := f.user(true, true)
			return u, f.invitation(f.tenant("active"), u.email, "")
		}, "TN002"},
		{"unknown token", func(f fixtures) (user, []byte) {
			return f.user(true, false), []byte("0123456789abcdef0123456789abcdef")
		}, "TN010"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixtures(t)
			u, token := tc.setup(f)
			var membershipID string
			conn := connect(t, testDB.RuntimeURL)
			err := inTx(t, conn, userVars(u.id), func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT membership_id::text FROM accept_invitation($1)`, token).Scan(&membershipID)
			})
			if tc.wantState != "" {
				wantSQLState(t, err, tc.wantState)
				return
			}
			if err != nil {
				t.Fatalf("accept_invitation: %v", err)
			}
			if got := f.scalar(`SELECT (accepted_at IS NOT NULL)::text FROM invitations WHERE token_hash = $1`, token); got != "true" {
				t.Fatal("invitation not marked as accepted")
			}
			if got := f.scalar(`SELECT user_id::text FROM memberships WHERE id = $1`, membershipID); got != u.id {
				t.Fatalf("membership user = %s, want %s", got, u.id)
			}
		})
	}
}

// Two accepts of the same token: the second blocks on the invitation's row lock, then
// finds it accepted. Only one membership is created.
func TestFunc_AcceptInvitationConcurrently(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	f := newFixtures(t)
	u := f.user(true, false)
	tenant := f.tenant("active")
	token := f.invitation(tenant, u.email, "")

	begin := func(conn *pgx.Conn) pgx.Tx {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, u.id); err != nil {
			t.Fatalf("set user: %v", err)
		}
		return tx
	}

	first, second := connect(t, testDB.RuntimeURL), connect(t, testDB.RuntimeURL)
	tx1 := begin(first)
	defer func() { _ = tx1.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx1.Exec(ctx, `SELECT * FROM accept_invitation($1)`, token); err != nil {
		t.Fatalf("first accept: %v", err)
	}

	secondPID := second.PgConn().PID()
	done := make(chan error, 1)
	go func() {
		tx2 := begin(second)
		_, err := tx2.Exec(ctx, `SELECT * FROM accept_invitation($1)`, token)
		_ = tx2.Rollback(context.WithoutCancel(ctx))
		done <- err
	}()

	// Commit the first one only once the second is provably waiting on the row lock.
	deadline := time.Now().Add(10 * time.Second)
	for f.scalar(`SELECT coalesce(wait_event_type, '') FROM pg_stat_activity WHERE pid = $1`, secondPID) != "Lock" {
		if time.Now().After(deadline) {
			t.Fatal("second accept never waited on the invitation lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit first accept: %v", err)
	}

	wantSQLState(t, <-done, "TN011")
	if n := f.scalar(`SELECT count(*)::text FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenant, u.id); n != "1" {
		t.Fatalf("memberships = %s, want 1", n)
	}
}

func TestFunc_RecordAudit(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	tenant := f.tenant("active")
	actor := f.user(true, false)

	tests := []struct {
		name      string
		vars      []setting
		wantActor string
	}{
		{"anonymous event has no actor", nil, ""},
		{"actor comes from app.user_id", userVars(actor.id), actor.id},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var id string
			conn := connect(t, testDB.RuntimeURL)
			err := inTx(t, conn, tc.vars, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT record_audit($1, 'test.recorded')::text`, tenant).Scan(&id)
			})
			if err != nil {
				t.Fatalf("record_audit: %v", err)
			}
			got := newFixtures(t).scalar(`SELECT coalesce(actor_user_id::text, '') FROM audit_log WHERE id = $1`, id)
			if got != tc.wantActor {
				t.Fatalf("actor = %q, want %q", got, tc.wantActor)
			}
		})
	}
}

func TestFunc_TenantStatusAndRolesInUse(t *testing.T) {
	t.Parallel()

	s := seedTwoTenants(t)
	suspended := newFixtures(t).tenant("suspended")
	conn := connect(t, testDB.RuntimeURL)
	err := inTx(t, conn, nil, func(tx pgx.Tx) error {
		for tenant, want := range map[string]*string{s.a: ptr("active"), suspended: ptr("suspended"), "01890000-0000-7000-8000-000000000000": nil} {
			var got *string
			if err := tx.QueryRow(t.Context(), `SELECT tenant_status($1)`, tenant).Scan(&got); err != nil {
				return err
			}
			if (got == nil) != (want == nil) || (got != nil && *got != *want) {
				t.Errorf("tenant_status(%s) = %v, want %v", tenant, deref(got), deref(want))
			}
		}
		rows, err := tx.Query(t.Context(), `SELECT * FROM roles_in_use()`)
		if err != nil {
			return err
		}
		roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, want := range []string{"admin", "client", "owner"} {
			if !slices.Contains(roles, want) {
				t.Errorf("roles_in_use() = %v, missing %q", roles, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func ptr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
