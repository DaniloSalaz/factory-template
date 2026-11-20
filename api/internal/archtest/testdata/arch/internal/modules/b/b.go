// Package b imports module a correctly and incorrectly.
package b

import (
	_ "github.com/DaniloSalaz/factory-template/api/internal/modules/a"
	_ "github.com/DaniloSalaz/factory-template/api/internal/modules/a/domain"
)
