// Package migrationsets holds the migration sets `api migrate` applies, so the tests of
// the whole schema (api/tests/db) run on the database a deployment builds. It is apart
// from testkit because it imports the app: the tests of platform/ use testkit and must
// not pull the app in, not even through a helper ([platform-imports-app]).
package migrationsets

import (
	appmigrations "chaskicode.dev/saas/app/migrations"
	"chaskicode.dev/saas/platform/kit/db"
	platformmigrations "chaskicode.dev/saas/platform/migrations"
)

// All returns the platform's and the app's sets in the order migrationSets (api/migrate.go)
// applies them: the platform's first, because the app's may use its tables.
func All() []db.MigrationSet {
	return []db.MigrationSet{platformmigrations.Set(), appmigrations.Set()}
}
