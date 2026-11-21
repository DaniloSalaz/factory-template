package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
)

const (
	testDBPassword = "hunter2-db-password"
	testAPIKey     = "sk_test_hunter2-workos-key"
)

func validEnv() map[string]string {
	return map[string]string{
		"APP_ENV":             "development",
		"DATABASE_URL":        "postgres://app:" + testDBPassword + "@localhost:5432/app?sslmode=disable&password=" + testDBPassword,
		"FRONTEND_ORIGIN":     "http://localhost:5173",
		"ALLOWED_ORIGINS":     "http://localhost:5173",
		"WORKOS_CLIENT_ID":    "client_test",
		"WORKOS_API_KEY":      testAPIKey,
		"WORKOS_REDIRECT_URI": "http://localhost:5173/auth/callback",
	}
}

// testSMTPPassword must never appear in an error or a rendering.
const testSMTPPassword = "smtp-hunter2-password"

// deployed adds what staging and production require besides development: SMTP.
func deployed(overrides map[string]string) map[string]string {
	m := map[string]string{
		"SMTP_HOST": "smtp.example.com", "SMTP_PORT": "587", "SMTP_USERNAME": "apikey",
		"SMTP_PASSWORD": testSMTPPassword, "SMTP_FROM": "Acme <no-reply@example.com>",
	}
	for k, v := range overrides {
		m[k] = v
	}
	return with(m)
}

func with(overrides map[string]string) map[string]string {
	m := validEnv()
	for k, v := range overrides {
		if v == "<unset>" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func TestLoad_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		environ map[string]string
		want    func(Config) bool
	}{
		{
			name:    "defaults",
			environ: validEnv(),
			want: func(c Config) bool {
				return c.Env == EnvDevelopment && c.HTTP.Port == 8080 && c.HTTP.Addr() == ":8080" &&
					c.HTTP.ReadHeaderTimeout == 5*time.Second && c.HTTP.ReadTimeout == 15*time.Second &&
					c.HTTP.WriteTimeout == 15*time.Second && c.HTTP.IdleTimeout == time.Minute &&
					c.HTTP.ShutdownTimeout == 10*time.Second && c.HTTP.MaxHeaderBytes == 1<<20 && c.HTTP.MaxBodyBytes == 1<<20 &&
					c.Log.Level == slog.LevelInfo &&
					c.Log.Format == LogFormatJSON &&
					c.Auth.SessionIdleTTL == 7*24*time.Hour && c.Auth.SessionAbsoluteTTL == 30*24*time.Hour &&
					c.Web.TrustedClientIPHeader == "" &&
					c.WorkOS.APIKey.Reveal() == testAPIKey &&
					c.Tenancy.MaxTenantsPerUser == 5 &&
					c.Web.FrontBaseURL == "http://localhost:5173"
			},
		},
		{
			name: "staging trusts CF-Connecting-IP by default",
			environ: deployed(map[string]string{
				"FRONTEND_ORIGIN": "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
				"APP_ENV": "staging", "WORKOS_REDIRECT_URI": "https://api.example.com/auth/callback",
			}),
			want: func(c Config) bool { return c.Web.TrustedClientIPHeader == "CF-Connecting-IP" },
		},
		{
			name: "production trusts CF-Connecting-IP by default",
			environ: deployed(map[string]string{
				"FRONTEND_ORIGIN": "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
				"APP_ENV": "production", "WORKOS_REDIRECT_URI": "https://api.example.com/auth/callback",
			}),
			want: func(c Config) bool { return c.Web.TrustedClientIPHeader == "CF-Connecting-IP" },
		},
		{
			name: "origins are normalised and listed",
			environ: with(map[string]string{
				"FRONTEND_ORIGIN": "HTTPS://App.Example.com/",
				"ALLOWED_ORIGINS": "https://app.example.com, https://admin.example.com",
			}),
			want: func(c Config) bool {
				return c.Web.FrontendOrigin == "https://app.example.com" &&
					slices.Equal(c.Web.AllowedOrigins, []string{"https://app.example.com", "https://admin.example.com"})
			},
		},
		{
			name:    "front base url with a path",
			environ: with(map[string]string{"FRONT_BASE_URL": "https://example.com/app/"}),
			want:    func(c Config) bool { return c.Web.FrontBaseURL == "https://example.com/app" },
		},
		{
			name: "overrides",
			environ: deployed(map[string]string{
				"FRONTEND_ORIGIN": "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
				"APP_ENV": "production", "HTTP_PORT": "9090", "HTTP_WRITE_TIMEOUT": "30s",
				"LOG_LEVEL": "debug", "LOG_FORMAT": "text", "DATABASE_URL": "postgresql://db.internal/app",
				"WORKOS_REDIRECT_URI": "https://api.example.com/auth/callback", "TENANT_MAX_PER_USER": "20",
			}),
			want: func(c Config) bool {
				return c.Env == EnvProduction && c.HTTP.Port == 9090 && c.HTTP.WriteTimeout == 30*time.Second &&
					c.Tenancy.MaxTenantsPerUser == 20 &&
					c.Log.Level == slog.LevelDebug && c.Log.Format == LogFormatText &&
					c.DB.URL.Reveal() == "postgresql://db.internal/app"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := load(tc.environ)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if !tc.want(cfg) {
				t.Fatalf("unexpected config: %s", cfg)
			}
		})
	}
}

