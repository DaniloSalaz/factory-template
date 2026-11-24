// Package authctx violates the rule: it imports a non-stdlib package.
package authctx

import (
	"strings"

	"chaskicode.dev/saas/core/kit/logx"
)

// Key mixes an allowed (strings) and a forbidden (logx) import.
func Key() string { return strings.ToUpper(logx.Key) }
