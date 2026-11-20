package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testDBPassword = "hunter2-db-password"

func validEnv() map[string]string {
	return map[string]string{
		"APP_ENV":      "development",
		"DATABASE_URL": "postgres://app:" + testDBPassword + "@localhost:5432/app?sslmode=disable&password=" + testDBPassword,
	}
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
					c.HTTP.ShutdownTimeout == 10*time.Second && c.Log.Level == slog.LevelInfo &&
					c.Log.Format == LogFormatJSON
			},
		},
		{
			name: "overrides",
			environ: with(map[string]string{
				"APP_ENV": "production", "HTTP_PORT": "9090", "HTTP_WRITE_TIMEOUT": "30s",
				"LOG_LEVEL": "debug", "LOG_FORMAT": "text", "DATABASE_URL": "postgresql://db.internal/app",
			}),
			want: func(c Config) bool {
				return c.Env == EnvProduction && c.HTTP.Port == 9090 && c.HTTP.WriteTimeout == 30*time.Second &&
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
			name:     "missing every required variable",
			environ:  map[string]string{},
			wantVars: []string{"APP_ENV is required", "DATABASE_URL is required"},
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
				"DATABASE_URL": "mysql://root:" + testDBPassword + "@db/app",
			}),
			wantVars: []string{"APP_ENV must be one of", "HTTP_PORT must be between", "HTTP_IDLE_TIMEOUT must be greater", "LOG_FORMAT must be one of", "DATABASE_URL must be a postgres"},
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
			for _, secret := range append(tc.secrets, testDBPassword) {
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

func TestConfig_RenderingsMaskDatabaseURL(t *testing.T) {
	t.Parallel()

	cfg, err := load(validEnv())
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