func TestLoad_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		environ  map[string]string
		wantVars []string
		secrets  []string // values that must not appear in the error
	}{
		{
			name:     "missing APP_ENV",
			environ:  with(map[string]string{"APP_ENV": "<unset>"}),
			wantVars: []string{"APP_ENV is required"},
		},
		{
			name:    "missing every required variable",
			environ: map[string]string{},
			wantVars: []string{
				"APP_ENV is required", "DATABASE_URL is required", "FRONTEND_ORIGIN is required",
				"ALLOWED_ORIGINS is required", "WORKOS_CLIENT_ID is required", "WORKOS_API_KEY is required",
				"WORKOS_REDIRECT_URI is required",
			},
		},
		{
			name: "origins must be exact, without wildcards, and include the front end",
			environ: with(map[string]string{
				"FRONTEND_ORIGIN": "https://app.example.com/login",
				"ALLOWED_ORIGINS": "https://*.example.com",
			}),
			wantVars: []string{"FRONTEND_ORIGIN must be an origin", "ALLOWED_ORIGINS must be a comma-separated list"},
		},
		{
			name:     "allowed origins without the front end",
			environ:  with(map[string]string{"ALLOWED_ORIGINS": "https://other.example.com"}),
			wantVars: []string{"ALLOWED_ORIGINS must include FRONTEND_ORIGIN"},
		},
		{
			name:     "X-Forwarded-For is never trusted",
			environ:  with(map[string]string{"TRUSTED_CLIENT_IP_HEADER": "x-forwarded-for"}),
			wantVars: []string{"TRUSTED_CLIENT_IP_HEADER must not be X-Forwarded-For"},
		},
		{
			name:     "absolute TTL shorter than idle TTL",
			environ:  with(map[string]string{"SESSION_IDLE_TTL": "48h", "SESSION_ABSOLUTE_TTL": "24h"}),
			wantVars: []string{"SESSION_ABSOLUTE_TTL must not be shorter than SESSION_IDLE_TTL"},
		},
		{
			name: "WorkOS values",
			environ: deployed(map[string]string{
				"APP_ENV": "staging", "WORKOS_CLIENT_ID": "wrong", "WORKOS_REDIRECT_URI": "http://api.example.com/auth/callback",
				"FRONTEND_ORIGIN": "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
			}),
			wantVars: []string{"WORKOS_CLIENT_ID must start with client_", "WORKOS_REDIRECT_URI must use https outside development"},
		},
		{
			name: "every browser-facing URL must be https outside development",
			environ: deployed(map[string]string{
				"APP_ENV": "production", "FRONTEND_ORIGIN": "http://app.example.com",
				"ALLOWED_ORIGINS": "http://app.example.com", "FRONT_BASE_URL": "http://app.example.com/app",
				"WORKOS_REDIRECT_URI": "http://app.example.com/auth/callback",
			}),
			wantVars: []string{
				"FRONTEND_ORIGIN must use https outside development", "ALLOWED_ORIGINS must use https outside development",
				"FRONT_BASE_URL must use https outside development", "WORKOS_REDIRECT_URI must use https outside development",
			},
		},
		{
			name: "one http origin among the allowed ones is enough to refuse",
			environ: deployed(map[string]string{
				"APP_ENV": "staging", "FRONTEND_ORIGIN": "https://app.example.com",
				"ALLOWED_ORIGINS":     "https://app.example.com,http://admin.example.com",
				"WORKOS_REDIRECT_URI": "https://app.example.com/auth/callback",
			}),
			wantVars: []string{"ALLOWED_ORIGINS must use https outside development"},
		},
		{
			name: "a default front base url reports its http once, on FRONTEND_ORIGIN",
			environ: deployed(map[string]string{
				"APP_ENV": "staging", "FRONTEND_ORIGIN": "http://app.example.com",
				"ALLOWED_ORIGINS": "https://app.example.com", "WORKOS_REDIRECT_URI": "https://app.example.com/auth/callback",
			}),
			wantVars: []string{"FRONTEND_ORIGIN must use https outside development"},
		},
		{
			name:     "front base url with a fragment",
			environ:  with(map[string]string{"FRONT_BASE_URL": "http://localhost:5173/#x"}),
			wantVars: []string{"FRONT_BASE_URL must be an absolute http(s) URL"},
		},
		{
			name:     "no tenant may be created",
			environ:  with(map[string]string{"TENANT_MAX_PER_USER": "0"}),
			wantVars: []string{"TENANT_MAX_PER_USER must be between 1 and 1000"},
		},
		{
			name:     "a limit that is no limit",
			environ:  with(map[string]string{"TENANT_MAX_PER_USER": "50000"}),
			wantVars: []string{"TENANT_MAX_PER_USER must be between 1 and 1000"},
		},
		{
			name:     "empty counts as missing",
			environ:  with(map[string]string{"DATABASE_URL": ""}),
			wantVars: []string{"DATABASE_URL is required"},
		},
		{
			name: "unparseable values name the variable but not the value",
			environ: with(map[string]string{
				"HTTP_PORT": "port-abc", "HTTP_READ_TIMEOUT": "soonish", "LOG_LEVEL": "loudest",
			}),
			wantVars: []string{"HTTP_PORT has an invalid value", "HTTP_READ_TIMEOUT has an invalid value", "LOG_LEVEL has an invalid value"},
			secrets:  []string{"port-abc", "soonish", "loudest"},
		},
		{
			name: "out of range and unsupported values",
			environ: with(map[string]string{
				"APP_ENV": "qa-env", "HTTP_PORT": "70000", "HTTP_IDLE_TIMEOUT": "0s", "LOG_FORMAT": "xmlish",
				"HTTP_MAX_HEADER_BYTES": "-1", "HTTP_MAX_BODY_BYTES": "0",
				"DATABASE_URL": "mysql://root:" + testDBPassword + "@db/app",
			}),
			wantVars: []string{"APP_ENV must be one of", "HTTP_PORT must be between", "HTTP_IDLE_TIMEOUT must be greater", "HTTP_MAX_HEADER_BYTES must be greater", "HTTP_MAX_BODY_BYTES must be greater", "LOG_FORMAT must be one of", "DATABASE_URL must be a postgres"},
			secrets:  []string{"qa-env", "70000", "xmlish", testDBPassword},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := load(tc.environ)
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := err.Error()
			for _, want := range tc.wantVars {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not contain %q:\n%s", want, msg)
				}
			}
			for _, secret := range append(tc.secrets, testDBPassword, testAPIKey, testSMTPPassword) {
				if strings.Contains(msg, secret) {
					t.Errorf("error leaks a value: %q", secret)
				}
			}
			if n := strings.Count(msg, "\n") + 1; n != len(tc.wantVars) {
				t.Errorf("got %d errors, want %d (each variable reported once):\n%s", n, len(tc.wantVars), msg)
			}
		})
	}
}

