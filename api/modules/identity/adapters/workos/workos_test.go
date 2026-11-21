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

	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
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
			a.SessionID != "session_1" || a.Method != "SSO" || !a.AuthTime.Equal(authTime) {
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

	t.Run("reports impersonation", func(t *testing.T) {
		t.Parallel()
		body := strings.Replace(resp("user_1", map[string]any{"sub": "user_1", "sid": "s", "auth_time": authTime.Unix()}),
			`"refresh_token":"rt"`, `"refresh_token":"rt","impersonator":{"email":"admin@workos.test","reason":"support"}`, 1)
		p, _, _ := fakeWorkOS(t, http.StatusOK, body)
		a, err := p.ExchangeCode(t.Context(), "c", "v")
		if err != nil || !a.Impersonated {
			t.Fatalf("authentication = %+v, %v; want Impersonated", a, err)
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

func TestExchangeCode_PendingStepsHaveTheirOwnCodes(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		`{"code":"email_verification_required","message":"Email ana@example.test must be verified ` + testKey + `","email":"ana@example.test"}`: "auth.email_verification_required",
		`{"code":"mfa_enrollment","message":"User must enroll in MFA ` + testKey + `","user":{"id":"user_1"}}`:                                  "auth.mfa_enrollment_required",
	}
	for body, code := range tests {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			p, _, _ := fakeWorkOS(t, http.StatusForbidden, body)
			_, err := p.ExchangeCode(t.Context(), "c", "v")
			e, ok := apperr.As(err)
			if !ok || e.Code != code || e.Kind != apperr.KindForbidden || e.Message == "" {
				t.Fatalf("err = %v, want %s (403) with a message", err, code)
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "ana@example.test") {
				t.Fatalf("the error leaks the response body: %v", err)
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

// fakeMFA answers the WorkOS MFA API: one route per call, each with its status and body,
// and records the requests.
func fakeMFA(t *testing.T, routes map[string]struct {
	status int
	body   string
},
) (*Provider, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		seen = append(seen, key+" "+string(b))
		route, ok := routes[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(route.status)
		_, _ = io.WriteString(w, route.body)
	}))
	t.Cleanup(srv.Close)
	p := New(Config{ClientID: "client_test", APIKey: pii.Secret(testKey), BaseURL: srv.URL}, slog.New(slog.DiscardHandler))
	return p, &seen
}

type route = struct {
	status int
	body   string
}

func TestMFA(t *testing.T) {
	t.Parallel()

	p, seen := fakeMFA(t, map[string]route{
		"POST /user_management/users/user_1/auth_factors": {http.StatusCreated, `{
			"authentication_factor":{"object":"authentication_factor","id":"auth_factor_1","type":"totp",
				"totp":{"issuer":"Acme","user":"ana@example.test","secret":"JBSWY3DPEHPK3PXP","qr_code":"data:image/png;base64,AAA","uri":"otpauth://totp/x?secret=JBSWY3DPEHPK3PXP"}},
			"authentication_challenge":{"object":"authentication_challenge","id":"auth_challenge_1","authentication_factor_id":"auth_factor_1"}}`},
		"POST /auth/factors/auth_factor_1/challenge":         {http.StatusCreated, `{"id":"auth_challenge_2","authentication_factor_id":"auth_factor_1"}`},
		"POST /auth/challenges/auth_challenge_2/verify":      {http.StatusOK, `{"challenge":{"id":"auth_challenge_2"},"valid":true}`},
		"POST /auth/challenges/auth_challenge_3/verify":      {http.StatusOK, `{"challenge":{"id":"auth_challenge_3"},"valid":false}`},
		"DELETE /auth/factors/auth_factor_1":                 {http.StatusNoContent, ``},
		"DELETE /auth/factors/auth_factor_gone":              {http.StatusNotFound, `{"message":"not found"}`},
		"POST /auth/challenges/auth_challenge_broken/verify": {http.StatusInternalServerError, `{"message":"boom"}`},
	})
	ctx := t.Context()

	e, err := p.EnrollTOTP(ctx, "user_1")
	if err != nil || e.FactorID != "auth_factor_1" || e.ChallengeID != "auth_challenge_1" ||
		e.Secret.Reveal() != "JBSWY3DPEHPK3PXP" || !strings.HasPrefix(e.QRCode.Reveal(), "data:image/png") {
		t.Fatalf("EnrollTOTP = %+v, %v", e, err)
	}
	if !strings.Contains((*seen)[0], `"type":"totp"`) {
		t.Fatalf("enroll request = %s", (*seen)[0])
	}
	if id, err := p.Challenge(ctx, "auth_factor_1"); err != nil || id != "auth_challenge_2" {
		t.Fatalf("Challenge = %q, %v", id, err)
	}
	if ok, err := p.Verify(ctx, "auth_challenge_2", "123456"); err != nil || !ok {
		t.Fatalf("Verify (valid) = %v, %v", ok, err)
	}
	if !strings.Contains((*seen)[2], `"code":"123456"`) {
		t.Fatalf("verify request = %s", (*seen)[2])
	}
	if ok, err := p.Verify(ctx, "auth_challenge_3", "000000"); err != nil || ok {
		t.Fatalf("Verify (invalid) = %v, %v; only valid: true counts", ok, err)
	}
	if ok, err := p.Verify(ctx, "auth_challenge_broken", "000000"); ok || apperr.KindOf(err) != apperr.KindUnavailable {
		t.Fatalf("Verify (provider error) = %v, %v", ok, err)
	}
	if err := p.DeleteFactor(ctx, "auth_factor_1"); err != nil {
		t.Fatalf("DeleteFactor = %v", err)
	}
	if err := p.DeleteFactor(ctx, "auth_factor_gone"); err != nil {
		t.Fatalf("DeleteFactor of a factor already gone = %v, want nil", err)
	}
	if _, err := p.Challenge(ctx, "auth_factor_unknown"); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("Challenge of an unknown factor = %v, want mfa.provider_rejected", err)
	}
}

// Each call to WorkOS is a client span with the provider and the result, never the URL,
// the code, the API key or a body.
func TestExchangeCode_ClientSpan(t *testing.T) {
	t.Parallel()
	spans := tracetest.NewInMemoryExporter()
	p, _, _ := fakeWorkOS(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)
	p.tracers = sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))

	const code = "secret-authorization-code"
	if _, err := p.ExchangeCode(t.Context(), code, "verifier"); err == nil {
		t.Fatal("expected an error")
	}
	got := spans.GetSpans()
	if len(got) != 1 {
		t.Fatalf("spans = %d", len(got))
	}
	s := got[0]
	attrs := map[string]string{}
	for _, kv := range s.Attributes {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	if s.Name != "workos.authenticate_with_code" || s.SpanKind != trace.SpanKindClient ||
		attrs["peer.service"] != "workos" || attrs["result"] != "auth.code_invalid" || len(attrs) != 2 {
		t.Fatalf("span = %s %v %v", s.Name, s.SpanKind, attrs)
	}
	if dump := fmt.Sprintf("%+v", s); strings.Contains(dump, code) || strings.Contains(dump, testKey) ||
		strings.Contains(dump, "user_management") || strings.Contains(dump, "verifier") {
		t.Fatalf("the span carries the call's details: %s", dump)
	}
}
