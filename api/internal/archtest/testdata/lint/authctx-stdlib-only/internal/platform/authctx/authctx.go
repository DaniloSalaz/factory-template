// Package authctx violates the rule: it imports a non-stdlib package.
package authctx

import (
	"strings"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
)

// Key mixes an allowed (strings) and a forbidden (logx) import.
func Key() string { return strings.ToUpper(logx.Key) }
