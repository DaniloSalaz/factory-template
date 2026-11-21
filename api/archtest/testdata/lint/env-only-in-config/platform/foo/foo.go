// Package foo violates the rule from a module.
package foo

import "os"

// Env must come from config.Config instead.
func Env() []string { return os.Environ() }
