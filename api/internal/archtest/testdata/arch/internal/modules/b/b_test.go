package b_test

import (
	"testing"

	_ "github.com/DaniloSalaz/factory-template/api/internal/modules/a/adapters/postgres"
)

func TestImportsAnotherModulesAdapter(t *testing.T) { t.Log("violation via test imports") }
