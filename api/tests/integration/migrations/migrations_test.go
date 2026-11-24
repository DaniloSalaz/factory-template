//go:build integration

package migrations_test

import (
	"testing"

	"github.com/testcontainers/testcontainers-go"

	"chaskicode.dev/saas/core/migrations"
	"chaskicode.dev/saas/tests/testkit"
)

// The core's migrations apply on their own to an empty database: they run before the
// app's, so anything they referenced from the app would not exist yet in a new
// environment. An app's migrations may use the core's tables, never the reverse.
func TestCoreMigrationsApplyAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	pg, err := testkit.New(t.Context(), migrations.Set())
	if pg != nil {
		testcontainers.CleanupContainer(t, pg.Container)
	}
	if err != nil {
		t.Fatalf("[core-migrations-alone] the core's migrations do not apply on an empty database "+
			"without the app's: %v\nCore migrations must apply on their own: they never reference "+
			"the app's tables, functions or types (AGENTS.md). Move that part to an app migration.", err)
	}
}
