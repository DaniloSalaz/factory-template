package b_test

import (
	"testing"

	_ "chaskicode.dev/saas/modules/a/adapters/postgres"
)

func TestImportsAnotherModulesAdapter(t *testing.T) { t.Log("violation via test imports") }
