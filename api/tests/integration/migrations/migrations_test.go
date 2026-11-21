//go:build integration

package migrations_test

import (
	"testing"

	"github.com/testcontainers/testcontainers-go"

	"chaskicode.dev/saas/platform/migrations"
	"chaskicode.dev/saas/tests/testkit"
)

// The platform's migrations apply on their own to an empty database: they run before the
// app's, so anything they referenced from the app would not exist yet in a new
// environment. An app's migrations may use the platform's tables, never the reverse.
func TestPlatformMigrationsApplyAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	pg, err := testkit.New(t.Context(), migrations.Set())
	if pg != nil {
		testcontainers.CleanupContainer(t, pg.Container)
	}
	if err != nil {
		t.Fatalf("[platform-migrations-alone] the platform's migrations do not apply on an empty database "+
			"without the app's: %v\nPlatform migrations must apply on their own: they never reference "+
			"the app's tables, functions or types (AGENTS.md). Move that part to an app migration.", err)
	}
}
