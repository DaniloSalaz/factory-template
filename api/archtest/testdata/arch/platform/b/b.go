// Package b imports module a correctly and incorrectly.
package b

import (
	_ "chaskicode.dev/saas/platform/a"
	_ "chaskicode.dev/saas/platform/a/domain"
)