// Load reads the real process environment; t.Setenv makes this test non-parallel.
func TestLoad_ReadsProcessEnvironment(t *testing.T) {
	for k, v := range with(map[string]string{"HTTP_PORT": "8181"}) {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Port != 8181 {
		t.Fatalf("HTTP.Port = %d, want 8181", cfg.HTTP.Port)
	}
}

func TestConfig_RenderingsMaskSecrets(t *testing.T) {
	t.Parallel()

	cfg, err := load(deployed(map[string]string{
		"APP_ENV": "production", "FRONTEND_ORIGIN": "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
		"WORKOS_REDIRECT_URI": "https://app.example.com/auth/callback",
		"DATABASE_URL":        "postgres://app:" + testDBPassword + "@localhost:5432/app?sslmode=disable&password=" + testDBPassword,
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("config", slog.Any("config", cfg))
	j, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json: %v", err)
	}

	renderings := map[string]string{
		"String": cfg.String(),
		"%v":     fmt.Sprintf("%v", cfg),
		"%+v":    fmt.Sprintf("%+v", cfg),
		"%#v":    fmt.Sprintf("%#v", cfg),
		"%v db":  fmt.Sprintf("%+v", cfg.DB),
		"%#v db": fmt.Sprintf("%#v", cfg.DB),
		"slog":   logBuf.String(),
		"json":   string(j),
	}
	for name, out := range renderings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if strings.Contains(out, testDBPassword) {
				t.Fatalf("leaks the database password: %s", out)
			}
			if strings.Contains(out, testAPIKey) {
				t.Fatalf("leaks the WorkOS API key: %s", out)
			}
			if strings.Contains(out, testSMTPPassword) {
				t.Fatalf("leaks the SMTP password: %s", out)
			}
			if !strings.Contains(out, "postgres://app:***@localhost:5432/app") {
				t.Fatalf("masked URL missing: %s", out)
			}
		})
	}
}

