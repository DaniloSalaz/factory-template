// Package dbtest starts a disposable Postgres 18 for integration tests, bootstrapped and
// migrated exactly like a real environment: infra/db/initdb.sh and bootstrap.sql run as
// init scripts, then the migration sets the caller passes run as app_owner, through the
// same db.Migrate as `api migrate`. It imports no migrations itself: platform tests pass
// the platform's, the tests in package main the platform's and the app's. Only tests
// import it.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	platformdb "chaskicode.dev/saas/platform/kit/db"
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

// Start runs a database for the duration of t, migrated with sets (see New).
func Start(t testing.TB, sets ...platformdb.MigrationSet) *DB {
	t.Helper()

	db, err := New(t.Context(), sets...)
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
//
// sets are applied as app_owner in the order given, each with its own goose version
// table, so test-only migrations (example tables, say) go after the real ones without
// sharing their numbering.
func New(ctx context.Context, sets ...platformdb.MigrationSet) (*DB, error) {
	if len(sets) == 0 {
		return nil, errors.New("dbtest: no migration set; pass at least the platform's")
	}
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

	if _, err := platformdb.Migrate(ctx, db.OwnerURL, sets); err != nil {
		return db, fmt.Errorf("migrate test database: %w", err)
	}
	return db, nil
}

// PlatformFromSource is the platform's migration set read from api/platform/migrations
// on disk: the same name, version table and files as migrations.Set(), which tests in
// platform/kit cannot import ([platform-no-modules]). A test in platform/migrations
// keeps the two identical.
func PlatformFromSource() platformdb.MigrationSet {
	root, err := repoRoot()
	if err != nil {
		// Only when the runtime cannot locate this file. The directory is then wrong and
		// Migrate fails: the set has no migrations and does not allow being empty.
		root = "."
	}
	return platformdb.MigrationSet{
		Name:  "platform",
		FS:    os.DirFS(filepath.Join(root, "api", "platform", "migrations")),
		Table: "goose_db_version",
	}
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
// infra/db.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("locate dbtest source file")
	}
	// file is <root>/api/platform/kit/db/dbtest/dbtest.go
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."), nil
}
