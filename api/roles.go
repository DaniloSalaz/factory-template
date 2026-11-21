package main

import (
	"fmt"
	"time"

	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/authz"
)

// ownerMFAMaxAge is how old an owner's MFA may be before acting in a tenant needs a
// new login (design, section 6).
const ownerMFAMaxAge = 12 * time.Hour

// roleCatalog is the app's role catalog (design, section 8). Each app replaces this
// example with its own roles and permissions; owner is reserved by the platform and
// must keep its name and every permission. The API does not start if it is invalid.
func roleCatalog() (*authz.Catalog, error) {
	members := []authctx.Permission{authz.PermMembersRead, authz.PermMembersInvite, authz.PermMembersManage}
	c, err := authz.NewCatalog(
		authz.Role{
			Name: authz.RoleOwner, Permissions: authz.PlatformPermissions(),
			RequiresMFA: true, MFAMaxAge: ownerMFAMaxAge,
		},
		authz.Role{
			Name:        "admin",
			Permissions: append(members, authz.PermAuditRead, authz.PermTenantUpdate, authctx.PermScopeTenant),
		},
		authz.Role{
			Name:        "member",
			Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("role catalog: %w", err)
	}
	return c, nil
}
