package foo

import "os"

// Unexplained names the linter but gives no reason.
func Unexplained() ([]byte, error) {
	return os.ReadFile("a") //nolint:wrapcheck
}
