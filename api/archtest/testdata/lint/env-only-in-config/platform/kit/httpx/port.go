// Package httpx violates the rule: it reads the environment directly.
package httpx

import "os"

// Port must come from config.Config instead.
func Port() string { return os.Getenv("HTTP_PORT") }
