package workos

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

const testKey = "sk_test_never-leak-this-key"

func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(b) + "." + enc.EncodeToString([]byte("sig"))
}

// fakeWorkOS answers POST /user_management/authenticate with status and body, and
// records the request body.
func fakeWorkOS(t *testing.T, status int, body string) (*Provider, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var got bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user_management/authenticate" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(&got, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	p := New(Config{ClientID: "client_test", APIKey: pii.Secret(testKey), RedirectURI: "https://api.test/auth/callback", BaseURL: srv.URL},
		slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return p, &got, &logs
}

func TestAuthorizationURL(t *testing.T) {
	t.Parallel()
	p, _, _ := fakeWorkOS(t, http.StatusOK, "{}")
	raw, err := p.AuthorizationURL("state-1", "challenge-1")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	want := map[string]string{
		"client_id": "client_test", "redirect_uri": "https://api.test/auth/callback", "state": "state-1",
		"code_challenge": "challenge-1", "code_challenge_method": "S256", "response_type": "code",
		"provider": "authkit", "max_age": "0",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if u.Path != "/user_management/authorize" || strings.Contains(raw, testKey) {
		t.Fatalf("url = %s", raw)
	}
}

func TestExchangeCode(t *testing.T) {
	t.Parallel()
	authTime := time.Unix(1_800_000_000, 0).UTC()
	resp := func(sub string, claims map[string]any) string {
		token := jwt(t, claims)
		return fmt.Sprintf(`{"user":{"object":"user","id":%q,"email":"ana@example.test","email_verified":true,"first_name":"Ana","last_name":"Ruiz"},
			"access_token":%q,"refresh_token":"rt","authentication_method":"SSO"}`, sub, token)
	}

	t.Run("maps the user and the claims", func(t *testing.T) {
		t.Parallel()
		p, sent, logs := fakeWorkOS(t, http.StatusOK, resp("user_1", map[string]any{"sub": "user_1", "sid": "session_1", "auth_time": authTime.Unix()}))
		a, err := p.ExchangeCode(t.Context(), "code-1", "verifier-1")
		if err != nil {
			t.Fatal(err)
		}
		if a.Subject != "user_1" || a.Email.Reveal() != "ana@example.test" || !a.EmailVerified || a.DisplayName != "Ana Ruiz" ||
			a.SessionID != "session_1" || a.Method != "SSO" || !a.MFA.AuthTime.Equal(authTime) || a.MFA.CoveredByPolicy || a.MFA.HasAMR {
			t.Fatalf("authentication = %+v", a)
		}
		var body map[string]string
		if err := json.Unmarshal(sent.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["grant_type"] != "authorization_code" || body["code"] != "code-1" || body["code_verifier"] != "verifier-1" ||
			body["client_id"] != "client_test" || body["client_secret"] != testKey {
			t.Fatalf("request = %v", body)
		}
		if !strings.Contains(logs.String(), `"claims":["auth_time","sid","sub"]`) || strings.Contains(logs.String(), "session_1") {
			t.Fatalf("debug log must list claim names only: %s", logs.String())
		}
	})

	t.Run("reports amr when present", func(t *testing.T) {
		t.Parallel()
		p, _, _ := fakeWorkOS(t, http.StatusOK, resp("user_1", map[string]any{"sub": "user_1", "sid": "s", "auth_time": authTime.Unix(), "amr": []string{"pwd", "mfa"}}))
		a, err := p.ExchangeCode(t.Context(), "c", "v")
		if err != nil || !a.MFA.HasAMR || len(a.MFA.AMR) != 2 {
			t.Fatalf("authentication = %+v, %v", a, err)
		}
	})

	broken := map[string]map[string]any{
		"sub of another user": {"sub": "user_2", "sid": "s", "auth_time": authTime.Unix()},
		"no auth_time":        {"sub": "user_1", "sid": "s"},
	}
	for name, claims := range broken {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, _, _ := fakeWorkOS(t, http.StatusOK, resp("user_1", claims))
			_, err := p.ExchangeCode(t.Context(), "c", "v")
			if e, ok := apperr.As(err); !ok || e.Code != "auth.provider_unavailable" {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// Only allowlisted methods count as MFA, and with MFA Required at the provider they give
// mfa_auth_time; any other method, unknown ones included, gives none (design, 5.2).
func TestExchangeCode_MFAAllowlist(t *testing.T) {
	t.Parallel()
	authTime := time.Unix(1_800_000_000, 0).UTC()
	tests := map[string]bool{
		"Password":       true,
		"MagicAuth":      false,
		"GoogleOAuth":    false,
		"MicrosoftOAuth": false,
		"Passkey":        false,
		"SSO":            false,
		"InventedAuth":   false,
		"":               false,
	}
	for method, wantMFA := range tests {
		t.Run("method "+method, func(t *testing.T) {
			t.Parallel()
			methodJSON := ""
			if method != "" {
				methodJSON = fmt.Sprintf(`,"authentication_method":%q`, method)
			}
			token := jwt(t, map[string]any{"sub": "user_1", "sid": "s", "auth_time": authTime.Unix()})
			p, _, _ := fakeWorkOS(t, http.StatusOK, fmt.Sprintf(
				`{"user":{"id":"user_1","email":"ana@example.test","email_verified":true},"access_token":%q%s}`, token, methodJSON))
			a, err := p.ExchangeCode(t.Context(), "c", "v")
			if err != nil {
				t.Fatal(err)
			}
			_, gotMFA, err := domain.MFAAuthTime(a.MFA, true)
			if err != nil || gotMFA != wantMFA {
				t.Fatalf("with WORKOS_MFA_REQUIRED=true, MFA = %v (%v), want %v", gotMFA, err, wantMFA)
			}
		})
	}
}

func TestExchangeCode_ErrorsNeverCarryTheAPIKey(t *testing.T) {
	t.Parallel()
	// A response that echoes the request, key included: the error must not keep it.
	echo := `{"error":"invalid_grant","error_description":"bad code for client_secret=` + testKey + `"}`
	for status, code := range map[int]string{
		http.StatusBadRequest:          "auth.code_invalid",
		http.StatusUnauthorized:        "auth.provider_unavailable",
		http.StatusInternalServerError: "auth.provider_unavailable",
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			p, _, _ := fakeWorkOS(t, status, echo)
			_, err := p.ExchangeCode(t.Context(), "c", "v")
			if e, ok := apperr.As(err); !ok || e.Code != code {
				t.Fatalf("err = %v, want %s", err, code)
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(fmt.Sprintf("%+v", err), testKey) {
				t.Fatalf("the error leaks the API key: %v", err)
			}
		})
	}
}

func TestLogoutURL(t *testing.T) {
	t.Parallel()
	p, _, _ := fakeWorkOS(t, http.StatusOK, "{}")
	u, err := p.LogoutURL("session_1")
	if err != nil || !strings.HasSuffix(u, "/user_management/sessions/logout?session_id=session_1") {
		t.Fatalf("LogoutURL = %q, %v", u, err)
	}
	if _, err := p.LogoutURL(""); err == nil {
		t.Fatal("an empty session id must fail")
	}
}
