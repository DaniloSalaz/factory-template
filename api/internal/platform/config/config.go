// Package config loads the process configuration from environment variables. It is the
// only package allowed to read the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// Environment is the deployment environment the process runs in.
type Environment string

// Supported environments.
const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// Valid reports whether e is a supported environment.
func (e Environment) Valid() bool {
	switch e {
	case EnvDevelopment, EnvStaging, EnvProduction:
		return true
	default:
		return false
	}
}

// LogFormat is the log output encoding.
type LogFormat string

// Supported log formats.
const (
	LogFormatJSON LogFormat = "json"
	LogFormatText LogFormat = "text"
)

// Valid reports whether f is a supported log format.
func (f LogFormat) Valid() bool { return f == LogFormatJSON || f == LogFormatText }

// Config is the typed process configuration.
type Config struct {
	Env    Environment `env:"APP_ENV"`
	HTTP   HTTP
	DB     DB
	Log    Log
	Web    Web
	Auth   Auth
	WorkOS WorkOS
}

// HTTP configures the HTTP server.
type HTTP struct {
	Port              int           `env:"HTTP_PORT"                envDefault:"8080"`
	ReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" envDefault:"5s"`
	ReadTimeout       time.Duration `env:"HTTP_READ_TIMEOUT"        envDefault:"15s"`
	WriteTimeout      time.Duration `env:"HTTP_WRITE_TIMEOUT"       envDefault:"15s"`
	IdleTimeout       time.Duration `env:"HTTP_IDLE_TIMEOUT"        envDefault:"60s"`
	ShutdownTimeout   time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT"    envDefault:"10s"`
	MaxHeaderBytes    int           `env:"HTTP_MAX_HEADER_BYTES"    envDefault:"1048576"`
	MaxBodyBytes      int64         `env:"HTTP_MAX_BODY_BYTES"      envDefault:"1048576"`
}

// Addr returns the listen address for the configured port on all interfaces.
func (h HTTP) Addr() string { return ":" + strconv.Itoa(h.Port) }

// DB configures the database connection.
type DB struct {
	URL DatabaseURL `env:"DATABASE_URL"`
}

// Log configures logging.
type Log struct {
	Level  slog.Level `env:"LOG_LEVEL"  envDefault:"info"`
	Format LogFormat  `env:"LOG_FORMAT" envDefault:"json"`
}

// Web describes the browser side: where the front end lives and who may call the API.
type Web struct {
	// FrontendOrigin is the origin return_to must resolve to after login (design, 5.1.1).
	FrontendOrigin string `env:"FRONTEND_ORIGIN"`
	// AllowedOrigins are the only origins accepted by CORS and by the CSRF Origin check.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envSeparator:","`
	// TrustedClientIPHeader is the header that carries the client IP: CF-Connecting-IP by
	// default in staging and production, behind Cloudflare Tunnel. Empty (development)
	// means RemoteAddr. X-Forwarded-For is never trusted.
	TrustedClientIPHeader string `env:"TRUSTED_CLIENT_IP_HEADER"`
}

// Auth configures sessions (design, section 6).
type Auth struct {
	SessionIdleTTL     time.Duration `env:"SESSION_IDLE_TTL"     envDefault:"168h"`
	SessionAbsoluteTTL time.Duration `env:"SESSION_ABSOLUTE_TTL" envDefault:"720h"`
}

// WorkOS configures the WorkOS AuthKit identity provider.
type WorkOS struct {
	ClientID    string     `env:"WORKOS_CLIENT_ID"`
	APIKey      pii.Secret `env:"WORKOS_API_KEY"`
	RedirectURI string     `env:"WORKOS_REDIRECT_URI"`
	// MFARequired states that MFA is set to "Required" in this environment's WorkOS
	// dashboard. WorkOS reports no MFA claim, so this is the only evidence the app has
	// that a login included MFA (design, 5.2 and 13); docs/runbooks/auth.md says how to
	// check that it matches the dashboard.
	MFARequired bool `env:"WORKOS_MFA_REQUIRED" envDefault:"false"`
}

// DefaultDeployedClientIPHeader is the client IP header in staging and production, where
// the API is reachable only through Cloudflare Tunnel (design, section 3). Without it,
// every request would carry the tunnel's address: one rate-limit quota for everyone and
// the wrong ip_prefix in sessions and the audit log.
const DefaultDeployedClientIPHeader = "CF-Connecting-IP"

// VarError reports a problem with one environment variable. It never carries the value.
type VarError struct {
	Var    string
	Reason string
}

