// Package identity is the module's public API: login with the identity provider,
// sessions and the session middleware (design, sections 5.1, 5.4.1 and 5.5).
package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	identityapi "github.com/DaniloSalaz/factory-template/api/gen/openapi/identity"
	identityhttp "github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/http"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/postgres"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/workos"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/ratelimit"
)

// Paths of the module that work without a session.
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
	// RolesRequiringMFA are the roles of the app's catalog that require MFA (plan step 4).
	RolesRequiringMFA []string
	// Provider replaces WorkOS; only tests set it.
	Provider app.IdentityProvider
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Module wires the identity module.
type Module struct {
	handler  *identityhttp.Handler
	sessions *app.Sessions
	limiter  ratelimit.Limiter
	logger   *slog.Logger
}

// New wires adapters and use cases. It fails if the configuration cannot honour the MFA
// that the roles require.
func New(deps Deps) (*Module, error) {
	if err := checkMFASupport(deps.RolesRequiringMFA, deps.WorkOS.MFARequired); err != nil {
		return nil, err
	}
	if deps.DB == nil || deps.Logger == nil || deps.RateLimiter == nil {
		return nil, errors.New("identity: DB, Logger and RateLimiter are required")
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
		FrontendOrigin: deps.Web.FrontendOrigin, Policy: policy, ProviderEnforcesMFA: deps.WorkOS.MFARequired,
	}, deps.Now)
	sessions := app.NewSessions(idp, store, policy, deps.Now)
	return &Module{
		handler:  identityhttp.NewHandler(login, sessions, deps.Logger),
		sessions: sessions,
		limiter:  deps.RateLimiter,
		logger:   deps.Logger,
	}, nil
}

// checkMFASupport refuses to start when a role requires MFA that the app could not
// establish (design, 5.2 and 13).
func checkMFASupport(rolesRequiringMFA []string, providerEnforcesMFA bool) error {
	if len(rolesRequiringMFA) == 0 || providerEnforcesMFA {
		return nil
	}
	roles := slices.Sorted(slices.Values(rolesRequiringMFA))
	return fmt.Errorf("identity: roles %s require MFA but WORKOS_MFA_REQUIRED is false. "+
		"WorkOS does not report whether a login used MFA, so the app can only rely on it when "+
		"the WorkOS dashboard requires MFA for every user: set Authentication > Multi-factor auth "+
		"to Required in this environment's dashboard and WORKOS_MFA_REQUIRED=true "+
		"(docs/runbooks/auth.md)", strings.Join(roles, ", "))
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

// SessionMiddleware requires a session on every route except the login, the callback
// and publicPaths (probes, say).
func (m *Module) SessionMiddleware(publicPaths ...string) httpx.Middleware {
	return identityhttp.SessionMiddleware(m.sessions, append([]string{LoginPath, CallbackPath}, publicPaths...), m.logger)
}
