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
		{"api-key", true},
		{"refresh_token", true},
		{"user_id", false},
		{"passwordless", false},
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
