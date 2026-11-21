// Package http breaks the rule: a handler checks permissions itself.
package http

import "github.com/DaniloSalaz/factory-template/api/internal/platform/authz"

// Perm violates the rule.
const Perm = authz.Perm
