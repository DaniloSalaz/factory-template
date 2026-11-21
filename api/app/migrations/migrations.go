// Package migrations embeds the app's goose migrations (NNNNN_name.sql, created with
// `mise run migration:new <name>`). `api migrate` applies them after the platform's,
// with their own version table, so their numbering never collides with the template's.
// They may use the platform's tables; the platform never uses theirs. A migration
// merged into main is immutable; changes go in a new one.
package migrations

import (
	"embed"

	"chaskicode.dev/saas/platform/kit/db"
)

// The pattern is * and not *.sql because the template has no app migration and
// "*.sql" does not compile without a match. goose reads only files with a numeric
// prefix, so this file is ignored.
//
//go:embed *
var files embed.FS

// Set returns the app's migrations. AllowEmpty because the template has none: db.Migrate
// then applies nothing and only creates the version table.
func Set() db.MigrationSet {
	return db.MigrationSet{Name: "app", FS: files, Table: "goose_app_db_version", AllowEmpty: true}
}
