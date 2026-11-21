// Package c is an app module: it may use a platform module's root and its own interior,
// never a platform module's interior.
package c

import (
	_ "chaskicode.dev/saas/app/modules/c/adapters/x"
	_ "chaskicode.dev/saas/platform/a"
	_ "chaskicode.dev/saas/platform/a/domain"
)
