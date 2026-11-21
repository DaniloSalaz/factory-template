// Package httpx violates the rule: platform imports a module.
package httpx

import "github.com/DaniloSalaz/factory-template/api/internal/modules/foo"

// Name leaks module knowledge into platform.
const Name = foo.Name
