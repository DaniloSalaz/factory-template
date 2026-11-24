// Package c is an app module: it may use a core module's root and its own interior,
// never a core module's interior.
package c

import (
	_ "chaskicode.dev/saas/app/modules/c/adapters/x"
	_ "chaskicode.dev/saas/core/a"
	_ "chaskicode.dev/saas/core/a/domain"
)
