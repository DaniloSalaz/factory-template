package usecases

import (
	"context"
	"fmt"

	"chaskicode.dev/saas/platform/kit/authz"
)

// UnknownRolesInUse returns the roles stored in memberships that catalog lacks (auth-y-tenancy
// §8). Their members act with no permissions until the role is added back or
// they are given another one.
func UnknownRolesInUse(ctx context.Context, store Store, catalog *authz.Catalog) ([]string, error) {
	var inUse []string
	err := store.Anon(ctx, func(ctx context.Context, r Repo) error {
		var err error
		inUse, err = r.RolesInUse(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("roles in use: %w", err)
	}
	return catalog.Unknown(inUse), nil
}
