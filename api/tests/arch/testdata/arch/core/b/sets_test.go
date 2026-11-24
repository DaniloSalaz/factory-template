package b_test

import (
	"testing"

	_ "chaskicode.dev/saas/tests/sets"
)

// Violation: the helper drags the app into the core's test binary.
func TestUsesAHelperThatImportsTheApp(t *testing.T) { t.Log("core reaches app indirectly") }
