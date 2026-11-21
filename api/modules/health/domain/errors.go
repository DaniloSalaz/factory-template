// Package domain holds the health module's business concepts. It may import only the
// standard library and platform/apperr.
package domain

import "github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"

// CodeDatabaseUnavailable identifies a readiness failure caused by the database.
const CodeDatabaseUnavailable = "health.db_unavailable"

// DatabaseUnavailable reports that the database does not answer. The cause is kept for
// logs; clients only see the code and message.
func DatabaseUnavailable(cause error) error {
	return apperr.Wrap(cause, apperr.KindUnavailable, CodeDatabaseUnavailable, "The database is not responding.")
}
