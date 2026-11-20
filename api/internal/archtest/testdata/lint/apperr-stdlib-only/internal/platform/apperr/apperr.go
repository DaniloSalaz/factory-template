// Package apperr violates the rule: it imports a non-stdlib package.
package apperr

import (
	"strings"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
)

// Code mixes an allowed (strings) and a forbidden (logx) import.
func Code() string { return strings.ToUpper(logx.Key) }
