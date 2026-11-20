// Package app contains the health module's use cases and the ports they depend on.
package app

import "context"

// DBChecker verifies that the database answers queries.
type DBChecker interface {
	CheckDB(ctx context.Context) error
}
