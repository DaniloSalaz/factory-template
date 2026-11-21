package smtpsender_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/smtpsender"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// The same image as compose.yaml.
const mailpitImage = "axllent/mailpit:v1.34.0"

const token = "integration-test-invitation-token"

// certificate writes a self-signed certificate for localhost and 127.0.0.1 and returns
// its files and a pool that trusts it: the sender verifies it as it would a real one.
func certificate(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

type mailpit struct {
	host          string
	smtpPort, api int
}

// startMailpit runs Mailpit with env; with tls, the certificate is mounted for it.
func startMailpit(t *testing.T, env map[string]string, certFile, keyFile string) mailpit {
	t.Helper()
	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
		testcontainers.WithEnv(env),
		testcontainers.WithWaitStrategy(wait.ForAll(wait.ForListeningPort("1025/tcp"), wait.ForListeningPort("8025/tcp"))),
	}
	if certFile != "" {
		opts = append(opts, testcontainers.WithFiles(
			testcontainers.ContainerFile{HostFilePath: certFile, ContainerFilePath: "/certs/cert.pem", FileMode: 0o644},
			testcontainers.ContainerFile{HostFilePath: keyFile, ContainerFilePath: "/certs/key.pem", FileMode: 0o644},
		))
	}
	ctr, err := testcontainers.Run(t.Context(), mailpitImage, opts...)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("run mailpit: %v", err)
	}
	host, err := ctr.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	smtp, err := ctr.MappedPort(t.Context(), "1025/tcp")
	if err != nil {
		t.Fatal(err)
	}
	api, err := ctr.MappedPort(t.Context(), "8025/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return mailpit{host: host, smtpPort: int(smtp.Num()), api: int(api.Num())}
}

func (m mailpit) get(t *testing.T, path string, out any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://%s:%d%s", m.host, m.api, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", path, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func smtpConfig(m mailpit, mode config.SMTPTLS) config.SMTP {
	return config.SMTP{
		Host: m.host, SMTPPort: m.smtpPort, Username: "relay-user", Password: pii.Secret("relay-password"),
		From: "Factory <no-reply@example.test>", TLS: mode, Timeout: 10 * time.Second,
	}
}

func message() app.InvitationMessage {
	return app.InvitationMessage{
		To: pii.Email("ana@example.test"), Link: pii.Secret("https://app.example.test/invite#token=" + token),
		TenantName: `Acme <script>alert("x")</script>`, Role: "admin", InviterName: `Bea <img src=x onerror=alert(1)>`,
	}
}

func TestSender_Mailpit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	certFile, keyFile, pool := certificate(t)

	for _, tc := range []struct {
		name string
		mode config.SMTPTLS
		env  map[string]string
	}{
		{"starttls", config.SMTPStartTLS, map[string]string{"MP_SMTP_REQUIRE_STARTTLS": "true"}},
		{"implicit tls", config.SMTPImplicitTLS, map[string]string{"MP_SMTP_REQUIRE_TLS": "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"MP_SMTP_TLS_CERT": "/certs/cert.pem", "MP_SMTP_TLS_KEY": "/certs/key.pem", "MP_SMTP_AUTH_ACCEPT_ANY": "true",
			}
			for k, v := range tc.env {
				env[k] = v
			}
			m := startMailpit(t, env, certFile, keyFile)
			sender, err := smtpsender.New(smtpConfig(m, tc.mode), pool)
			if err != nil {
				t.Fatal(err)
			}
			if err := sender.Send(t.Context(), message()); err != nil {
				t.Fatalf("Send: %v", err)
			}

			var list struct {
				Messages []struct {
					ID      string
					Subject string
					To      []struct{ Address string }
				}
			}
			m.get(t, "/api/v1/messages", &list)
			if len(list.Messages) != 1 {
				t.Fatalf("messages = %+v", list.Messages)
			}
			got := list.Messages[0]
			if got.Subject != smtpsender.Subject || len(got.To) != 1 || got.To[0].Address != "ana@example.test" {
				t.Fatalf("message = %+v", got)
			}
			var full struct{ HTML, Text string }
			m.get(t, "/api/v1/message/"+got.ID, &full)
			var headers map[string][]string
			m.get(t, "/api/v1/message/"+got.ID+"/headers", &headers)

			// User-written names are escaped: no markup of theirs in the HTML.
			for _, bad := range []string{"<script>", "<img src=x"} {
				if strings.Contains(full.HTML, bad) {
					t.Errorf("html contains %q:\n%s", bad, full.HTML)
				}
			}
			if !strings.Contains(full.HTML, "&lt;script&gt;") {
				t.Errorf("html does not show the escaped name:\n%s", full.HTML)
			}
			// The token is only after #token=, in both bodies, and in no header.
			for name, body := range map[string]string{"text": full.Text, "html": full.HTML} {
				parts := strings.Split(body, token)
				if len(parts) < 2 {
					t.Fatalf("%s body has no link:\n%s", name, body)
				}
				for _, before := range parts[:len(parts)-1] {
					if !strings.HasSuffix(before, "/invite#token=") {
						t.Errorf("%s body has the token outside the fragment", name)
					}
				}
			}
			for k, vs := range headers {
				for _, v := range vs {
					if strings.Contains(v, token) || strings.Contains(v, "Acme") || strings.Contains(v, "Bea") {
						t.Errorf("header %s carries the token or a user-written name: %q", k, v)
					}
				}
			}
		})
	}

	// TLS is mandatory: a server that offers no STARTTLS gets nothing, credentials
	// included, and the error names neither the token nor the recipient.
	t.Run("a server without TLS is refused", func(t *testing.T) {
		m := startMailpit(t, map[string]string{"MP_SMTP_AUTH_ACCEPT_ANY": "true", "MP_SMTP_AUTH_ALLOW_INSECURE": "true"}, "", "")
		sender, err := smtpsender.New(smtpConfig(m, config.SMTPStartTLS), pool)
		if err != nil {
			t.Fatal(err)
		}
		err = sender.Send(t.Context(), message())
		if err == nil {
			t.Fatal("Send succeeded without TLS")
		}
		t.Logf("refused with: %v", err)
		if msg := err.Error(); strings.Contains(msg, token) || strings.Contains(msg, "ana@example.test") {
			t.Fatalf("the error leaks the token or the recipient: %v", err)
		}
		var list struct{ Messages []struct{ ID string } }
		m.get(t, "/api/v1/messages", &list)
		if len(list.Messages) != 0 {
			t.Fatalf("a message reached the server: %+v", list.Messages)
		}
	})

	t.Run("a certificate nobody trusts is refused", func(t *testing.T) {
		m := startMailpit(t, map[string]string{
			"MP_SMTP_TLS_CERT": "/certs/cert.pem", "MP_SMTP_TLS_KEY": "/certs/key.pem",
			"MP_SMTP_REQUIRE_STARTTLS": "true", "MP_SMTP_AUTH_ACCEPT_ANY": "true",
		}, certFile, keyFile)
		sender, err := smtpsender.New(smtpConfig(m, config.SMTPStartTLS), x509.NewCertPool())
		if err != nil {
			t.Fatal(err)
		}
		if err := sender.Send(t.Context(), message()); err == nil {
			t.Fatal("Send trusted an unknown certificate")
		}
	})
}