// Error implements error.
func (e *VarError) Error() string { return e.Var + " " + e.Reason }

// Load reads the process environment and validates the result, reporting every problem.
func Load() (Config, error) {
	return load(env.ToMap(os.Environ()))
}

func load(environ map[string]string) (Config, error) {
	var cfg Config
	var errs []error
	reported := map[string]bool{}

	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environ}); err != nil {
		for _, e := range sanitizeParseError(err) {
			errs = append(errs, e)
			reported[e.Var] = true
		}
	}
	cfg.applyDerivedDefaults()
	// A variable that failed to parse also fails validation as a zero value; report it once.
	for _, e := range cfg.validate() {
		if !reported[e.Var] {
			errs = append(errs, e)
		}
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// applyDerivedDefaults fills defaults that depend on other variables, which envDefault
// cannot express, and normalises origins so they compare exactly.
func (c *Config) applyDerivedDefaults() {
	if c.Web.TrustedClientIPHeader == "" && (c.Env == EnvStaging || c.Env == EnvProduction) {
		c.Web.TrustedClientIPHeader = DefaultDeployedClientIPHeader
	}
	if o, ok := normalizeOrigin(c.Web.FrontendOrigin); ok {
		c.Web.FrontendOrigin = o
	}
	for i, raw := range c.Web.AllowedOrigins {
		if o, ok := normalizeOrigin(strings.TrimSpace(raw)); ok {
			c.Web.AllowedOrigins[i] = o
		}
	}
}

// Validate checks every field and returns all problems joined, or nil.
func (c Config) Validate() error {
	verrs := c.validate()
	errs := make([]error, len(verrs))
	for i, e := range verrs {
		errs[i] = e
	}
	return errors.Join(errs...)
}

func (c Config) validate() []*VarError {
	var errs []*VarError
	add := func(v, reason string) { errs = append(errs, &VarError{Var: v, Reason: reason}) }

	switch {
	case c.Env == "":
		add("APP_ENV", "is required")
	case !c.Env.Valid():
		add("APP_ENV", "must be one of development, staging, production")
	}

	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		add("HTTP_PORT", "must be between 1 and 65535")
	}
	if c.HTTP.MaxHeaderBytes <= 0 {
		add("HTTP_MAX_HEADER_BYTES", "must be greater than zero")
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		add("HTTP_MAX_BODY_BYTES", "must be greater than zero")
	}
	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{"HTTP_READ_HEADER_TIMEOUT", c.HTTP.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", c.HTTP.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", c.HTTP.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", c.HTTP.IdleTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT", c.HTTP.ShutdownTimeout},
	} {
		if d.value <= 0 {
			add(d.name, "must be greater than zero")
		}
	}

	switch {
	case c.DB.URL == "":
		add("DATABASE_URL", "is required")
	case !c.DB.URL.valid():
		add("DATABASE_URL", "must be a postgres:// or postgresql:// URL with a host")
	}

	if !c.Log.Format.Valid() {
		add("LOG_FORMAT", "must be one of json, text")
	}

	c.validateWeb(add)
	c.validateAuth(add)
	c.validateWorkOS(add)
	return errs
}

func (c Config) validateWeb(add func(v, reason string)) {
	frontendOK := false
	switch {
	case c.Web.FrontendOrigin == "":
		add("FRONTEND_ORIGIN", "is required")
	case !isOrigin(c.Web.FrontendOrigin):
		add("FRONTEND_ORIGIN", "must be an origin such as https://app.example.com, without path")
	default:
		frontendOK = true
	}

	switch {
	case len(c.Web.AllowedOrigins) == 0:
		add("ALLOWED_ORIGINS", "is required")
	case !all(c.Web.AllowedOrigins, isOrigin):
		add("ALLOWED_ORIGINS", "must be a comma-separated list of origins, without wildcards or paths")
	case frontendOK && !slices.Contains(c.Web.AllowedOrigins, c.Web.FrontendOrigin):
		add("ALLOWED_ORIGINS", "must include FRONTEND_ORIGIN")
	}

	h := c.Web.TrustedClientIPHeader
	switch {
	case h != "" && !isHeaderName(h):
		add("TRUSTED_CLIENT_IP_HEADER", "must be a header name")
	case strings.EqualFold(h, "X-Forwarded-For"):
		add("TRUSTED_CLIENT_IP_HEADER", "must not be X-Forwarded-For, which the client controls")
	}
}

