package schematest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The tenant and member functions of migration 00005 (design 5.5 and 5.7, ADR 0003),
// called as app_runtime with the variables WithUserTx and WithTenantTx set.

// execAs runs sql in one transaction as app_runtime with vars and returns its error.
func execAs(t *testing.T, vars []setting, sql string, args ...any) error {
	t.Helper()
	return inTx(t, connect(t, testDB.RuntimeURL), vars, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), sql, args...)
		return err
	})
}

// slugFor returns a slug unique to this test run.
func slugFor(prefix, id string) string {
	return prefix + "-" + strings.ReplaceAll(id, "-", "")[:20]
}

func TestFunc_CreateTenant(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	active := f.user(true, false)
	taken := f.scalar(`SELECT slug FROM tenants WHERE id = $1`, f.tenant("active"))
	atLimit := f.user(true, false)
	for range 2 {
		f.exec(`INSERT INTO tenants (slug, name, created_by)
			VALUES ('t-' || replace(uuidv7()::text, '-', ''), 'Created before', $1)`, atLimit.id)
	}

	tests := []struct {
		name, user, slug string
		limit            int
		wantState        string
	}{
		{"active user becomes owner", active.id, slugFor("acme", active.id), 5, ""},
		{"disabled user", f.user(true, true).id, slugFor("disabled", active.id), 5, "TN002"},
		{"system user is disabled", systemUserID, slugFor("system", active.id), 5, "TN002"},
		{"unknown user", "01890000-0000-7000-8000-000000000000", slugFor("ghost", active.id), 5, "TN001"},
		{"slug taken", active.id, taken, 5, "TN020"},
		{"limit reached", atLimit.id, slugFor("limit", atLimit.id), 2, "TN021"},
		{"slug longer than 40", active.id, strings.Repeat("a", 41), 5, "23514"},
		{"slug ending with a hyphen", active.id, "acme-", 5, "23514"},
		{"no limit", active.id, slugFor("nolimit", active.id), 0, "22023"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var tenantID string
			conn := connect(t, testDB.RuntimeURL)
			err := inTx(t, conn, userVars(tc.user), func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT tenant_id::text FROM create_tenant('Acme', $1, $2, $3)`,
					tc.slug, tc.limit, testIPPrefix).Scan(&tenantID)
			})
			if tc.wantState != "" {
				wantSQLState(t, err, tc.wantState)
				return
			}
			if err != nil {
				t.Fatalf("create_tenant: %v", err)
			}
			f := newFixtures(t)
			if got := f.scalar(`SELECT created_by::text FROM tenants WHERE id = $1`, tenantID); got != tc.user {
				t.Fatalf("created_by = %s, want %s", got, tc.user)
			}
			f.scalar(`SELECT id::text FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND role = 'owner'`, tenantID, tc.user)
			want := []string{
				"membership.created/" + tc.user + "/user/" + tc.user + "/owner/" + testIPPrefix,
				"tenant.created/" + tc.user + "/tenant/" + tenantID + "/" + testIPPrefix,
			}
			if got := f.auditEvents(tenantID); !slices.Equal(got, want) {
				t.Fatalf("audit events = %v, want %v", got, want)
			}
		})
	}
}

// Two creations by the same user with room for one: the second waits on the user's
// advisory lock, then counts the first and is refused.
func TestFunc_CreateTenantLimitHoldsConcurrently(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	f := newFixtures(t)
	u := f.user(true, false)

	first, second := connect(t, testDB.RuntimeURL), connect(t, testDB.RuntimeURL)
	tx1 := beginWith(ctx, t, first, userVars(u.id))
	defer func() { _ = tx1.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx1.Exec(ctx, `SELECT * FROM create_tenant('One', $1, 1)`, slugFor("one", u.id)); err != nil {
		t.Fatalf("first create: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		tx2 := beginWith(ctx, t, second, userVars(u.id))
		_, err := tx2.Exec(ctx, `SELECT * FROM create_tenant('Two', $1, 1)`, slugFor("two", u.id))
		_ = tx2.Rollback(context.WithoutCancel(ctx))
		done <- err
	}()
	waitForLock(t, f, second.PgConn().PID(), "second create")
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit first create: %v", err)
	}

	wantSQLState(t, <-done, "TN021")
	if n := f.scalar(`SELECT count(*)::text FROM tenants WHERE created_by = $1`, u.id); n != "1" {
		t.Fatalf("tenants created = %s, want 1", n)
	}
}

// members seeds an active tenant with the given roles, one user each, in order.
func (f fixtures) members(roles ...string) (string, []user) {
	f.t.Helper()
	tenant := f.tenant("active")
	users := make([]user, len(roles))
	for i, role := range roles {
		users[i] = f.user(true, false)
		f.member(tenant, users[i], role)
	}
	return tenant, users
}

func (f fixtures) roleOf(tenant string, u user) string {
	f.t.Helper()
	var role *string
	if err := f.super.QueryRow(f.t.Context(), `SELECT role FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		tenant, u.id).Scan(&role); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		f.t.Fatalf("role of %s: %v", u.id, err)
	}
	if role == nil {
		return "<none>"
	}
	return *role
}

// memberEvents returns the tenant's membership.* events as action/actor/target/
// metadata, sorted by action.
func (f fixtures) memberEvents(tenant string) []string {
	f.t.Helper()
	rows, err := f.super.Query(f.t.Context(), `
		SELECT concat_ws('/', action, actor_user_id, target_type, target_id,
		                 metadata->>'role', metadata->>'from_role', metadata->>'to_role', ip_prefix)
		FROM audit_log WHERE tenant_id = $1 AND action LIKE 'membership.%' ORDER BY action, created_at`, tenant)
	if err != nil {
		f.t.Fatalf("member events: %v", err)
	}
	events, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatalf("member events: %v", err)
	}
	return events
}