func TestDatabaseURL_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  DatabaseURL
		want string
	}{
		{"credentials and query", "postgres://u:pw@h:5432/db?password=pw", "postgres://u:***@h:5432/db"},
		{"user without password", "postgres://u@h/db", "postgres://u:***@h/db"},
		{"no user", "postgresql://h/db", "postgresql://h/db"},
		{"key value dsn", "host=h password=pw", "[REDACTED]"},
		{"unparseable", "postgres://u:pw@h:bad port/db", "[REDACTED]"},
		{"empty", "", "[REDACTED]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.url.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// sanitizeParseError resolves variables by Go field name, which only works if names are unique.
func TestConfig_FieldNamesAreUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for f := range rt.Fields() {
			if f.Type.Kind() == reflect.Struct && f.Tag.Get("env") == "" {
				walk(f.Type)
				continue
			}
			if seen[f.Name] {
				t.Errorf("duplicate field name %q in Config", f.Name)
			}
			seen[f.Name] = true
		}
	}
	walk(reflect.TypeFor[Config]())
}

func TestSanitizeParseError(t *testing.T) {
	t.Parallel()

	const raw = "raw-value-123"
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{"not set", env.VarIsNotSetError{Key: "APP_ENV"}, []string{"APP_ENV is required"}},
		{"empty", env.EmptyVarError{Key: "DATABASE_URL"}, []string{"DATABASE_URL is required"}},
		{
			"parse error uses the variable and ignores Err",
			env.ParseError{Name: "Port", Type: reflect.TypeFor[int](), Err: errors.New(`parsing "` + raw + `"`)},
			[]string{"HTTP_PORT has an invalid value, expected an integer"},
		},
		{
			"parse error on an unknown field",
			env.ParseError{Name: "Mystery", Type: reflect.TypeFor[bool](), Err: errors.New(raw)},
			[]string{"field Mystery has an invalid value, expected bool"},
		},
		{"unknown error is generic", errors.New(raw), []string{"environment could not be parsed"}},
		{
			"aggregate keeps every error",
			env.AggregateError{Errors: []error{env.VarIsNotSetError{Key: "APP_ENV"}, env.ParseError{Name: "Level", Type: reflect.TypeFor[slog.Level](), Err: errors.New(raw)}}},
			[]string{"APP_ENV is required", "LOG_LEVEL has an invalid value, expected a log level (debug, info, warn, error)"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := sanitizeParseError(tc.err, reflect.TypeFor[Config]())
			if len(got) != len(tc.want) {
				t.Fatalf("got %d errors %v, want %d", len(got), got, len(tc.want))
			}
			for i, e := range got {
				if e.Error() != tc.want[i] {
					t.Errorf("error %d = %q, want %q", i, e.Error(), tc.want[i])
				}
				if strings.Contains(e.Error(), raw) {
					t.Errorf("error %d leaks the raw value: %q", i, e.Error())
				}
			}
		})
	}
}