func (c Config) validateAuth(add func(v, reason string)) {
	if c.Auth.SessionIdleTTL <= 0 {
		add("SESSION_IDLE_TTL", "must be greater than zero")
	}
	switch {
	case c.Auth.SessionAbsoluteTTL <= 0:
		add("SESSION_ABSOLUTE_TTL", "must be greater than zero")
	case c.Auth.SessionAbsoluteTTL < c.Auth.SessionIdleTTL:
		add("SESSION_ABSOLUTE_TTL", "must not be shorter than SESSION_IDLE_TTL")
	}
}

func (c Config) validateWorkOS(add func(v, reason string)) {
	switch {
	case c.WorkOS.ClientID == "":
		add("WORKOS_CLIENT_ID", "is required")
	case !strings.HasPrefix(c.WorkOS.ClientID, "client_"):
		add("WORKOS_CLIENT_ID", "must start with client_")
	}
	if c.WorkOS.APIKey == "" {
		add("WORKOS_API_KEY", "is required")
	}

	switch u, err := url.Parse(c.WorkOS.RedirectURI); {
	case c.WorkOS.RedirectURI == "":
		add("WORKOS_REDIRECT_URI", "is required")
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		add("WORKOS_REDIRECT_URI", "must be an absolute http(s) URL")
	case u.Scheme != "https" && (c.Env == EnvStaging || c.Env == EnvProduction):
		add("WORKOS_REDIRECT_URI", "must use https outside development")
	}
}

