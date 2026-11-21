package b_test

import (
	"testing"

	_ "chaskicode.dev/saas/app"
	_ "chaskicode.dev/saas/platform/a/adapters/postgres"
)

func TestImportsAnotherModulesAdapter(t *testing.T) { t.Log("violations via test imports") }
