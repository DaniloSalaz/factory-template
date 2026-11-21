package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"chaskicode.dev/saas/platform/authctx"
	"chaskicode.dev/saas/platform/db"
)

const (
	guardUser  = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	guardOther = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5d"
	guardTnt   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
)

// The identity checks happen before any connection is taken: a DB without a pool proves
// it, since reaching the pool would panic.
func TestTxGuards(t *testing.T) {
	t.Parallel()

	user := func(t *testing.T, id string) authctx.User {
		t.Helper()
		u, err := authctx.NewUser(id, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	principal := func(t *testing.T, id string) authctx.Principal {
		t.Helper()
		p, err := authctx.NewPrincipal(id, guardTnt, "member")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	d := db.New(nil)
	tests := []struct {
		name string
		ctx  func(t *testing.T) context.Context
		run  func(ctx context.Context, fn db.TxFunc) error
		want error
	}{
		{"tenant tx without principal", func(*testing.T) context.Context { return context.Background() }, d.WithTenantTx, db.ErrNoPrincipal},
		{"tenant tx with only a user", func(t *testing.T) context.Context {
			return authctx.WithUser(context.Background(), user(t, guardUser))
		}, d.WithTenantTx, db.ErrNoPrincipal},
		{"tenant tx with a different user", func(t *testing.T) context.Context {
			return authctx.WithPrincipal(authctx.WithUser(context.Background(), user(t, guardOther)), principal(t, guardUser))
		}, d.WithTenantTx, db.ErrIdentityMismatch},
		{"user tx without user", func(*testing.T) context.Context { return context.Background() }, d.WithUserTx, db.ErrNoUser},
		{"user tx with only a principal", func(t *testing.T) context.Context {
			return authctx.WithPrincipal(context.Background(), principal(t, guardUser))
		}, d.WithUserTx, db.ErrNoUser},
		{"user tx with a different principal", func(t *testing.T) context.Context {
			return authctx.WithPrincipal(authctx.WithUser(context.Background(), user(t, guardUser)), principal(t, guardOther))
		}, d.WithUserTx, db.ErrIdentityMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			called := false
			err := tc.run(tc.ctx(t), func(context.Context, db.Querier) error {
				called = true
				return nil
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if called {
				t.Fatal("fn ran although the transaction was refused")
			}
		})
	}
}
