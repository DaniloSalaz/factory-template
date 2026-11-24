// Package app is where an app hooks into the platform (docs/core/estructura.md): its
// role catalog and its modules. package main is its only importer.
package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"chaskicode.dev/saas/core/kit/authctx"
	"chaskicode.dev/saas/core/kit/authz"
	"chaskicode.dev/saas/core/kit/db"
	"chaskicode.dev/saas/core/kit/telemetry"
)

// Module is what main needs from an app module: it mounts the module's routes after the
// platform's.
type Module interface {
	Register(mux *http.ServeMux)
}

// Deps is what the platform hands to the app's modules, the same pieces its own modules
// get. There is no pool and no raw config on purpose: tenant data is reached only
// through DB's transactions (RLS), and a module that needs settings declares them in
// its own Deps.
type Deps struct {
	DB     *db.DB
	Logger *slog.Logger
	// Authorizer is the permission check for use cases (Require); it audits denials.
	Authorizer *authz.Authorizer
	// Telemetry gives the tracers for telemetry.ClientSpan and the metrics; nil records
	// nothing.
	Telemetry *telemetry.Telemetry
}

// Modules returns the app's modules, in the order main registers them. The template has
// none. To add one, create app/modules/<m> shaped like core/health (module.go with
// New(Deps) and Register(mux)), then wire it here:
//
//	m, err := <m>.New(<m>.Deps{DB: deps.DB, Logger: deps.Logger, Authorizer: deps.Authorizer})
//	if err != nil {
//		return nil, fmt.Errorf("wire module <m>: %w", err)
//	}
//	modules = append(modules, m)
//
// main never changes for it.
func Modules(deps Deps) ([]Module, error) { //nolint:revive // unused only while the template has no module; it keeps the name the example above uses
	modules := []Module{}
	return modules, nil
}

// ownerMFAMaxAge is how old an owner's MFA may be before acting in a tenant needs a
// new login (auth-y-tenancy §6).
const ownerMFAMaxAge = 12 * time.Hour

// Roles is the app's role catalog (auth-y-tenancy §8). Each app replaces this example
// with its own roles and permissions; owner is reserved by the platform and must keep
// its name and every permission (authz.NewCatalog checks it). The API does not start if
// it is invalid.
func Roles() (*authz.Catalog, error) {
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
