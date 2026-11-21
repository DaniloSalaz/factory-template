// Package usecases may use its own module's interior, never an app module's.
package usecases

import (
	_ "chaskicode.dev/saas/app/modules/c/adapters/x"
	_ "chaskicode.dev/saas/platform/a/domain"
)
