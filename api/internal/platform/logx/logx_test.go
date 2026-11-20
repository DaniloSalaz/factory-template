package logx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

func jsonLogger(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return logx.New(&buf, config.Log{Level: level, Format: config.LogFormatJSON}), &buf
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSON log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestNew_FormatAndLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       config.Log
		wantLines int
		wantStart string
	}{
		{"json at info drops debug", config.Log{Level: slog.LevelInfo, Format: config.LogFormatJSON}, 1, `{"time"`},
		{"json at debug", config.Log{Level: slog.LevelDebug, Format: config.LogFormatJSON}, 2, `{"time"`},
		{"text", config.Log{Level: slog.LevelInfo, Format: config.LogFormatText}, 1, `time=`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := logx.New(&buf, tc.cfg)
			logger.Debug("debug")
			logger.Info("info")

			out := buf.String()
			if got := strings.Count(out, "\n"); got != tc.wantLines {
				t.Fatalf("lines = %d, want %d:\n%s", got, tc.wantLines, out)
			}
			if !strings.HasPrefix(out, tc.wantStart) {
				t.Fatalf("output %q does not start with %q", out, tc.wantStart)
			}
		})
	}
}

func TestWith_AttrsFollowTheContext(t *testing.T) {
	t.Parallel()

	base := logx.With(t.Context(), slog.String("tenant", "t1"))
	child := logx.With(base, slog.String("user", "u1"))
	withID := logx.WithRequestID(child, "req-123")

	tests := []struct {
		name    string
		ctx     context.Context
		want    map[string]string
		missing []string
	}{
		{"no context attrs", t.Context(), nil, []string{"tenant", "user", "request_id"}},
		{"one level", base, map[string]string{"tenant": "t1"}, []string{"user"}},
		{"accumulates without touching the parent", child, map[string]string{"tenant": "t1", "user": "u1"}, []string{"request_id"}},
		{"request id", withID, map[string]string{"tenant": "t1", "user": "u1", "request_id": "req-123"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger, buf := jsonLogger(slog.LevelInfo)
			logger.InfoContext(tc.ctx, "hello")

			line := decodeLines(t, buf)[0]
			for k, v := range tc.want {
				if line[k] != v {
					t.Errorf("%s = %v, want %q", k, line[k], v)
				}
			}
			for _, k := range tc.missing {
				if _, ok := line[k]; ok {
					t.Errorf("unexpected key %q in %v", k, line)
				}
			}
		})
	}
}

func TestRequestIDFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ctx    context.Context
		wantID string
		wantOK bool
	}{
		{"absent", t.Context(), "", false},
		{"empty is absent", logx.WithRequestID(t.Context(), ""), "", false},
		{"present", logx.WithRequestID(t.Context(), "abc"), "abc", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, ok := logx.RequestIDFrom(tc.ctx)
			if id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("RequestIDFrom = (%q, %v), want (%q, %v)", id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestNew_RedactsSensitiveValues(t *testing.T) {
	t.Parallel()

	const raw = "very-secret-value"
	tests := []struct {
		name string
		log  func(*slog.Logger)
	}{
		{"password", func(l *slog.Logger) { l.Info("m", slog.String("password", raw)) }},
		{"case and dash insensitive", func(l *slog.Logger) { l.Info("m", slog.String("Set-Cookie", raw)) }},
		{"authorization header", func(l *slog.Logger) { l.Info("m", slog.String("Authorization", "Bearer "+raw)) }},
		{"inside a group", func(l *slog.Logger) { l.Info("m", slog.Group("req", slog.String("api_key", raw))) }},
		{"email inside a group", func(l *slog.Logger) { l.Info("m", slog.Group("user", slog.String("email", raw))) }},
		{"camelCase key", func(l *slog.Logger) { l.Info("m", slog.String("userPassword", raw)) }},
		{"logger.With", func(l *slog.Logger) { l.With(slog.String("token", raw)).Info("m") }},
		{"context attrs", func(l *slog.Logger) {
			l.InfoContext(logx.With(context.Background(), slog.String("database_url", raw)), "m")
		}},
		{"pii email", func(l *slog.Logger) { l.Info("m", slog.Any("email", pii.Email(raw+"@dominio.com"))) }},
		{"pii secret", func(l *slog.Logger) { l.Info("m", slog.Any("anything", pii.Secret(raw))) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger, buf := jsonLogger(slog.LevelInfo)
			tc.log(logger)
			if out := buf.String(); strings.Contains(out, raw) {
				t.Fatalf("log leaks the value: %s", out)
			}
		})
	}
}

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want bool
	}{
		{"password", true},
		{"PASSWORD", true},
		{"user_email", true},
		{"userPassword", true},
		{"X-Api-Key", true},
		{"x_api_key", true},
		{"my_api_key", true},
		{"APIKey", true},
		{"Authorization", true},
		{"customer_dni", true},
		{"userAccessToken", true},
		{"db_private_key", true},
		{"set-cookie", true},
		{"billing.iban", true},
		{"shipping_address", true},
		{"phoneNumber", true},
		{"name", false},
		{"api_version", false},
		{"user_id", false},
		{"passwordless", false},
		{"keyboard", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if got := logx.IsSensitiveKey(tc.key); got != tc.want {
				t.Fatalf("IsSensitiveKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// Documents a slog property: context attributes are added to the record, so a logger
// created with WithGroup nests them inside that group. AGENTS.md therefore forbids
// WithGroup on the base logger.
func TestWithGroup_NestsContextAttrs(t *testing.T) {
	t.Parallel()

	logger, buf := jsonLogger(slog.LevelInfo)
	ctx := logx.WithRequestID(logx.With(t.Context(), slog.String("tenant", "t1")), "req-1")
	logger.WithGroup("job").InfoContext(ctx, "m", slog.Int("attempt", 2))

	line := decodeLines(t, buf)[0]
	group, ok := line["job"].(map[string]any)
	if !ok {
		t.Fatalf("missing job group: %v", line)
	}
	for _, k := range []string{"tenant", "request_id", "attempt"} {
		if _, ok := group[k]; !ok {
			t.Errorf("%s not inside the group: %v", k, line)
		}
		if _, ok := line[k]; ok {
			t.Errorf("%s unexpectedly at top level: %v", k, line)
		}
	}
}

func TestAccessAttrs(t *testing.T) {
	t.Parallel()

	// Without a container, adding is a no-op rather than a panic.
	logx.AddAccessAttrs(t.Context(), slog.String("user_id", "u"))

	ctx, added := logx.WithAccessAttrs(t.Context())
	// A derived context still reaches the same container: that is how inner middlewares
	// pass attributes out to the outer one.
	inner := logx.With(ctx, slog.String("other", "x"))
	logx.AddAccessAttrs(inner, slog.String("user_id", "u1"))
	logx.AddAccessAttrs(inner, slog.String("tenant_id", "t1"))

	got := added()
	if len(got) != 2 || got[0].Key != "user_id" || got[1].Key != "tenant_id" {
		t.Fatalf("added = %v, want user_id and tenant_id", got)
	}

	// Access attributes do not leak into other records.
	logger, buf := jsonLogger(slog.LevelDebug)
	logger.InfoContext(inner, "inside")
	if line := decodeLines(t, buf)[0]; line["user_id"] != nil || line["tenant_id"] != nil {
		t.Fatalf("access attributes leaked into a normal record: %v", line)
	}
}
