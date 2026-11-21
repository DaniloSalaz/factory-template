// Package devsender is the development adapter of app.InvitationSender: it writes the
// invitation link to the log instead of emailing it. It is the only place where a
// token reaches a log, so it refuses to exist outside development, where the SMTP
// adapter sends real email (design, 5.6.7).
package devsender

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
)

// LogMessage is the message of the log record that carries the link.
const LogMessage = "invitation link (development only)"

// Sender logs each invitation link at Info.
type Sender struct {
	logger *slog.Logger
}

var _ app.InvitationSender = (*Sender)(nil)

// New builds a Sender, or fails outside development: anywhere else the logs would hold
// working invitation tokens.
func New(env config.Environment, logger *slog.Logger) (*Sender, error) {
	if env != config.EnvDevelopment {
		return nil, fmt.Errorf("devsender: the development invitation sender logs tokens and cannot run in %q", env)
	}
	return &Sender{logger: logger}, nil
}

// Send implements app.InvitationSender. The email stays masked (pii.Email); the link is
// revealed on purpose: reading it from the log is how invitations work in development.
func (s *Sender) Send(ctx context.Context, msg app.InvitationMessage) error {
	s.logger.InfoContext(ctx, LogMessage,
		slog.Any("email", msg.To), slog.String("tenant", msg.TenantName), slog.String("role", msg.Role),
		slog.String("link", msg.Link.Reveal()))
	return nil
}