func TestFunc_ChangeMemberRole(t *testing.T) {
	t.Parallel()

	// call is change_member_role(member, role, expected actor role, expected member role,
	// ip prefix), run as actor in tenant.
	type call struct {
		tenant, actor, member       string
		role, actorRole, memberRole string
		ipPrefix                    any
	}
	tests := []struct {
		name  string
		setup func(f fixtures) call
		// wantState is the SQLSTATE of a refusal; on success, wantEvent is the audit event
		// (empty: none).
		wantState, wantEvent string
	}{
		{"an owner demotes another owner", func(f fixtures) call {
			tenant, u := f.members("owner", "owner")
			return call{tenant, u[0].id, u[1].id, "admin", "owner", "owner", testIPPrefix}
		}, "", "membership.role_changed/{actor}/user/{member}/owner/admin/" + testIPPrefix},
		{"an admin promotes a member", func(f fixtures) call {
			tenant, u := f.members("owner", "admin", "member")
			return call{tenant, u[1].id, u[2].id, "admin", "admin", "member", nil}
		}, "", "membership.role_changed/{actor}/user/{member}/member/admin"},
		{"the same role changes nothing", func(f fixtures) call {
			tenant, u := f.members("owner", "member")
			return call{tenant, u[0].id, u[1].id, "member", "owner", "member", nil}
		}, "", ""},
		{"the last owner is not demoted", func(f fixtures) call {
			tenant, u := f.members("owner", "admin")
			return call{tenant, u[0].id, u[0].id, "admin", "owner", "owner", nil}
		}, "TN061", ""},
		{"a user who is not a member", func(f fixtures) call {
			tenant, u := f.members("owner")
			return call{tenant, u[0].id, f.user(true, false).id, "admin", "owner", "member", nil}
		}, "TN060", ""},
		{"a member of another tenant", func(f fixtures) call {
			tenant, u := f.members("owner")
			_, other := f.members("owner", "member")
			return call{tenant, u[0].id, other[1].id, "admin", "owner", "member", nil}
		}, "TN060", ""},
		{"the actor's role changed", func(f fixtures) call {
			tenant, u := f.members("owner", "admin", "member")
			return call{tenant, u[1].id, u[2].id, "admin", "owner", "member", nil}
		}, "TN062", ""},
		{"the member's role changed", func(f fixtures) call {
			tenant, u := f.members("owner", "admin", "member")
			return call{tenant, u[1].id, u[2].id, "admin", "admin", "client", nil}
		}, "TN062", ""},
		{"a suspended tenant", func(f fixtures) call {
			tenant, u := f.members("owner", "member")
			f.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, tenant)
			return call{tenant, u[0].id, u[1].id, "admin", "owner", "member", nil}
		}, "TN014", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixtures(t)
			c := tc.setup(f)
			err := execAs(t, tenantVars(c.tenant, c.actor, "tenant"), `SELECT change_member_role($1, $2, $3, $4, NULL, $5)`,
				c.member, c.role, c.actorRole, c.memberRole, c.ipPrefix)
			var want []string
			if tc.wantState != "" {
				wantSQLState(t, err, tc.wantState)
			} else if err != nil {
				t.Fatalf("change_member_role: %v", err)
			}
			if tc.wantEvent != "" {
				want = []string{strings.NewReplacer("{actor}", c.actor, "{member}", c.member).Replace(tc.wantEvent)}
			}
			if got := f.memberEvents(c.tenant); !slices.Equal(got, want) {
				t.Fatalf("events = %v, want %v", got, want)
			}
		})
	}
}

