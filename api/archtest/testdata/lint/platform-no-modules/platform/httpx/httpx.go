// Package httpx violates the rule: platform imports a module.
package httpx

import "chaskicode.dev/saas/modules/foo"

// Name leaks module knowledge into platform.
const Name = foo.Name
