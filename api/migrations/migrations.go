// Package migrations embeds the goose migrations (NNNNN_name.sql) in the binary, so a
// deployment applies exactly the ones it was built with (`api migrate`) before it starts
// the new version. A migration merged into main is immutable; changes go in a new one.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the migrations, at the root of the file system.
func FS() fs.FS { return files }
