// Package http breaks the rule: a handler checks permissions itself.
package http

import "chaskicode.dev/saas/platform/kit/authz"

// Perm violates the rule.
const Perm = authz.Perm