func TestLoadMigrate(t *testing.T) {
	t.Parallel()

	ownerURL := "postgres://app_owner:" + testDBPassword + "@db.internal:5432/app"
	cfg, err := loadMigrate(map[string]string{"APP_ENV": "production", "MIGRATIONS_DATABASE_URL": ownerURL})
	if err != nil {
		t.Fatalf("loadMigrate: %v", err)
	}
	if cfg.URL.Reveal() != ownerURL || cfg.Env != EnvProduction || cfg.Log.Format != LogFormatJSON {
		t.Fatalf("cfg = %v", cfg)
	}

	// Nothing of the API's configuration is needed: a deployment runs it with the owner's
	// credentials only. Problems are reported by variable, never with the value.
	_, err = loadMigrate(map[string]string{"APP_ENV": "qa", "MIGRATIONS_DATABASE_URL": "mysql://root:" + testDBPassword + "@db/app"})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"APP_ENV must be one of", "MIGRATIONS_DATABASE_URL must be a postgres"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not contain %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, testDBPassword) {
		t.Error("error leaks the password")
	}
	if _, err := loadMigrate(map[string]string{"APP_ENV": "development"}); err == nil ||
		!strings.Contains(err.Error(), "MIGRATIONS_DATABASE_URL is required") {
		t.Fatalf("without the URL: %v", err)
	}
}

