// Package identity is the module's public API: login with the identity provider,
// sessions, and the session and tenant middlewares (design, sections 5.1, 5.4 and 5.5).
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	identityapi "github.com/DaniloSalaz/factory-template/api/gen/openapi/identity"
	identityhttp "github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/http"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/postgres"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/workos"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authz"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/ratelimit"
)

// Paths of the module that are rate limited per client IP.
const (
	LoginPath    = "/auth/login"
	CallbackPath = "/auth/callback"
)

// intentKeyLabel derives the key that seals login intents from the WorkOS API key: every
// replica shares it without one more secret, and rotating the API key only invalidates
// logins in flight (five minutes at most).
const intentKeyLabel = "factory-template/identity/oauth-intent/v1"

// Deps are the module's external dependencies.
type Deps struct {
	DB     *db.DB
	Logger *slog.Logger
	Web    config.Web
	Auth   config.Auth
	WorkOS config.WorkOS
	// RateLimiter limits /auth/login and /auth/callback per client IP.
	RateLimiter ratelimit.Limiter
	// MFARateLimiter limits MFA verification attempts, per user and per session, and
	// enrollments and challenges per user (design, 5.2).
	MFARateLimiter ratelimit.Limiter
	// Catalog is the app's validated role catalog. A catalog where no role requires MFA
	// is valid: MFA is then optional for everyone.
	Catalog *authz.Catalog
	// Provider replaces WorkOS; only tests set it.
	Provider Provider
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Provider is the identity provider: login and, for the second factor, its MFA API.
type Provider interface {
	app.IdentityProvider
	app.MFAProvider
}

// Module wires the identity module.
type Module struct {
	handler  *identityhttp.Handler
	sessions *app.Sessions
	tenancy  *app.Tenancy
	authz    *authz.Authorizer
	store    app.Store
	catalog  *authz.Catalog
	limiter  ratelimit.Limiter
	logger   *slog.Logger
}

// New wires adapters and use cases.
func New(deps Deps) (*Module, error) {
	if deps.Catalog == nil || deps.DB == nil || deps.Logger == nil || deps.RateLimiter == nil || deps.MFARateLimiter == nil {
		return nil, errors.New("identity: Catalog, DB, Logger, RateLimiter and MFARateLimiter are required")
	}

	mac := hmac.New(sha256.New, []byte(deps.WorkOS.APIKey.Reveal()))
	mac.Write([]byte(intentKeyLabel))
	codec, err := domain.NewIntentCodec(mac.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("identity: intent codec: %w", err)
	}

	idp := deps.Provider
	if idp == nil {
		idp = workos.New(workos.Config{
			ClientID: deps.WorkOS.ClientID, APIKey: deps.WorkOS.APIKey, RedirectURI: deps.WorkOS.RedirectURI,
		}, deps.Logger)
	}
	store := postgres.NewStore(deps.DB)
	policy := domain.SessionPolicy{IdleTTL: deps.Auth.SessionIdleTTL, AbsoluteTTL: deps.Auth.SessionAbsoluteTTL}

	login := app.NewLogin(idp, store, codec, app.LoginConfig{
		FrontendOrigin: deps.Web.FrontendOrigin, Policy: policy,
	}, deps.Now)
	sessions := app.NewSessions(idp, store, policy, deps.Now)
	return &Module{
		handler: identityhttp.NewHandler(login, sessions, app.NewAccounts(store, deps.Catalog),
			app.NewMFA(idp, idp.Name(), store, deps.Catalog, deps.MFARateLimiter, deps.Now), deps.Logger),
		sessions: sessions,
		tenancy:  app.NewTenancy(store, deps.Catalog, deps.Now),
		authz:    authz.NewAuthorizer(app.NewDenialAuditor(store), deps.Logger),
		store:    store,
		catalog:  deps.Catalog,
		limiter:  deps.RateLimiter,
		logger:   deps.Logger,
	}, nil
}

// Authorizer is the permission check that use cases make (authz.Require), recording
// denials in the tenant's audit trail. Other modules receive it through their Deps.
func (m *Module) Authorizer() *authz.Authorizer {
	return m.authz
}

// TenantMiddleware requires X-Tenant-ID and a membership that may act in it on every
// route whose contract security asks for a tenant, and builds the request's Principal
// from the catalog. It goes right after SessionMiddleware.
func (m *Module) TenantMiddleware() httpx.Middleware {
	return identityhttp.TenantMiddleware(m.tenancy, m.logger)
}

// CheckRolesInUse logs an error for every role stored in a membership that the catalog
// lacks (design, section 8): those members act with no permissions. It never stops the
// start-up, not even when the query fails.
func (m *Module) CheckRolesInUse(ctx context.Context) {
	unknown, err := app.UnknownRolesInUse(ctx, m.store, m.catalog)
	if err != nil {
		m.logger.ErrorContext(ctx, "check roles in use", slog.Any("error", err))
		return
	}
	for _, role := range unknown {
		m.logger.ErrorContext(ctx, "a role in use is not in the catalog: its members have no permissions",
			slog.String("role", role))
	}
}

// Register mounts the module's routes on mux.
func (m *Module) Register(mux *http.ServeMux) {
	strict := identityapi.NewStrictHandlerWithOptions(m.handler,
		[]identityapi.StrictMiddlewareFunc{identityhttp.CaptureRequest},
		identityapi.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  httpx.RequestErrorHandler(m.logger),
			ResponseErrorHandlerFunc: httpx.ResponseErrorHandler(m.logger),
		})
	limit := httpx.RateLimit(m.limiter, m.logger)
	identityapi.HandlerWithOptions(strict, identityapi.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: httpx.RequestErrorHandler(m.logger),
		Middlewares: []identityapi.MiddlewareFunc{func(next http.Handler) http.Handler {
			limited := limit(next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == LoginPath || r.URL.Path == CallbackPath {
					limited.ServeHTTP(w, r)
					return
				}
				next.ServeHTTP(w, r)
			})
		}},
	})
}

// SessionMiddleware requires a session on every route whose contract security asks for
// one, and on every route if httpx.RouteSecurity does not run before it.
func (m *Module) SessionMiddleware() httpx.Middleware {
	return identityhttp.SessionMiddleware(m.sessions, m.logger)
}
