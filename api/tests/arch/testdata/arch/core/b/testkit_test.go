package b_test

import (
	"testing"

	_ "chaskicode.dev/saas/tests/testkit"
)

func TestUsesTestkit(t *testing.T) { t.Log("tests may import tests/") }
