// Package migrationsets holds the migration sets `api migrate` applies, so the tests of
// the whole schema (api/tests/db) run on the database a deployment builds. It is apart
// from testkit because it imports the app: the tests of core/ use testkit and must
// not pull the app in, not even through a helper ([core-imports-app]).
package migrationsets

import (
	appmigrations "chaskicode.dev/saas/app/migrations"
	"chaskicode.dev/saas/core/kit/db"
	coremigrations "chaskicode.dev/saas/core/migrations"
)

// All returns the core's and the app's sets in the order migrationSets (api/migrate.go)
// applies them: the core's first, because the app's may use its tables.
func All() []db.MigrationSet {
	return []db.MigrationSet{coremigrations.Set(), appmigrations.Set()}
}
