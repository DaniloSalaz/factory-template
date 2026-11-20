// Package dbtest starts a disposable Postgres 18 for integration tests, bootstrapped and
// migrated exactly like a real environment: infra/db/initdb.sh and bootstrap.sql run as
// init scripts, then the migrations run as app_owner. Only tests import it.
package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver used by goose
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Credentials of the throwaway container; they never leave the test process.
const (
	dbName        = "factory"
	superUser     = "factory"
	superPassword = "factory"
	ownerPassword = "owner-test"
	runtimePass   = "runtime-test"
	adminPassword = "admin-test"
)

// DB is a running, migrated database with a connection URL per role.
type DB struct {
	Container  *tcpostgres.PostgresContainer
	SuperURL   string // container superuser: test fixtures only
	OwnerURL   string // app_owner: migrations
	RuntimeURL string // app_runtime: what the application uses
	AdminURL   string // app_admin: administration commands
}

// Start runs a database for the duration of t.
func Start(t testing.TB) *DB {
	t.Helper()

	db, err := New(t.Context())
	if db != nil {
		testcontainers.CleanupContainer(t, db.Container)
	}
	if err != nil {
		t.Fatalf("start test database: %v", err)
	}
	return db
}

// New runs a database until the returned container is terminated. TestMain uses it to
// share one database between the tests of a package.
func New(ctx context.Context) (*DB, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}

	ctr, err := tcpostgres.Run(ctx, "postgres:18",
		tcpostgres.WithDatabase(dbName),
		tcpostgres.WithUsername(superUser),
		tcpostgres.WithPassword(superPassword),
		testcontainers.WithEnv(map[string]string{
			"APP_OWNER_PASSWORD":   ownerPassword,
			"APP_RUNTIME_PASSWORD": runtimePass,
			"APP_ADMIN_PASSWORD":   adminPassword,
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(root, "infra", "db", "bootstrap.sql"),
			ContainerFilePath: "/bootstrap/bootstrap.sql",
			FileMode:          0o644,
		}),
		tcpostgres.WithInitScripts(filepath.Join(root, "infra", "db", "initdb.sh")),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("run postgres container: %w", err)
	}
	db := &DB{Container: ctr}

	superURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return db, fmt.Errorf("connection string: %w", err)
	}
	db.SuperURL = superURL
	if db.OwnerURL, err = withUser(superURL, "app_owner", ownerPassword); err != nil {
		return db, err
	}
	if db.RuntimeURL, err = withUser(superURL, "app_runtime", runtimePass); err != nil {
		return db, err
	}
	if db.AdminURL, err = withUser(superURL, "app_admin", adminPassword); err != nil {
		return db, err
	}

	if err := migrate(ctx, db.OwnerURL, filepath.Join(root, "migrations")); err != nil {
		return db, err
	}
	return db, nil
}

func migrate(ctx context.Context, ownerURL, dir string) error {
	sqlDB, err := sql.Open("pgx", ownerURL)
	if err != nil {
		return fmt.Errorf("open migrations connection: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS(dir))
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func withUser(rawURL, user, password string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse connection string: %w", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

// repoRoot locates the repository from this file, so tests in any package find
// infra/db and migrations/.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("locate dbtest source file")
	}
	// file is <root>/api/internal/platform/db/dbtest/dbtest.go
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."), nil
}