func TestFunc_RemoveMemberAndLeave(t *testing.T) {
	t.Parallel()

	t.Run("an owner removes another owner", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "owner")
		if err := execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT remove_member($1, 'owner', 'owner', $2)`,
			u[1].id, testIPPrefix); err != nil {
			t.Fatal(err)
		}
		if got := f.roleOf(tenant, u[1]); got != "<none>" {
			t.Fatalf("removed member's role = %s", got)
		}
		want := []string{"membership.removed/" + u[0].id + "/user/" + u[1].id + "/owner/" + testIPPrefix}
		if got := f.memberEvents(tenant); !slices.Equal(got, want) {
			t.Fatalf("events = %v, want %v", got, want)
		}
	})

	t.Run("the last owner is not removed", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin")
		err := execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT remove_member($1, 'owner', 'owner')`, u[0].id)
		wantSQLState(t, err, "TN061")
	})

	t.Run("removing someone who is not a member", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner")
		_, other := f.members("owner")
		err := execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT remove_member($1, 'owner', 'owner')`, other[0].id)
		wantSQLState(t, err, "TN060")
		if got := f.roleOf(tenant, u[0]); got != "owner" {
			t.Fatal("the actor's membership changed")
		}
	})

	t.Run("removing with a stale role", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin", "member")
		err := execAs(t, tenantVars(tenant, u[1].id, "tenant"), `SELECT remove_member($1, 'owner', 'member')`, u[2].id)
		wantSQLState(t, err, "TN062")
	})

	t.Run("a member leaves", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "member")
		if err := execAs(t, tenantVars(tenant, u[1].id, "tenant"), `SELECT leave_tenant($1)`, testIPPrefix); err != nil {
			t.Fatal(err)
		}
		want := []string{"membership.left/" + u[1].id + "/user/" + u[1].id + "/member/" + testIPPrefix}
		if got := f.memberEvents(tenant); !slices.Equal(got, want) {
			t.Fatalf("events = %v, want %v", got, want)
		}
	})

	t.Run("one of two owners leaves", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "owner")
		if err := execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT leave_tenant()`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the last owner cannot leave", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin")
		wantSQLState(t, execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT leave_tenant()`), "TN061")
	})

	t.Run("leaving a tenant one is not a member of", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, _ := f.members("owner")
		wantSQLState(t, execAs(t, tenantVars(tenant, f.user(true, false).id, "tenant"), `SELECT leave_tenant()`), "TN060")
	})

	t.Run("without app.tenant_id", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		_, u := f.members("owner", "owner")
		err := execAs(t, userVars(u[0].id), `SELECT remove_member($1, 'owner', 'owner')`, u[1].id)
		if state := sqlState(err); state != "42704" && state != "22P02" {
			t.Fatalf("got %v (SQLSTATE %q), want the missing variable to fail", err, state)
		}
	})
}

// The member functions act only on app.tenant_id: with tenant A's variables, a member
// of B is "not a member", whatever function is called, and B is untouched.
func TestFunc_MemberFunctionsStayInTheTenant(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	a, ua := f.members("owner")
	b, ub := f.members("owner", "member")
	for name, sql := range map[string]string{
		"change role": `SELECT change_member_role($1, 'member', 'owner', 'owner', NULL)`,
		"remove":      `SELECT remove_member($1, 'owner', 'owner')`,
	} {
		t.Run(name, func(t *testing.T) {
			wantSQLState(t, execAs(t, tenantVars(a, ua[0].id, "tenant"), sql, ub[0].id), "TN060")
		})
	}
	if f.roleOf(b, ub[0]) != "owner" || f.roleOf(b, ub[1]) != "member" || len(f.memberEvents(b)) != 0 {
		t.Fatal("tenant B changed")
	}
}

// Two owners demote each other at the same time: the second call waits on the tenant's
// row lock, then counts the owners the first one left and is refused. Without the lock
// both would count the other as owner and leave the tenant with none.
func TestFunc_MemberFunctionsLockTheTenant(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	f := newFixtures(t)
	tenant, u := f.members("owner", "owner")

	first, second := connect(t, testDB.RuntimeURL), connect(t, testDB.RuntimeURL)
	tx1 := beginWith(ctx, t, first, tenantVars(tenant, u[0].id, "tenant"))
	defer func() { _ = tx1.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx1.Exec(ctx, `SELECT change_member_role($1, 'admin', 'owner', 'owner', NULL)`, u[1].id); err != nil {
		t.Fatalf("first demotion: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		tx2 := beginWith(ctx, t, second, tenantVars(tenant, u[1].id, "tenant"))
		_, err := tx2.Exec(ctx, `SELECT change_member_role($1, 'admin', 'owner', 'owner', NULL)`, u[0].id)
		_ = tx2.Rollback(context.WithoutCancel(ctx))
		done <- err
	}()
	waitForLock(t, f, second.PgConn().PID(), "second demotion")
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit first demotion: %v", err)
	}

	wantSQLState(t, <-done, "TN061")
	if f.roleOf(tenant, u[0]) != "owner" || f.roleOf(tenant, u[1]) != "admin" {
		t.Fatalf("roles = %s, %s; want owner, admin", f.roleOf(tenant, u[0]), f.roleOf(tenant, u[1]))
	}
}

// beginWith opens a transaction on conn with vars set; the caller ends it.
func beginWith(ctx context.Context, t *testing.T, conn *pgx.Conn, vars []setting) pgx.Tx {
	t.Helper()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Errorf("begin: %v", err)
		return nil
	}
	for _, v := range vars {
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, v.name, v.value); err != nil {
			t.Errorf("set %s: %v", v.name, err)
		}
	}
	return tx
}

// waitForLock returns once the backend pid waits on a lock, and fails the test if it
// never does within 10 seconds.
func waitForLock(t *testing.T, f fixtures, pid uint32, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for f.scalar(`SELECT coalesce(wait_event_type, '') FROM pg_stat_activity WHERE pid = $1`, pid) != "Lock" {
		if time.Now().After(deadline) {
			t.Fatalf("%s never waited on a lock", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
