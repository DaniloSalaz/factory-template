// Package app is the app's hook: it may use kit, a platform module's root and its own
// modules, never a platform module's interior.
package app

import (
	_ "chaskicode.dev/saas/app/modules/c"
	_ "chaskicode.dev/saas/core/a"
	_ "chaskicode.dev/saas/core/a/usecases"
	_ "chaskicode.dev/saas/core/kit/authz"
)
