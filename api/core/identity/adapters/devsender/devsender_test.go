package devsender_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"chaskicode.dev/saas/core/identity/adapters/devsender"
	"chaskicode.dev/saas/core/identity/usecases"
	"chaskicode.dev/saas/core/kit/config"
	"chaskicode.dev/saas/core/kit/logx"
	"chaskicode.dev/saas/core/kit/pii"
)

func TestNew_OnlyInDevelopment(t *testing.T) {
	t.Parallel()
	for _, env := range []config.Environment{config.EnvStaging, config.EnvProduction, ""} {
		if _, err := devsender.New(env, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("New(%q) succeeded, want an error", env)
		}
	}
}

func TestSend_LogsTheLinkWithTheEmailMasked(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s, err := devsender.New(config.EnvDevelopment, logx.New(&buf, config.Log{Level: slog.LevelInfo, Format: config.LogFormatJSON}))
	if err != nil {
		t.Fatal(err)
	}
	const link = "http://localhost:5173/invite#token=abc"
	msg := usecases.InvitationMessage{
		To: pii.Email("ana@example.com"), Link: pii.Secret(link), TenantName: "Acme", Role: "admin", InviterName: "Bea",
	}
	if err := s.Send(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["msg"] != devsender.LogMessage || rec["link"] != link || rec["role"] != "admin" || rec["tenant"] != "Acme" {
		t.Fatalf("record = %v", rec)
	}
	if strings.Contains(buf.String(), "ana@example.com") {
		t.Fatalf("the email reached the log unmasked: %s", buf.String())
	}
}
