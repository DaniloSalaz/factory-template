// Command api is the HTTP API process and the application's composition root.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chaskicode.dev/saas/app"
	"chaskicode.dev/saas/core/health"
	"chaskicode.dev/saas/core/identity"
	"chaskicode.dev/saas/core/kit/clientip"
	"chaskicode.dev/saas/core/kit/config"
	"chaskicode.dev/saas/core/kit/db"
	"chaskicode.dev/saas/core/kit/httpx"
	"chaskicode.dev/saas/core/kit/logx"
	"chaskicode.dev/saas/core/kit/ratelimit"
	"chaskicode.dev/saas/core/kit/telemetry"
	"chaskicode.dev/saas/gen/routes"
)

// Requests per client IP and minute to /auth/login and /auth/callback together: a login
// takes two, so about ten logins a minute.
const (
	authRateLimit  = 20
	authRateWindow = time.Minute
)

// MFA verification attempts per user and per session, and enrollments and challenges per
// user, in 15 minutes (auth-y-tenancy §5.2).
const (
	mfaRateLimit  = 5
	mfaRateWindow = 15 * time.Minute
)

// Requests per user (auth-y-tenancy §6): creating a tenant, five an hour; inviting and
// resending together, twenty an hour; accepting, ten in 15 minutes, which also bounds
// guessing tokens.
const (
	tenantRateLimit  = 5
	tenantRateWindow = time.Hour
	inviteRateLimit  = 20
	inviteRateWindow = time.Hour
	acceptRateLimit  = 10
	acceptRateWindow = 15 * time.Minute
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// `api migrate` applies the embedded migrations and exits; without arguments, the
	// HTTP server.
	switch args := os.Args[1:]; {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "migrate":
		code := migrateCommand(ctx)
		stop()
		os.Exit(code)
	default:
		fmt.Fprintln(os.Stderr, "usage: api [migrate]")
		stop()
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		// The configured logger needs the config, so this one error uses a plain JSON logger.
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).ErrorContext(ctx, "load config", slog.Any("error", err))
		stop()
		os.Exit(1)
	}
	logger := logx.New(os.Stdout, cfg.Log).With(slog.String("env", string(cfg.Env)), slog.String("version", version()))

	if err := run(ctx, cfg, logger); err != nil {
		logger.ErrorContext(ctx, "api stopped with error", slog.Any("error", err))
		stop()
		os.Exit(1)
	}
	stop()
}

// telemetryShutdownTimeout bounds the final flush of spans and metrics, after the HTTP
// server has drained: a collector that does not answer must not hold the process.
const telemetryShutdownTimeout = 5 * time.Second

// run wires the application: database, module handlers, middlewares and server.
func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.InfoContext(ctx, "config loaded", slog.Any("config", cfg))

	tel, err := telemetry.New(ctx, telemetry.Settings{Config: cfg.Telemetry, Env: cfg.Env, Version: version(), Logger: logger})
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() {
		// ctx is cancelled by now; the flush gets its own deadline.
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryShutdownTimeout)
		defer cancel()
		if serr := tel.Shutdown(flushCtx); serr != nil {
			logger.WarnContext(ctx, "telemetry flush on shutdown", slog.Any("error", serr))
		}
	}()

	// An invalid catalog stops the start-up before anything else (auth-y-tenancy §8).
	catalog, err := app.Roles()
	if err != nil {
		return err
	}

	pool, err := db.NewPool(ctx, cfg.DB.URL.Reveal(), db.WithTracer(tel.PgxTracer()))
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if err := tel.RecordPoolStats(pool); err != nil {
		return err
	}

	// Email by SMTP; only development may log the links instead (auth-y-tenancy §5.6.7).
	sender, err := identity.NewInvitationSender(cfg.Env, cfg.SMTP, tel, logger)
	if err != nil {
		return fmt.Errorf("invitation sender: %w", err)
	}

	database := db.New(pool)
	idm, err := identity.New(identity.Deps{
		DB: database, Logger: logger, Web: cfg.Web, Auth: cfg.Auth, WorkOS: cfg.WorkOS,
		RateLimiter:           ratelimit.NewMemory(authRateLimit, authRateWindow, nil),
		MFARateLimiter:        ratelimit.NewMemory(mfaRateLimit, mfaRateWindow, nil),
		Catalog:               catalog,
		Tenancy:               cfg.Tenancy,
		TenantRateLimiter:     ratelimit.NewMemory(tenantRateLimit, tenantRateWindow, nil),
		InvitationRateLimiter: ratelimit.NewMemory(inviteRateLimit, inviteRateWindow, nil),
		AcceptRateLimiter:     ratelimit.NewMemory(acceptRateLimit, acceptRateWindow, nil),
		InvitationSender:      sender,
		Telemetry:             tel,
	})
	if err != nil {
		return fmt.Errorf("wire identity module: %w", err)
	}
	idm.CheckRolesInUse(ctx)

	appModules, err := app.Modules(app.Deps{
		DB: database, Logger: logger, Authorizer: idm.Authorizer(), Telemetry: tel,
	})
	if err != nil {
		return fmt.Errorf("wire app modules: %w", err)
	}

	mux := http.NewServeMux()
	health.New(health.Deps{DB: pool, Logger: logger, Version: version()}).Register(mux)
	idm.Register(mux)
	// The platform's routes first: an app module that tried to take one of them makes
	// ServeMux panic at start-up instead of silently replacing it.
	for _, m := range appModules {
		m.Register(mux)
	}

	// Telemetry is outermost, so its span covers the whole request (ADR 0004; the probes
	// get none).
	handler := tel.HTTP(httpx.Chain(httpx.WithRouteFallback(mux, logger),
		// Before Recover, so that a recovered panic reaches Sentry too.
		httpx.ReportErrors(tel),
		httpx.RequestID(),
		httpx.AccessLog(logger, "/healthz", "/readyz"),
		httpx.Recover(logger),
		httpx.SecurityHeaders(),
		httpx.BodyLimit(cfg.HTTP.MaxBodyBytes),
		httpx.ClientIP(clientip.NewResolver(cfg.Web.TrustedClientIPHeader)),
		httpx.CORS(cfg.Web.AllowedOrigins),
		httpx.CSRF(cfg.Web.AllowedOrigins, logger),
		// What each route requires comes from the security declared in the contract.
		httpx.RouteSecurity(mux, routes.Security()),
		idm.SessionMiddleware(),
		idm.TenantMiddleware(),
		// After authentication, so a caller without a session gets 401, not 400.
		httpx.KnownBodyFields(mux, routes.BodyFields(), logger),
	), "/healthz", "/readyz")

	if err := httpx.NewServer(cfg.HTTP, handler, logger).Run(ctx); err != nil {
		return fmt.Errorf("run http server: %w", err)
	}
	return nil
}
