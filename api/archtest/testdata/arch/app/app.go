// Package app is the app's hook: it may use kit, a platform module's root and its own
// modules, never a platform module's interior.
package app

import (
	_ "chaskicode.dev/saas/app/modules/c"
	_ "chaskicode.dev/saas/platform/a"
	_ "chaskicode.dev/saas/platform/a/usecases"
	_ "chaskicode.dev/saas/platform/kit/authz"
)
