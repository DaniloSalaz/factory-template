package identity_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/devsender"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/smtpsender"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
)

// Outside development the links go by email or the API does not start: the log sender
// writes working tokens.
func TestNewInvitationSender(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	smtp := config.SMTP{Host: "smtp.example.com", SMTPPort: 587, From: "no-reply@example.com", TLS: config.SMTPStartTLS, Timeout: time.Second}

	if s, err := identity.NewInvitationSender(config.EnvDevelopment, config.SMTP{}, logger); err != nil {
		t.Fatalf("development without SMTP: %v", err)
	} else if _, ok := s.(*devsender.Sender); !ok {
		t.Fatalf("development without SMTP: %T, want the log sender", s)
	}
	for _, env := range []config.Environment{config.EnvDevelopment, config.EnvStaging, config.EnvProduction} {
		s, err := identity.NewInvitationSender(env, smtp, logger)
		if err != nil {
			t.Fatalf("%s with SMTP: %v", env, err)
		}
		if _, ok := s.(*smtpsender.Sender); !ok {
			t.Fatalf("%s with SMTP: %T, want the SMTP sender", env, s)
		}
	}
	for _, env := range []config.Environment{config.EnvStaging, config.EnvProduction} {
		if _, err := identity.NewInvitationSender(env, config.SMTP{}, logger); err == nil {
			t.Fatalf("%s without SMTP: a sender, want an error", env)
		}
	}
}
