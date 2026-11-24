// Package apperr violates the rule: it imports a non-stdlib package.
package apperr

import (
	"strings"

	"chaskicode.dev/saas/core/kit/logx"
)

// Code mixes an allowed (strings) and a forbidden (logx) import.
func Code() string { return strings.ToUpper(logx.Key) }
