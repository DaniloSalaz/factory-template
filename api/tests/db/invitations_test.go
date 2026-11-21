//go:build integration

package db

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Invitations in the database (migration 00006, design 5.6): the automatic revocation of
// the member functions and accept_invitation under concurrency.

// invite inserts a pending invitation from inviter offering role and returns its id.
func (f fixtures) invite(tenant string, inviter user, role, extra string) string {
	f.t.Helper()
	id := f.scalar(`INSERT INTO invitations (tenant_id, email, role, token_hash, expires_at, invited_by)
		VALUES ($1, replace(uuidv7()::text, '-', '') || '@example.com', $2, sha256(uuidv7()::text::bytea),
		        now() + interval '7 days', $3)
		RETURNING id::text`, tenant, role, inviter.id)
	if extra != "" {
		f.exec(`UPDATE invitations SET `+extra+` WHERE id = $1`, id)
	}
	return id
}

func (f fixtures) revoked(id string) bool {
	f.t.Helper()
	return f.scalar(`SELECT (revoked_at IS NOT NULL)::text FROM invitations WHERE id = $1`, id) == "true"
}

// revocations returns the tenant's invitation.revoked events as target/reason/role.
func (f fixtures) revocations(tenant string) []string {
	f.t.Helper()
	rows, err := f.super.Query(f.t.Context(), `
		SELECT concat_ws('/', target_id, metadata->>'reason', metadata->>'role')
		FROM audit_log WHERE tenant_id = $1 AND action = 'invitation.revoked' ORDER BY target_id`, tenant)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestFunc_MemberChangesRevokeTheirInvitations(t *testing.T) {
	t.Parallel()

	t.Run("demotion keeps what the new role may still offer", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin")
		owner, admin := u[0], u[1]
		asMember := f.invite(tenant, admin, "member", "")
		asAdmin := f.invite(tenant, admin, "admin", "")
		expired := f.invite(tenant, admin, "admin", "expires_at = now() - interval '1 day'")
		accepted := f.invite(tenant, admin, "admin", "accepted_at = now()")
		alreadyRevoked := f.invite(tenant, admin, "admin", "revoked_at = now()")
		byOwner := f.invite(tenant, owner, "admin", "")

		err := execAs(t, tenantVars(tenant, owner.id, "tenant"),
			`SELECT change_member_role($1, 'member', 'owner', 'admin', ARRAY['member'], $2)`, admin.id, testIPPrefix)
		if err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]bool{
			asMember: false, asAdmin: true, expired: true, accepted: false, alreadyRevoked: true, byOwner: false,
		} {
			if got := f.revoked(id); got != want {
				t.Errorf("invitation %s revoked = %v, want %v", id, got, want)
			}
		}
		want := []string{asAdmin + "/inviter_role_changed/admin", expired + "/inviter_role_changed/admin"}
		slices.Sort(want)
		if got := f.revocations(tenant); !slices.Equal(got, want) {
			t.Fatalf("revocations = %v, want %v", got, want)
		}
		// The audit never holds the invitee's email.
		if n := f.scalar(`SELECT count(*)::text FROM audit_log a JOIN invitations i ON i.id::text = a.target_id
			WHERE a.tenant_id = $1 AND a.metadata::text LIKE '%' || i.email::text || '%'`, tenant); n != "0" {
			t.Fatalf("%s audit events hold an invitee's email", n)
		}

		// Without members:invite, Go offers nothing: every pending invitation goes.
		if err := execAs(t, tenantVars(tenant, owner.id, "tenant"),
			`SELECT change_member_role($1, 'client', 'owner', 'member', NULL)`, admin.id); err != nil {
			t.Fatal(err)
		}
		if !f.revoked(asMember) {
			t.Fatal("the last pending invitation survived losing members:invite")
		}
	})

	t.Run("a change that is no change revokes nothing", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin")
		inv := f.invite(tenant, u[1], "admin", "")
		if err := execAs(t, tenantVars(tenant, u[0].id, "tenant"),
			`SELECT change_member_role($1, 'admin', 'owner', 'admin', NULL)`, u[1].id); err != nil {
			t.Fatal(err)
		}
		if f.revoked(inv) {
			t.Fatal("setting the same role revoked an invitation")
		}
	})

	t.Run("removal revokes everything the member issued", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin")
		a, b := f.invite(tenant, u[1], "member", ""), f.invite(tenant, u[1], "admin", "")
		if err := execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT remove_member($1, 'owner', 'admin')`, u[1].id); err != nil {
			t.Fatal(err)
		}
		want := []string{a + "/inviter_removed/member", b + "/inviter_removed/admin"}
		slices.Sort(want)
		if got := f.revocations(tenant); !slices.Equal(got, want) {
			t.Fatalf("revocations = %v, want %v", got, want)
		}
	})

	t.Run("leaving revokes everything the member issued", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin")
		a := f.invite(tenant, u[1], "member", "")
		if err := execAs(t, tenantVars(tenant, u[1].id, "tenant"), `SELECT leave_tenant()`); err != nil {
			t.Fatal(err)
		}
		if got := f.revocations(tenant); !slices.Equal(got, []string{a + "/inviter_left/member"}) {
			t.Fatalf("revocations = %v", got)
		}
	})

	t.Run("a refused change revokes nothing", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner")
		inv := f.invite(tenant, u[0], "admin", "")
		wantSQLState(t, execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT leave_tenant()`), "TN061")
		if f.revoked(inv) {
			t.Fatal("the last owner's failed leave revoked an invitation")
		}
	})

	t.Run("another tenant's invitations are untouched", func(t *testing.T) {
		t.Parallel()
		f := newFixtures(t)
		tenant, u := f.members("owner", "admin")
		other := f.tenant("active")
		f.member(other, u[1], "admin")
		elsewhere := f.invite(other, u[1], "member", "")
		if err := execAs(t, tenantVars(tenant, u[0].id, "tenant"), `SELECT remove_member($1, 'owner', 'admin')`, u[1].id); err != nil {
			t.Fatal(err)
		}
		if f.revoked(elsewhere) {
			t.Fatal("removing a member of A revoked their invitation in B")
		}
	})
}

