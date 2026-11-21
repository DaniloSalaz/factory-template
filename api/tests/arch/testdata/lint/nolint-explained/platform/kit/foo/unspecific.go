package foo

import "os"

// Unspecific gives a reason but no linter.
func Unspecific() ([]byte, error) {
	return os.ReadFile("b") //nolint // the caller adds the context
}
