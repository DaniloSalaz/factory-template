package a

import (
	"testing"

	_ "chaskicode.dev/saas/tests/testkit"
)

func TestUsesTestkitInternally(t *testing.T) { t.Log("internal tests may import tests/ too") }
