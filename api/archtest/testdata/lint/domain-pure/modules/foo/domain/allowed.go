// Package domain mixes allowed and forbidden imports.
package domain

import (
	"strings"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
)

// Allowed uses stdlib and apperr only.
func Allowed() string { return strings.ToUpper(apperr.Code) }
