// Package b imports module a correctly and incorrectly, and an app module's root, which
// module boundaries allow and the platform-app hook does not.
package b

import (
	_ "chaskicode.dev/saas/app/modules/c"
	_ "chaskicode.dev/saas/core/a"
	_ "chaskicode.dev/saas/core/a/domain"
)