// Fifty times: two transactions accept the same token at once. Exactly one creates the
// membership; the other gets TN011 (already accepted) or TN016 (already a member).
func TestFunc_AcceptInvitationRaces(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	conns := []*pgx.Conn{connect(t, testDB.RuntimeURL), connect(t, testDB.RuntimeURL)}
	for i := range 50 {
		u := f.user(true, false)
		tenant := f.tenant("active")
		token := f.invitation(tenant, u.email, "")

		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for j := range errs {
			conn := conns[j]
			wg.Go(func() {
				<-start
				errs[j] = acceptOnce(t.Context(), conn, u.id, token)
			})
		}
		close(start)
		wg.Wait()

		ok := 0
		for _, err := range errs {
			switch state := sqlState(err); {
			case err == nil:
				ok++
			case state == "TN011" || state == "TN016":
			default:
				t.Fatalf("round %d: accept failed with %v", i, err)
			}
		}
		if n := f.scalar(`SELECT count(*)::text FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenant, u.id); ok != 1 || n != "1" {
			t.Fatalf("round %d: %d accepts succeeded and %s memberships exist, want 1 and 1", i, ok, n)
		}
	}
}

// An accept and the demotion of the inviter, at the same time, a hundred times. They
// must never deadlock (40P01), and the outcome is one of the two orders: accepted and
// not revoked, or revoked and refused (TN012).
func TestFunc_AcceptAndInviterDemotionNeverDeadlock(t *testing.T) {
	t.Parallel()

	f := newFixtures(t)
	acceptConn, demoteConn := connect(t, testDB.RuntimeURL), connect(t, testDB.RuntimeURL)
	for i := range 100 {
		tenant, u := f.members("owner", "admin")
		invitee := f.user(true, false)
		token := f.invitation(tenant, invitee.email, "")
		f.exec(`UPDATE invitations SET invited_by = $2, role = 'admin' WHERE token_hash = $1`, token, u[1].id)

		var acceptErr, demoteErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() {
			<-start
			acceptErr = acceptOnce(t.Context(), acceptConn, invitee.id, token)
		})
		wg.Go(func() {
			<-start
			demoteErr = inTxCtx(t.Context(), demoteConn, tenantVars(tenant, u[0].id, "tenant"),
				`SELECT change_member_role($1, 'member', 'owner', 'admin', ARRAY['member'])`, u[1].id)
		})
		close(start)
		wg.Wait()

		if demoteErr != nil {
			t.Fatalf("round %d: demotion: %v", i, demoteErr)
		}
		accepted := f.scalar(`SELECT (accepted_at IS NOT NULL)::text FROM invitations WHERE token_hash = $1`, token) == "true"
		switch {
		case acceptErr == nil && accepted:
		case sqlState(acceptErr) == "TN012" && !accepted:
		default:
			t.Fatalf("round %d: accept = %v, accepted = %v", i, acceptErr, accepted)
		}
	}
}

// acceptOnce runs accept_invitation as userID in its own transaction on conn.
func acceptOnce(ctx context.Context, conn *pgx.Conn, userID string, token []byte) error {
	return inTxCtx(ctx, conn, userVars(userID), `SELECT * FROM accept_invitation($1)`, token)
}

// inTxCtx is inTx without the testing.T, for goroutines: it runs one statement and
// commits, or rolls back and returns the error.
func inTxCtx(ctx context.Context, conn *pgx.Conn, vars []setting, sql string, args ...any) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, v := range vars {
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, v.name, v.value); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
