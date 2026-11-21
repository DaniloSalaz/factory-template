package foo

import "os"

// Explained names the linter and gives a reason.
func Explained() ([]byte, error) {
	return os.ReadFile("c") //nolint:wrapcheck // the caller adds the context
}