// normalizeOrigin returns scheme://host[:port] in lower case, or false when raw is not
// a bare http(s) origin.
func normalizeOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" ||
		strings.Contains(u.Host, "*") {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

func isOrigin(s string) bool {
	o, ok := normalizeOrigin(s)
	return ok && o == s
}

func isHeaderName(s string) bool {
	for _, c := range []byte(s) {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-'
		if !ok {
			return false
		}
	}
	return s != ""
}

func all[T any](xs []T, pred func(T) bool) bool {
	for _, x := range xs {
		if !pred(x) {
			return false
		}
	}
	return true
}

// sanitizeParseError turns caarlos0/env errors into VarErrors using only their typed
// fields. ParseError.Err is never used: its text can contain the raw value.
func sanitizeParseError(err error) []*VarError {
	list := []error{err}
	var agg env.AggregateError
	if errors.As(err, &agg) {
		list = agg.Errors
	}

	keys := envKeysByField(reflect.TypeFor[Config]())
	out := make([]*VarError, 0, len(list))
	for _, e := range list {
		var (
			notSet   env.VarIsNotSetError
			empty    env.EmptyVarError
			parseErr env.ParseError
		)
		switch {
		case errors.As(e, &notSet):
			out = append(out, &VarError{Var: notSet.Key, Reason: "is required"})
		case errors.As(e, &empty):
			out = append(out, &VarError{Var: empty.Key, Reason: "is required"})
		case errors.As(e, &parseErr):
			// ParseError names the Go field, not the variable.
			key, ok := keys[parseErr.Name]
			if !ok {
				key = "field " + parseErr.Name
			}
			out = append(out, &VarError{Var: key, Reason: "has an invalid value, expected " + describeType(parseErr.Type)})
		default:
			out = append(out, &VarError{Var: "environment", Reason: "could not be parsed"})
		}
	}
	return out
}

// envKeysByField maps Go field names to their env variable. Field names are unique across
// Config (enforced by a test), so the name alone identifies the variable.
func envKeysByField(t reflect.Type) map[string]string {
	keys := map[string]string{}
	for f := range t.Fields() {
		if f.Type.Kind() == reflect.Struct && f.Tag.Get("env") == "" {
			for name, key := range envKeysByField(f.Type) {
				keys[name] = key
			}
			continue
		}
		if tag := f.Tag.Get("env"); tag != "" {
			key, _, _ := strings.Cut(tag, ",")
			keys[f.Name] = key
		}
	}
	return keys
}

func describeType(t reflect.Type) string {
	switch t {
	case reflect.TypeFor[time.Duration]():
		return "a duration such as 10s"
	case reflect.TypeFor[slog.Level]():
		return "a log level (debug, info, warn, error)"
	}
	switch t.Kind() { //nolint:exhaustive // only kinds used by Config need a friendly name.
	case reflect.Int, reflect.Int64:
		return "an integer"
	default:
		return t.String()
	}
}

// String renders the configuration with secrets masked.
func (c Config) String() string {
	return fmt.Sprintf(
		"Config{Env:%s HTTP:{Port:%d ReadHeaderTimeout:%s ReadTimeout:%s WriteTimeout:%s IdleTimeout:%s ShutdownTimeout:%s MaxHeaderBytes:%d MaxBodyBytes:%d} DB:{URL:%s} Log:{Level:%s Format:%s} "+
			"Web:{FrontendOrigin:%s AllowedOrigins:%v TrustedClientIPHeader:%s} Auth:{SessionIdleTTL:%s SessionAbsoluteTTL:%s} "+
			"WorkOS:{ClientID:%s APIKey:%s RedirectURI:%s MFARequired:%t}}",
		c.Env, c.HTTP.Port, c.HTTP.ReadHeaderTimeout, c.HTTP.ReadTimeout, c.HTTP.WriteTimeout,
		c.HTTP.IdleTimeout, c.HTTP.ShutdownTimeout, c.HTTP.MaxHeaderBytes, c.HTTP.MaxBodyBytes, c.DB.URL, c.Log.Level, c.Log.Format,
		c.Web.FrontendOrigin, c.Web.AllowedOrigins, c.Web.TrustedClientIPHeader, c.Auth.SessionIdleTTL, c.Auth.SessionAbsoluteTTL,
		c.WorkOS.ClientID, c.WorkOS.APIKey, c.WorkOS.RedirectURI, c.WorkOS.MFARequired,
	)
}

// GoString keeps %#v masked as well.
func (c Config) GoString() string { return c.String() }

// LogValue renders the configuration for slog with secrets masked.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", string(c.Env)),
		slog.Group("http",
			slog.Int("port", c.HTTP.Port),
			slog.Duration("read_header_timeout", c.HTTP.ReadHeaderTimeout),
			slog.Duration("read_timeout", c.HTTP.ReadTimeout),
			slog.Duration("write_timeout", c.HTTP.WriteTimeout),
			slog.Duration("idle_timeout", c.HTTP.IdleTimeout),
			slog.Duration("shutdown_timeout", c.HTTP.ShutdownTimeout),
			slog.Int("max_header_bytes", c.HTTP.MaxHeaderBytes),
			slog.Int64("max_body_bytes", c.HTTP.MaxBodyBytes),
		),
		slog.Group("db", slog.String("url", c.DB.URL.String())),
		slog.Group("log", slog.String("level", c.Log.Level.String()), slog.String("format", string(c.Log.Format))),
		slog.Group("web",
			slog.String("frontend_origin", c.Web.FrontendOrigin),
			slog.Any("allowed_origins", c.Web.AllowedOrigins),
			slog.String("trusted_client_ip_header", c.Web.TrustedClientIPHeader),
		),
		slog.Group("auth",
			slog.Duration("session_idle_ttl", c.Auth.SessionIdleTTL),
			slog.Duration("session_absolute_ttl", c.Auth.SessionAbsoluteTTL),
		),
		slog.Group("workos",
			slog.String("client_id", c.WorkOS.ClientID),
			slog.Any("api_key", c.WorkOS.APIKey),
			slog.String("redirect_uri", c.WorkOS.RedirectURI),
			slog.Bool("mfa_required", c.WorkOS.MFARequired),
		),
	)
}

// DatabaseURL is a connection URL that hides its credentials and query when printed.
type DatabaseURL string

// Reveal returns the full URL, credentials included.
func (u DatabaseURL) Reveal() string { return string(u) }

// String renders scheme://user:***@host/path; the query is dropped because it may hold
// credentials too. Unparseable values are fully redacted.
func (u DatabaseURL) String() string {
	parsed, err := url.Parse(string(u))
	if err != nil || parsed.Host == "" {
		return pii.Redacted
	}
	var b strings.Builder
	b.WriteString(parsed.Scheme + "://")
	if parsed.User != nil {
		b.WriteString(parsed.User.Username() + ":***@")
	}
	b.WriteString(parsed.Host + parsed.EscapedPath())
	return b.String()
}

// GoString implements fmt.GoStringer with the masked value.
func (u DatabaseURL) GoString() string { return strconv.Quote(u.String()) }

// LogValue implements slog.LogValuer with the masked value.
func (u DatabaseURL) LogValue() slog.Value { return slog.StringValue(u.String()) }

// MarshalText masks the value in JSON and other text encodings.
func (u DatabaseURL) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

func (u DatabaseURL) valid() bool {
	parsed, err := url.Parse(string(u))
	if err != nil {
		return false
	}
	return (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") && parsed.Host != ""
}
