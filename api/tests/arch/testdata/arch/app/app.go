// Package app is the app's hook: it may use kit, a core module's root and its own
// modules, never a core module's interior.
package app

import (
	_ "chaskicode.dev/saas/app/modules/c"
	_ "chaskicode.dev/saas/core/a"
	_ "chaskicode.dev/saas/core/a/usecases"
	_ "chaskicode.dev/saas/core/kit/authz"
)
