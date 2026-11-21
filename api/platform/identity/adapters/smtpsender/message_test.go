package smtpsender

import (
	"strings"
	"testing"
	"unicode/utf8"

	"chaskicode.dev/saas/platform/identity/usecases"
	"chaskicode.dev/saas/platform/kit/pii"
)

const testToken = "not-a-real-invitation-token"

func testMessage(inviter, tenant string) usecases.InvitationMessage {
	return usecases.InvitationMessage{
		To: pii.Email("ana@example.com"), Link: pii.Secret("https://app.example.com/invite#token=" + testToken),
		TenantName: tenant, Role: "admin", InviterName: inviter,
	}
}

func mustCompose(t *testing.T, msg usecases.InvitationMessage) content {
	t.Helper()
	tmpl, err := parseHTML()
	if err != nil {
		t.Fatal(err)
	}
	c, err := compose(tmpl, msg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCompose_SaysWhoWhereAndWhichRole(t *testing.T) {
	t.Parallel()
	c := mustCompose(t, testMessage("Bea Ruiz", "Despacho Pérez"))
	for name, body := range map[string]string{"text": c.Text, "html": c.HTML} {
		for _, want := range []string{"Bea Ruiz", "Despacho Pérez", "admin", "caduca en 7 días"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body lacks %q:\n%s", name, want, body)
			}
		}
	}
	if !strings.Contains(c.HTML, `href="https://app.example.com/invite#token=`+testToken+`"`) {
		t.Errorf("html has no link:\n%s", c.HTML)
	}
}

// User-written names are escaped in HTML and cannot add markup, links or lines.
func TestCompose_EscapesUserWrittenNames(t *testing.T) {
	t.Parallel()
	c := mustCompose(t, testMessage(`<a href="https://evil.example">Soporte</a>`, "Acme<script>alert(1)</script>\r\n\r\nVisita evil.example"))
	for _, bad := range []string{"<script>", `<a href="https://evil.example"`, "\r"} {
		if strings.Contains(c.HTML, bad) {
			t.Errorf("html contains %q:\n%s", bad, c.HTML)
		}
	}
	if !strings.Contains(c.HTML, "&lt;script&gt;") || !strings.Contains(c.HTML, "&lt;a href=") {
		t.Errorf("names are not escaped:\n%s", c.HTML)
	}
	// In the text the name stays on its own line: no fake paragraph after it.
	if strings.Contains(c.Text, "</script>\n") || strings.Contains(c.Text, "\r") {
		t.Errorf("text has a line break from a name:\n%q", c.Text)
	}
}

func TestCleanName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"\tAna   Pérez\t":          "Ana Pérez",
		"Ana\nPérez\t":             "Ana Pérez",
		"evil\u202egnp.exe":        "evil gnp.exe", // a bidi override would reverse the text
		"\x00\x07":                 "",
		strings.Repeat("ñ", 200):   strings.Repeat("ñ", maxNameRunes-1) + "…",
		strings.Repeat("ab ", 100): strings.TrimSpace(strings.Repeat("ab ", 100))[:maxNameRunes-1] + "…",
	} {
		got := cleanName(in)
		if got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
		if utf8.RuneCountInString(got) > maxNameRunes {
			t.Errorf("cleanName(%q) is %d runes long", in, utf8.RuneCountInString(got))
		}
	}
}

// The subject is fixed: no user-written name reaches a header.
func TestSubject_HasNoUserInput(t *testing.T) {
	t.Parallel()
	if strings.ContainsAny(Subject, "%{}") || Subject == "" {
		t.Fatalf("Subject = %q", Subject)
	}
}

// The token appears only inside the link, right after #token=.
func TestCompose_TokenOnlyInTheFragment(t *testing.T) {
	t.Parallel()
	c := mustCompose(t, testMessage("Bea", "Acme"))
	for name, body := range map[string]string{"text": c.Text, "html": c.HTML} {
		parts := strings.Split(body, testToken)
		if len(parts) < 2 {
			t.Fatalf("%s body has no token", name)
		}
		for _, before := range parts[:len(parts)-1] {
			if !strings.HasSuffix(before, "/invite#token=") {
				t.Errorf("%s body has the token outside the fragment: …%s", name, before[max(0, len(before)-40):])
			}
		}
	}
}
