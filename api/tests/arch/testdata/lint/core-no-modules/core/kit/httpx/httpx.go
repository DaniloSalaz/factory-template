// Package httpx violates the rule: kit imports a platform module.
package httpx

import "chaskicode.dev/saas/core/foo"

// Name leaks module knowledge into kit.
const Name = foo.Name