func TestLoad_SMTP(t *testing.T) {
	t.Parallel()

	https := map[string]string{
		"FRONTEND_ORIGIN": "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
		"WORKOS_REDIRECT_URI": "https://app.example.com/auth/callback",
	}
	merge := func(ms ...map[string]string) map[string]string {
		out := map[string]string{}
		for _, m := range ms {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}

	t.Run("development without SMTP_HOST logs links instead", func(t *testing.T) {
		t.Parallel()
		cfg, err := load(validEnv())
		if err != nil || cfg.SMTP.Enabled() {
			t.Fatalf("load = %v, enabled = %v", err, cfg.SMTP.Enabled())
		}
	})

	t.Run("development may use Mailpit: no credentials, any certificate", func(t *testing.T) {
		t.Parallel()
		cfg, err := load(with(map[string]string{
			"SMTP_HOST": "localhost", "SMTP_PORT": "1025", "SMTP_FROM": "dev@localhost.test", "SMTP_TLS_SKIP_VERIFY": "true",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.SMTP.Enabled() || cfg.SMTP.TLS != SMTPStartTLS || cfg.SMTP.Timeout != 10*time.Second || !cfg.SMTP.TLSSkipVerify {
			t.Fatalf("smtp = %+v", cfg.SMTP)
		}
	})

	t.Run("deployed with implicit TLS", func(t *testing.T) {
		t.Parallel()
		cfg, err := load(deployed(merge(https, map[string]string{"APP_ENV": "production", "SMTP_TLS": "implicit", "SMTP_PORT": "465"})))
		if err != nil || cfg.SMTP.TLS != SMTPImplicitTLS || cfg.SMTP.SMTPPort != 465 {
			t.Fatalf("load = %v, smtp = %+v", err, cfg.SMTP)
		}
	})

	for _, tc := range []struct {
		name    string
		environ map[string]string
		want    []string
	}{
		{
			"required outside development",
			with(merge(https, map[string]string{"APP_ENV": "staging"})),
			[]string{"SMTP_HOST is required outside development"},
		},
		{
			"credentials required outside development, certificates always verified",
			deployed(merge(https, map[string]string{
				"APP_ENV": "production", "SMTP_USERNAME": "", "SMTP_PASSWORD": "", "SMTP_TLS_SKIP_VERIFY": "true",
			})),
			[]string{"SMTP_USERNAME and SMTP_PASSWORD are required outside development", "SMTP_TLS_SKIP_VERIFY is only allowed in development"},
		},
		{
			"no plain-text mode, and every value checked",
			with(map[string]string{
				"SMTP_HOST": "smtp://mail.example.com", "SMTP_PORT": "0", "SMTP_FROM": "not an address",
				"SMTP_TLS": "none", "SMTP_TIMEOUT": "0s", "SMTP_USERNAME": "only-user",
			}),
			[]string{
				"SMTP_TLS must be one of starttls, implicit", "SMTP_HOST must be a host name", "SMTP_PORT must be between",
				"SMTP_FROM must be an email address", "SMTP_TIMEOUT must be greater than zero", "SMTP_USERNAME and SMTP_PASSWORD go together",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := load(tc.environ)
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error does not contain %q:\n%s", w, msg)
				}
			}
			if n := strings.Count(msg, "\n") + 1; n != len(tc.want) {
				t.Errorf("got %d errors, want %d:\n%s", n, len(tc.want), msg)
			}
			if strings.Contains(msg, testSMTPPassword) {
				t.Error("error leaks the SMTP password")
			}
		})
	}
}

func TestLoad_Telemetry(t *testing.T) {
	t.Parallel()

	const dsnKey = "examplepublickey"
	cfg, err := load(with(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4318", "SENTRY_DSN": "https://" + dsnKey + "@o1.ingest.sentry.io/42",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Telemetry.TracesEnabled() || !cfg.Telemetry.MetricsEnabled() {
		t.Fatalf("telemetry = %+v", cfg.Telemetry)
	}
	if strings.Contains(cfg.String(), dsnKey) || strings.Contains(fmt.Sprintf("%+v", cfg), dsnKey) {
		t.Fatal("the Sentry DSN is rendered")
	}

	if cfg, err := load(validEnv()); err != nil || cfg.Telemetry.TracesEnabled() || cfg.Telemetry.MetricsEnabled() {
		t.Fatalf("without endpoints: %v %+v", err, cfg.Telemetry)
	}
	if cfg, err := load(with(map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://otlp.example.com/v1/traces"})); err != nil ||
		!cfg.Telemetry.TracesEnabled() || cfg.Telemetry.MetricsEnabled() {
		t.Fatalf("traces only: %v %+v", err, cfg.Telemetry)
	}

	_, err = load(with(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "localhost:4317", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc", "SENTRY_DSN": "not-a-dsn",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT must be", "OTEL_EXPORTER_OTLP_PROTOCOL must be http/protobuf", "SENTRY_DSN must be"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q: %v", want, err)
		}
	}
}
