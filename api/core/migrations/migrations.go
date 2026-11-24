// Package migrations embeds the platform's goose migrations (NNNNN_name.sql) in the
// binary, so a deployment applies exactly the ones it was built with (`api migrate`)
// before it starts the new version. They run before the app's (app/migrations) and
// never reference anything the app creates: on an empty database the app's tables do
// not exist yet. A migration merged into main is immutable; changes go in a new one.
package migrations

import (
	"embed"

	"chaskicode.dev/saas/platform/kit/db"
)

//go:embed *.sql
var files embed.FS

// Set returns the platform's migrations. They keep goose's default version table, the
// one every database had before the app got its own set, so an existing database sees
// no change.
func Set() db.MigrationSet {
	return db.MigrationSet{Name: "platform", FS: files, Table: "goose_db_version"}
}
