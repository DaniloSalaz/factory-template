// Package b imports module a correctly and incorrectly.
package b

import (
	_ "chaskicode.dev/saas/modules/a"
	_ "chaskicode.dev/saas/modules/a/domain"
)
