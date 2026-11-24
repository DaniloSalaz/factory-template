package smtpsender

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"unicode"
	"unicode/utf8"

	"chaskicode.dev/saas/platform/identity/usecases"
)

// Subject is fixed: the tenant's and the inviter's names are written by users, and in a
// subject they would let anyone send phishing ("Your account is locked") from our
// domain. In the body they are escaped, cut short and clearly someone's name.
const Subject = "Tienes una invitación"

// maxNameRunes bounds a user-written name in the email.
const maxNameRunes = 80

// content is an invitation email in Spanish, as plain text and as HTML.
type content struct {
	Text, HTML string
}

// compose renders msg. Names lose control characters (no line breaks to fake
// paragraphs) and are cut to maxNameRunes; html/template escapes them in the HTML.
func compose(tmpl *template.Template, msg usecases.InvitationMessage) (content, error) {
	v := view{
		Inviter: cleanName(msg.InviterName),
		Tenant:  cleanName(msg.TenantName),
		Role:    cleanName(msg.Role),
		Link:    msg.Link.Reveal(),
	}
	var html bytes.Buffer
	if err := tmpl.Execute(&html, v); err != nil {
		return content{}, fmt.Errorf("render invitation html: %w", err)
	}
	text := fmt.Sprintf(textBody, v.Inviter, v.Tenant, v.Role, v.Link)
	return content{Text: text, HTML: html.String()}, nil
}

type view struct {
	Inviter, Tenant, Role string
	// Link is a URL of our front end with the token in the fragment; html/template
	// still treats it as untrusted in href.
	Link string
}

// cleanName keeps a user-written name on one line, without control or format
// characters (bidi overrides could reorder the text), cut to maxNameRunes.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxNameRunes {
		s = string([]rune(s)[:maxNameRunes-1]) + "…"
	}
	return s
}

const textBody = `Hola:

%s te ha invitado a unirte a «%s» con el rol %s.

Para aceptar la invitación, abre este enlace (caduca en 7 días):
%s

Si no esperabas esta invitación, ignora este mensaje: sin abrir el enlace no ocurre nada.
`

// parseHTML parses the HTML body, once per Sender.
func parseHTML() (*template.Template, error) {
	t, err := template.New("invitation").Parse(htmlBody)
	if err != nil {
		return nil, fmt.Errorf("parse invitation html: %w", err)
	}
	return t, nil
}

const htmlBody = `<!doctype html>
<html lang="es">
<head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>` + Subject + `</title></head>
<body style="font-family: system-ui, sans-serif; line-height: 1.5; color: #111;">
<p>Hola:</p>
<p><strong>{{.Inviter}}</strong> te ha invitado a unirte a <strong>{{.Tenant}}</strong> con el rol <strong>{{.Role}}</strong>.</p>
<p><a href="{{.Link}}">Aceptar la invitación</a></p>
<p>Si el botón no funciona, copia este enlace en tu navegador (caduca en 7 días):<br>{{.Link}}</p>
<p style="color: #555;">Si no esperabas esta invitación, ignora este mensaje: sin abrir el enlace no ocurre nada.</p>
</body>
</html>
`
