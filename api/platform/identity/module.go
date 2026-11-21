// Package identity is the module's public API: login with the identity provider,
// sessions, the session and tenant middlewares, and tenants and their members (design,
// sections 5.1, 5.4, 5.5 and 5.7).
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

	identityapi "chaskicode.dev/saas/gen/openapi/identity"
	"chaskicode.dev/saas/modules/identity/adapters/devsender"
	identityhttp "chaskicode.dev/saas/modules/identity/adapters/http"
	"chaskicode.dev/saas/modules/identity/adapters/postgres"
	"chaskicode.dev/saas/modules/identity/adapters/smtpsender"
	"chaskicode.dev/saas/modules/identity/adapters/workos"
	"chaskicode.dev/saas/modules/identity/app"
	"chaskicode.dev/saas/modules/identity/domain"
	"chaskicode.dev/saas/platform/authz"
	"chaskicode.dev/saas/platform/config"
	"chaskicode.dev/saas/platform/db"
	"chaskicode.dev/saas/platform/httpx"
	"chaskicode.dev/saas/platform/ratelimit"
	"chaskicode.dev/saas/platform/telemetry"
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
	// Tenancy limits how many tenants a user may create.
	Tenancy config.Tenancy
	// TenantRateLimiter limits POST /tenants per user (design, section 6).
	TenantRateLimiter ratelimit.Limiter
	// InvitationRateLimiter limits POST /invitations and their resends per user;
	// AcceptRateLimiter, POST /invitations/accept (design, section 6).
	InvitationRateLimiter ratelimit.Limiter
	AcceptRateLimiter     ratelimit.Limiter
	// InvitationSender delivers invitation links: NewInvitationSender picks it from the
	// configuration.
	InvitationSender InvitationSender
	// Telemetry traces the calls to WorkOS and SMTP and counts logins, MFA verifications,
	// denials, refused tenants and invitation emails; nil records nothing.
	Telemetry *telemetry.Telemetry
	// Provider replaces WorkOS; only tests set it.
	Provider Provider
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Provider is the identity provider: login and, for the second factor, its MFA API.
type Provider interface {
	usecases.IdentityProvider
	usecases.MFAProvider
}

// InvitationSender delivers an invitation's link (usecases.InvitationSender).
type InvitationSender = usecases.InvitationSender

// NewInvitationSender returns the SMTP sender when SMTP is configured (always, outside
// development: config refuses to start without it), and otherwise the development one,
// which writes each link to the log and fails outside development.
func NewInvitationSender(env config.Environment, smtp config.SMTP, tel *telemetry.Telemetry, logger *slog.Logger) (InvitationSender, error) {
	if smtp.Enabled() {
		s, err := smtpsender.New(smtp, nil, tel.Tracers())
		if err != nil {
			return nil, fmt.Errorf("identity: %w", err)
		}
		return s, nil
	}
	s, err := devsender.New(env, logger)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	return s, nil
}

// Module wires the identity module.
type Module struct {
	handler  *identityhttp.Handler
	sessions *usecases.Sessions
	tenancy  *usecases.Tenancy
	authz    *authz.Authorizer
	store    usecases.Store
	catalog  *authz.Catalog
	limiter  ratelimit.Limiter
	logger   *slog.Logger
	metrics  *telemetry.Metrics
}

// New wires adapters and use cases.
func New(deps Deps) (*Module, error) {
	if deps.Catalog == nil || deps.DB == nil || deps.Logger == nil || deps.RateLimiter == nil || deps.MFARateLimiter == nil {
		return nil, errors.New("identity: Catalog, DB, Logger, RateLimiter and MFARateLimiter are required")
	}
	if deps.TenantRateLimiter == nil || deps.InvitationRateLimiter == nil || deps.AcceptRateLimiter == nil {
		return nil, errors.New("identity: TenantRateLimiter, InvitationRateLimiter and AcceptRateLimiter are required")
	}
	if deps.InvitationSender == nil {
		return nil, errors.New("identity: InvitationSender is required")
	}
	if deps.Tenancy.MaxTenantsPerUser < 1 {
		return nil, errors.New("identity: Tenancy.MaxTenantsPerUser must be at least 1")
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
			Tracers: deps.Telemetry.Tracers(),
		}, deps.Logger)
	}
	store := postgres.NewStore(deps.DB)
	policy := domain.SessionPolicy{IdleTTL: deps.Auth.SessionIdleTTL, AbsoluteTTL: deps.Auth.SessionAbsoluteTTL}

	login := usecases.NewLogin(idp, store, codec, usecases.LoginConfig{
		FrontendOrigin: deps.Web.FrontendOrigin, Policy: policy, Metrics: deps.Telemetry.Metrics(),
	}, deps.Now)
	sessions := usecases.NewSessions(idp, store, policy, deps.Now)
	authorizer := authz.NewAuthorizer(usecases.NewDenialAuditor(store), deps.Telemetry.Metrics(), deps.Logger)
	tenantStore := postgres.NewTenantStore(deps.DB)
	members := usecases.NewMembers(tenantStore, authorizer, deps.Catalog)
	return &Module{
		handler: identityhttp.NewHandler(identityhttp.UseCases{
			Login: login, Sessions: sessions, Accounts: usecases.NewAccounts(store, deps.Catalog),
			MFA:     usecases.NewMFA(idp, idp.Name(), store, deps.Catalog, deps.MFARateLimiter, deps.Telemetry.Metrics(), deps.Now),
			Tenants: usecases.NewTenants(tenantStore, authorizer, deps.Tenancy.MaxTenantsPerUser, deps.TenantRateLimiter),
			Members: members,
			Invitations: usecases.NewInvitations(tenantStore, authorizer, members, deps.InvitationSender, usecases.InvitationsConfig{
				FrontBaseURL: deps.Web.FrontBaseURL, InviteLimiter: deps.InvitationRateLimiter, AcceptLimiter: deps.AcceptRateLimiter,
				Metrics: deps.Telemetry.Metrics(),
			}, deps.Logger, deps.Now),
		}, deps.Logger),
		sessions: sessions,
		tenancy:  usecases.NewTenancy(store, deps.Catalog, deps.Now),
		authz:    authorizer,
		store:    store,
		catalog:  deps.Catalog,
		limiter:  deps.RateLimiter,
		logger:   deps.Logger,
		metrics:  deps.Telemetry.Metrics(),
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
	return identityhttp.TenantMiddleware(m.tenancy, m.metrics, m.logger)
}

// CheckRolesInUse logs an error for every role stored in a membership that the catalog
// lacks (design, section 8): those members act with no permissions. It never stops the
// start-up, not even when the query fails.
func (m *Module) CheckRolesInUse(ctx context.Context) {
	unknown, err := usecases.UnknownRolesInUse(ctx, m.store, m.catalog)
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
