package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"chaskicode.dev/saas/platform/authctx"
)

// The transaction helpers refuse to run, before taking a connection, when the context
// lacks the identity they need or is already inside one of them.
var (
	ErrNoPrincipal      = errors.New("db: WithTenantTx needs an authctx.Principal in the context")
	ErrNoUser           = errors.New("db: WithUserTx needs an authctx.User in the context")
	ErrIdentityMismatch = errors.New("db: the context's authctx.User and authctx.Principal are different users")
	ErrNestedTx         = errors.New("db: transaction helpers cannot be nested; use the Querier you were given")
)

// Querier is what a transaction function receives: the statements of the transaction
// and nothing that could end it or reach its connection. sqlc's generated DBTX is a
// subset, so queries are built with <module>db.New(q).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// TxFunc is the body of a transaction. It must run its statements on q and pass ctx on:
// ctx is how a nested helper call is detected.
type TxFunc func(ctx context.Context, q Querier) error

// DB runs the three kinds of transaction of the design (section 5.3). They are the only
// code that sets the RLS variables, always with set_config(..., true), so the values die
// with the transaction and a pooled connection never carries them into the next one.
type DB struct {
	pool *pgxpool.Pool
}

// New wraps a pool, which must connect as app_runtime.
func New(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool}
}

// WithTenantTx runs fn with the RLS variables of the context's Principal: app.tenant_id,
// app.user_id and app.scope. It is the only way to reach a tenant's data.
func (d *DB) WithTenantTx(ctx context.Context, fn TxFunc) error {
	if inTx(ctx) {
		return ErrNestedTx
	}
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok {
		return ErrNoPrincipal
	}
	if u, ok := authctx.UserFrom(ctx); ok && u.ID() != p.UserID() {
		return ErrIdentityMismatch
	}
	// The ::uuid casts let Postgres reject a malformed id too, and the three values are
	// set in one round trip so no statement ever runs with only some of them.
	return d.run(ctx, fn, `SELECT set_config('app.tenant_id', $1::uuid::text, true),
		set_config('app.user_id', $2::uuid::text, true),
		set_config('app.scope', $3, true)`,
		p.TenantID(), p.UserID(), string(p.Scope()))
}

// WithUserTx runs fn with only app.user_id, from the context's User: flows of a user
// without a tenant (the tenant middleware, /me, POST /tenants, accepting an invitation).
// Tables under RLS fail; only SECURITY DEFINER functions and identity tables work.
func (d *DB) WithUserTx(ctx context.Context, fn TxFunc) error {
	if inTx(ctx) {
		return ErrNestedTx
	}
	u, ok := authctx.UserFrom(ctx)
	if !ok {
		return ErrNoUser
	}
	if p, ok := authctx.PrincipalFrom(ctx); ok && p.UserID() != u.ID() {
		return ErrIdentityMismatch
	}
	return d.run(ctx, fn, `SELECT set_config('app.user_id', $1::uuid::text, true)`, u.ID())
}

// WithAnonTx runs fn without any RLS variable: login, the callback and audit events
// without a user. Only SECURITY DEFINER functions and identity tables work.
func (d *DB) WithAnonTx(ctx context.Context, fn TxFunc) error {
	if inTx(ctx) {
		return ErrNestedTx
	}
	return d.run(ctx, fn, "")
}

// run commits when fn returns nil and rolls back otherwise. The deferred rollback also
// covers a panic, which then keeps unwinding, and runtime.Goexit; after a commit it is
// a no-op.
func (d *DB) run(ctx context.Context, fn TxFunc, setVars string, args ...any) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if isIndependent(ctx) {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, independentLockTimeout); err != nil {
			return fmt.Errorf("set lock timeout: %w", err)
		}
	}
	if setVars != "" {
		if _, err := tx.Exec(ctx, setVars, args...); err != nil {
			return fmt.Errorf("set rls variables: %w", err)
		}
	}
	// Inside fn, nesting is refused again, also in an independent transaction.
	fnCtx := context.WithValue(context.WithValue(ctx, txKey{}, struct{}{}), independentKey{}, false)
	if err := fn(fnCtx, querier{tx: tx}); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback transaction: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

type txKey struct{}

type independentKey struct{}

// independentLockTimeout bounds how long an independent transaction waits for a lock.
// The caller's transaction stays open meanwhile, so a lock it holds (a row it locked
// FOR UPDATE that a foreign key of the write needs) would otherwise wait forever: the
// independent write fails instead, and its caller decides (authz denies anyway).
const independentLockTimeout = "2s"

// Independent returns a context in which the next transaction helper opens a new
// transaction on its own pool connection, even when ctx is already inside one, and
// commits it whatever happens to the outer one. It is for writes that must survive the
// caller's rollback, such as the audit of a denied permission. It covers only that next
// helper: inside it, nesting is refused again. The outer transaction keeps its
// connection meanwhile, so the pool must have a spare one.
func Independent(ctx context.Context) context.Context {
	return context.WithValue(ctx, independentKey{}, true)
}

func inTx(ctx context.Context) bool {
	independent, _ := ctx.Value(independentKey{}).(bool)
	return ctx.Value(txKey{}) != nil && !independent
}

func isIndependent(ctx context.Context) bool {
	independent, _ := ctx.Value(independentKey{}).(bool)
	return independent
}

// querier hides the pgx.Tx behind Querier: a type assertion cannot get it back to
// commit, roll back or reach the connection.
type querier struct {
	tx pgx.Tx
}

// The methods are a transparent pass-through; wrapping would only repeat the caller's
// own context.

func (q querier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return q.tx.Exec(ctx, sql, args...) //nolint:wrapcheck // transparent pass-through
}

func (q querier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.tx.Query(ctx, sql, args...) //nolint:wrapcheck // transparent pass-through
}

func (q querier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q.tx.QueryRow(ctx, sql, args...)
}

func (q querier) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	return q.tx.CopyFrom(ctx, table, columns, src) //nolint:wrapcheck // transparent pass-through
}

func (q querier) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return q.tx.SendBatch(ctx, b)
}
