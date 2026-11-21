// Package smtpsender is the SMTP adapter of app.InvitationSender (design, 5.6.7): a
// plain SMTP relay, of whatever provider, over TLS only. It is the sender outside
// development, where devsender may not run.
package smtpsender

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"html/template"

	"github.com/wneessen/go-mail"
	"go.opentelemetry.io/otel/trace"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/telemetry"
)

// Sender emails invitations through an SMTP relay.
type Sender struct {
	cfg     config.SMTP
	rootCAs *x509.CertPool
	html    *template.Template
	tracers trace.TracerProvider
}

var _ app.InvitationSender = (*Sender)(nil)

// New builds a Sender from a validated configuration. rootCAs replaces the system's
// trusted roots; nil keeps them (only tests pass their own). tracers traces each send as
// a client span (peer.service smtp); nil traces nothing.
func New(cfg config.SMTP, rootCAs *x509.CertPool, tracers trace.TracerProvider) (*Sender, error) {
	if !cfg.Enabled() {
		return nil, errors.New("smtpsender: SMTP_HOST is not set")
	}
	html, err := parseHTML()
	if err != nil {
		return nil, fmt.Errorf("smtpsender: %w", err)
	}
	return &Sender{cfg: cfg, rootCAs: rootCAs, html: html, tracers: tracers}, nil
}

// Send implements app.InvitationSender. The link travels only in the message body: no
// error it returns names the link, the token or the recipient.
func (s *Sender) Send(ctx context.Context, msg app.InvitationMessage) (err error) {
	ctx, end := telemetry.ClientSpan(ctx, s.tracers, "smtp", "send")
	defer func() { end(err) }()
	body, err := compose(s.html, msg)
	if err != nil {
		return err
	}
	m := mail.NewMsg()
	if err := m.From(s.cfg.From); err != nil {
		return fmt.Errorf("invitation email: sender address: %w", err)
	}
	if err := m.To(msg.To.Reveal()); err != nil {
		// go-mail's error quotes the address; the invitee's email stays out of the logs.
		return errors.New("invitation email: the recipient address is not valid")
	}
	m.Subject(Subject)
	m.SetMessageID()
	m.SetDate()
	m.SetBodyString(mail.TypeTextPlain, body.Text)
	m.AddAlternativeString(mail.TypeTextHTML, body.HTML)

	client, err := mail.NewClient(s.cfg.Host, s.options()...)
	if err != nil {
		return fmt.Errorf("invitation email: smtp client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return fmt.Errorf("invitation email: send through %s: %w", s.cfg.Host, withoutRecipients(err))
	}
	return nil
}

// withoutRecipients keeps from a go-mail send error only its reason and SMTP code: its
// text lists the recipients, and the server's replies may quote them too ("550
// <ana@…>: no such user"). Connection and TLS errors carry no address and stay as they
// are.
func withoutRecipients(err error) error {
	var sendErr *mail.SendError
	if !errors.As(err, &sendErr) {
		return err
	}
	return fmt.Errorf("%s (smtp code %d, temporary: %t)", sendErr.Reason, sendErr.ErrorCode(), sendErr.IsTemp())
}

// options is TLS always (implicit, or STARTTLS that must succeed), timeouts and, when
// configured, authentication.
func (s *Sender) options() []mail.Option {
	tlsConfig := &tls.Config{
		ServerName: s.cfg.Host,
		MinVersion: tls.VersionTLS12,
		RootCAs:    s.rootCAs,
		// Only in development, for Mailpit's self-signed certificate: config refuses
		// SMTP_TLS_SKIP_VERIFY anywhere else.
		InsecureSkipVerify: s.cfg.TLSSkipVerify, //nolint:gosec // development only, enforced by config
	}
	opts := []mail.Option{
		mail.WithPort(s.cfg.SMTPPort),
		mail.WithTimeout(s.cfg.Timeout),
		mail.WithTLSConfig(tlsConfig),
		mail.WithTLSPolicy(mail.TLSMandatory),
	}
	if s.cfg.TLS == config.SMTPImplicitTLS {
		opts = append(opts, mail.WithSSL())
	}
	if s.cfg.Username != "" {
		opts = append(opts,
			mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover),
			mail.WithUsername(s.cfg.Username),
			mail.WithPassword(s.cfg.Password.Reveal()),
		)
	}
	return opts
}
