package identity_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"

	"github.com/DaniloSalaz/factory-template/api/gen/routes"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/adapters/fakeidp"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authz"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/clientip"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db/dbtest"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/ratelimit"
)

// Integration tests of the login, the session middleware and logout, over HTTPS (the
// __Host- cookies need Secure) with the fake identity provider and a real database.
// They run one after another: some count audit events.

const (
	frontOrigin = "https://front.test"
	apiKey      = "sk_test_integration-api-key"
	idleTTL     = 7 * 24 * time.Hour
	absoluteTTL = 30 * 24 * time.Hour
)

var testDB *dbtest.DB

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	ctx := context.Background()
	database, err := dbtest.New(ctx)
	if err != nil {
		if database != nil && database.Container != nil {
			_ = testcontainers.TerminateContainer(database.Container)
		}
		fmt.Fprintln(os.Stderr, "start test database:", err)
		os.Exit(1)
	}
	testDB = database
	code := m.Run()
	_ = testcontainers.TerminateContainer(database.Container)
	os.Exit(code)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type env struct {
	t      *testing.T
	srv    *httptest.Server
	idp    *fakeidp.Provider
	clock  *clock
	super  *pgx.Conn
	logs   *syncBuffer
	module *identity.Module
}

type options struct {
	mfaRequired bool
	rateLimit   int
	// catalog defaults to testCatalog.
	catalog *authz.Catalog
}

// testCatalog has the example roles but no MFA, so tests that do not care about MFA run
// with WORKOS_MFA_REQUIRED=false.
func testCatalog(t *testing.T, ownerMFA time.Duration) *authz.Catalog {
	t.Helper()
	c, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions(), RequiresMFA: ownerMFA > 0, MFAMaxAge: ownerMFA},
		authz.Role{Name: "admin", Permissions: []authctx.Permission{
			authz.PermMembersRead, authz.PermMembersInvite, authz.PermMembersManage, authz.PermAuditRead, authctx.PermScopeTenant,
		}},
		authz.Role{Name: "member", Permissions: []authctx.Permission{authz.PermMembersRead, authctx.PermScopeTenant}},
		authz.Role{Name: "client"},
	)
	if err != nil {
		t.Fatalf("test catalog: %v", err)
	}
	return c
}

func newEnv(t *testing.T, opts options) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx := t.Context()

	pool, err := pgxpool.New(ctx, testDB.RuntimeURL)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	super, err := pgx.Connect(ctx, testDB.SuperURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(func() { _ = super.Close(context.WithoutCancel(ctx)) })

	logs := &syncBuffer{}
	logger := logx.New(logs, config.Log{Level: slog.LevelDebug, Format: config.LogFormatJSON})
	clk := &clock{t: time.Now().UTC().Truncate(time.Second)}
	idp := fakeidp.New()
	if opts.rateLimit == 0 {
		opts.rateLimit = 1000
	}
	if opts.catalog == nil {
		opts.catalog = testCatalog(t, 0)
	}

	m, err := identity.New(identity.Deps{
		DB: db.New(pool), Logger: logger,
		Web:         config.Web{FrontendOrigin: frontOrigin, AllowedOrigins: []string{frontOrigin}},
		Auth:        config.Auth{SessionIdleTTL: idleTTL, SessionAbsoluteTTL: absoluteTTL},
		WorkOS:      config.WorkOS{ClientID: "client_test", APIKey: pii.Secret(apiKey), MFARequired: opts.mfaRequired},
		RateLimiter: ratelimit.NewMemory(opts.rateLimit, time.Minute, nil),
		Catalog:     opts.catalog,
		Provider:    idp, Now: clk.Now,
	})
	if err != nil {
		t.Fatalf("identity.New: %v", err)
	}

	mux := http.NewServeMux()
	m.Register(mux)
	noContent := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	mux.HandleFunc("GET /whoami", noContent)
	mux.HandleFunc("GET /undeclared", noContent)
	// A use case that writes, then checks a permission it lacks inside its transaction:
	// the write rolls back, and the denial must still be audited.
	database := db.New(pool)
	mux.HandleFunc("POST /deny-in-tx", func(w http.ResponseWriter, r *http.Request) {
		err := database.WithTenantTx(r.Context(), func(ctx context.Context, q db.Querier) error {
			if _, err := q.Exec(ctx, `UPDATE tenants SET name = name || ' (renamed)' WHERE id = $1`,
				r.Header.Get("X-Tenant-ID")); err != nil {
				return err
			}
			return m.Authorizer().Require(ctx, authz.PermMembersManage)
		})
		if err != nil {
			httpx.WriteProblem(w, r, err, logger)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /in-tenant", func(w http.ResponseWriter, r *http.Request) {
		p, ok := authctx.PrincipalFrom(r.Context())
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(principalView{
			UserID: p.UserID(), TenantID: p.TenantID(), Role: p.Role(), Scope: string(p.Scope()), Permissions: p.Permissions(),
		})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// The contract's table, plus a test route that needs only a session. /undeclared is
	// left out of it on purpose.
	security := routes.Security()
	security["GET /whoami"] = httpx.Security{Session: true}
	security["GET /in-tenant"] = httpx.Security{Session: true, Tenant: true}
	security["POST /deny-in-tx"] = httpx.Security{Session: true, Tenant: true}
	// The same chain as cmd/api.
	h := httpx.Chain(httpx.WithRouteFallback(mux, logger),
		httpx.RequestID(),
		httpx.AccessLog(logger),
		httpx.Recover(logger),
		httpx.SecurityHeaders(),
		httpx.ClientIP(clientip.NewResolver("")),
		httpx.CORS([]string{frontOrigin}),
		httpx.CSRF([]string{frontOrigin}, logger),
		httpx.RouteSecurity(mux, security),
		m.SessionMiddleware(),
		m.TenantMiddleware(),
	)
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, idp: idp, clock: clk, super: super, logs: logs, module: m}
}

// browser is one cookie jar: one user agent.
type browser struct {
	e      *env
	client *http.Client
}

func (e *env) browser() *browser {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	// srv.Client returns the same client every time: copy it so each browser has its own jar.
	c := *e.srv.Client()
	c.Jar = jar
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &browser{e: e, client: &c}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) problemCode(t *testing.T) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatalf("decode problem (status %d): %v: %s", r.status, err, r.body)
	}
	return p.Code
}

func (b *browser) do(method, path, origin string) response {
	b.e.t.Helper()
	return b.doHeaders(method, path, origin, nil)
}

// inTenant calls GET /in-tenant with X-Tenant-ID (none if tenantID is "-").
func (b *browser) inTenant(tenantID string) response {
	b.e.t.Helper()
	h := http.Header{}
	if tenantID != "-" {
		h.Set("X-Tenant-ID", tenantID)
	}
	return b.doHeaders(http.MethodGet, "/in-tenant", "", h)
}

func (b *browser) doHeaders(method, path, origin string, header http.Header) response {
	b.e.t.Helper()
	req, err := http.NewRequestWithContext(b.e.t.Context(), method, b.e.srv.URL+path, nil)
	if err != nil {
		b.e.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		b.e.t.Fatal(err)
	}
	return response{status: res.StatusCode, header: res.Header, body: body}
}

// startLogin calls /auth/login and returns the state sent to the provider.
func (b *browser) startLogin(returnTo string) string {
	b.e.t.Helper()
	res := b.do(http.MethodGet, "/auth/login?return_to="+url.QueryEscape(returnTo), "")
	if res.status != http.StatusFound {
		b.e.t.Fatalf("login: status %d: %s", res.status, res.body)
	}
	loc, err := url.Parse(res.header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), fakeidp.AuthorizeBase) {
		b.e.t.Fatalf("login redirects to %q", res.header.Get("Location"))
	}
	if loc.Query().Get("max_age") != "0" || loc.Query().Get("code_challenge") == "" {
		b.e.t.Fatalf("authorization url must force re-authentication with PKCE: %s", loc)
	}
	return loc.Query().Get("state")
}

func (b *browser) callback(state, code string) response {
	b.e.t.Helper()
	return b.do(http.MethodGet, "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), "")
}

// login runs a whole login as authn and fails the test unless it succeeds.
func (b *browser) login(authn app.Authentication) {
	b.e.t.Helper()
	state := b.startLogin("/")
	code := "code-" + state
	b.e.idp.Authenticate(state, code, authn)
	res := b.callback(state, code)
	if res.status != http.StatusFound {
		b.e.t.Fatalf("callback: status %d: %s", res.status, res.body)
	}
}

// sessionToken returns the browser's __Host-session cookie, or "".
func (b *browser) sessionToken() string {
	u, _ := url.Parse(b.e.srv.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == "__Host-session" {
			return c.Value
		}
	}
	return ""
}

func (b *browser) intentCookies() int {
	u, _ := url.Parse(b.e.srv.URL)
	n := 0
	for _, c := range b.client.Jar.Cookies(u) {
		if strings.HasPrefix(c.Name, "__Host-oauth_") {
			n++
		}
	}
	return n
}

func (e *env) scalar(sql string, args ...any) string {
	e.t.Helper()
	var v *string
	if err := e.super.QueryRow(e.t.Context(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("query %q: %v", sql, err)
	}
	if v == nil {
		return "<null>"
	}
	return *v
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.super.QueryRow(e.t.Context(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.super.Exec(e.t.Context(), sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

var seq struct {
	mu sync.Mutex
	n  int
}

// person returns a fresh provider identity with a unique email.
func (e *env) person() app.Authentication {
	seq.mu.Lock()
	seq.n++
	n := seq.n
	seq.mu.Unlock()
	return app.Authentication{
		Subject:       fmt.Sprintf("user_%s_%d", strings.ReplaceAll(e.t.Name(), "/", "_"), n),
		Email:         pii.Email(fmt.Sprintf("u%d.%d@example.test", n, time.Now().UnixNano())),
		EmailVerified: true, DisplayName: "Test User", SessionID: fmt.Sprintf("session_%d", n), Method: "Password",
		MFA: domain.ProviderMFA{AuthTime: e.clock.Now().Add(-time.Second), CoveredByPolicy: true},
	}
}

// accessRecord returns the last access record ("http request") logged for path.
func (e *env) accessRecord(path string) map[string]any {
	e.t.Helper()
	var last map[string]any
	for line := range strings.Lines(e.logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			e.t.Fatalf("invalid log line %q: %v", line, err)
		}
		if m["msg"] == "http request" && m["path"] == path {
			last = m
		}
	}
	if last == nil {
		e.t.Fatalf("no access record for %s:\n%s", path, e.logs.String())
	}
	return last
}

func hashHex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestLogin_CreatesSessionStoringOnlyTheHash(t *testing.T) {
	e := newEnv(t, options{})
	b := e.browser()
	who := e.person()

	state := b.startLogin("/cases/1?tab=docs")
	e.idp.Authenticate(state, "code-1", who)
	res := b.callback(state, "code-1")
	if res.status != http.StatusFound || res.header.Get("Location") != frontOrigin+"/cases/1?tab=docs" {
		t.Fatalf("callback: %d to %q: %s", res.status, res.header.Get("Location"), res.body)
	}

	var setCookie string
	for _, c := range res.header.Values("Set-Cookie") {
		if strings.HasPrefix(c, "__Host-session=") {
			setCookie = c
		}
	}
	for _, attr := range []string{"Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(setCookie, attr) {
			t.Errorf("session cookie lacks %s: %s", attr, setCookie)
		}
	}
	if strings.Contains(setCookie, "Domain=") {
		t.Errorf("session cookie must have no Domain: %s", setCookie)
	}
	token := b.sessionToken()
	if !domain.WellFormed(token) {
		t.Fatalf("session token %q", token)
	}
	if n := b.intentCookies(); n != 0 {
		t.Fatalf("the callback must delete its intent; %d left", n)
	}

	userID := e.scalar(`SELECT user_id::text FROM user_identities WHERE provider = 'fake' AND subject = $1`, who.Subject)
	if got := e.scalar(`SELECT encode(token_hash, 'hex') FROM sessions WHERE user_id = $1`, userID); got != hashHex(token) {
		t.Fatalf("token_hash = %s, want sha256 of the cookie", got)
	}
	if n := e.count(`SELECT count(*) FROM sessions s WHERE row_to_json(s)::text LIKE '%' || $1 || '%'`, token); n != 0 {
		t.Fatal("the session token is stored in clear")
	}
	if got := e.scalar(`SELECT (auth_time = $2)::text FROM sessions WHERE user_id = $1`, userID, who.MFA.AuthTime); got != "true" {
		t.Fatalf("auth_time must be the provider's time")
	}
	if got := e.scalar(`SELECT ip_prefix::text FROM sessions WHERE user_id = $1`, userID); got != "127.0.0.0/24" {
		t.Fatalf("ip_prefix = %s, want the truncated client address", got)
	}
	if got := e.scalar(`SELECT provider_session_id FROM sessions WHERE user_id = $1`, userID); got != who.SessionID {
		t.Fatalf("provider_session_id = %s", got)
	}
	if got := e.scalar(`SELECT action FROM audit_log WHERE actor_user_id = $1 ORDER BY created_at DESC LIMIT 1`, userID); got != "auth.login" {
		t.Fatalf("last audit event of the user = %s, want auth.login", got)
	}

	if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusNoContent {
		t.Fatalf("authenticated request: %d %s", res.status, res.body)
	}
	if got := e.accessRecord("/whoami")["user_id"]; got != userID {
		t.Fatalf("access record user_id = %v, want %s (issue #13)", got, userID)
	}
	if res := e.browser().do(http.MethodGet, "/healthz", ""); res.status != http.StatusOK {
		t.Fatalf("healthz: %d", res.status)
	}
	if rec := e.accessRecord("/healthz"); rec["user_id"] != nil {
		t.Fatalf("a request without a session has no user_id: %v", rec)
	}
	if strings.Contains(e.logs.String(), token) {
		t.Fatal("the session token appears in the logs")
	}
}

func TestLogin_ReturnToMustBeTheFrontEnd(t *testing.T) {
	e := newEnv(t, options{})
	b := e.browser()
	for _, raw := range []string{"//evil.com", `/\evil.com`, "/\t/evil.com", `/\t/evil.com`, "https://evil.com"} {
		res := b.do(http.MethodGet, "/auth/login?return_to="+url.QueryEscape(raw), "")
		if res.status != http.StatusBadRequest || res.problemCode(t) != "auth.return_to_invalid" {
			t.Errorf("return_to %q: %d %s", raw, res.status, res.body)
		}
	}
	if n := b.intentCookies(); n != 0 {
		t.Fatalf("a rejected login must not create an intent; %d exist", n)
	}
}

func TestLogin_ParallelIntentsAndOldestDiscarded(t *testing.T) {
	e := newEnv(t, options{})
	b := e.browser()

	// Two tabs: both callbacks work.
	s1, s2 := b.startLogin("/one"), b.startLogin("/two")
	e.idp.Authenticate(s1, "c1", e.person())
	e.idp.Authenticate(s2, "c2", e.person())
	if res := b.callback(s2, "c2"); res.status != http.StatusFound || res.header.Get("Location") != frontOrigin+"/two" {
		t.Fatalf("second tab: %d %s", res.status, res.body)
	}
	if res := b.callback(s1, "c1"); res.status != http.StatusFound || res.header.Get("Location") != frontOrigin+"/one" {
		t.Fatalf("first tab: %d %s", res.status, res.body)
	}

	// Four intents: the oldest is dropped so that at most three stay alive.
	var states []string
	for i := range 4 {
		states = append(states, b.startLogin("/"))
		e.clock.Advance(time.Second)
		if n := b.intentCookies(); n > 3 {
			t.Fatalf("after %d logins, %d intents are alive", i+1, n)
		}
	}
	e.idp.Authenticate(states[0], "c-old", e.person())
	if res := b.callback(states[0], "c-old"); res.status != http.StatusBadRequest || res.problemCode(t) != "auth.intent_invalid" {
		t.Fatalf("oldest intent: %d %s", res.status, res.body)
	}
	e.idp.Authenticate(states[3], "c-new", e.person())
	if res := b.callback(states[3], "c-new"); res.status != http.StatusFound {
		t.Fatalf("newest intent: %d %s", res.status, res.body)
	}
}

func TestLogin_IntentIsBoundToTheBrowserAndExpires(t *testing.T) {
	e := newEnv(t, options{})
	victim, attacker := e.browser(), e.browser()

	// A state started in another browser (login CSRF) names no intent here.
	state := attacker.startLogin("/")
	e.idp.Authenticate(state, "c", e.person())
	if res := victim.callback(state, "c"); res.status != http.StatusBadRequest || res.problemCode(t) != "auth.intent_invalid" {
		t.Fatalf("foreign state: %d %s", res.status, res.body)
	}
	if victim.sessionToken() != "" {
		t.Fatal("a foreign callback must not create a session")
	}

	state = victim.startLogin("/")
	e.idp.Authenticate(state, "c2", e.person())
	e.clock.Advance(domain.IntentTTL + time.Second)
	if res := victim.callback(state, "c2"); res.status != http.StatusBadRequest || res.problemCode(t) != "auth.intent_invalid" {
		t.Fatalf("expired intent: %d %s", res.status, res.body)
	}
}

func TestLogin_EmailInUseWritesOnlyTheAudit(t *testing.T) {
	e := newEnv(t, options{})
	owner := e.person()
	e.browser().login(owner)

	intruder := e.person()
	intruder.Email = owner.Email
	before := map[string]int{}
	tables := []string{"users", "user_identities", "sessions"}
	for _, tbl := range tables {
		before[tbl] = e.count(`SELECT count(*) FROM ` + tbl)
	}
	audits := e.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.email_in_use'`)

	b := e.browser()
	state := b.startLogin("/")
	e.idp.Authenticate(state, "c", intruder)
	res := b.callback(state, "c")
	if res.status != http.StatusConflict || res.problemCode(t) != "auth.email_in_use" {
		t.Fatalf("callback: %d %s", res.status, res.body)
	}
	for _, tbl := range tables {
		if n := e.count(`SELECT count(*) FROM ` + tbl); n != before[tbl] {
			t.Errorf("%s: %d rows, want %d (nothing written)", tbl, n, before[tbl])
		}
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.email_in_use'`); n != audits+1 {
		t.Fatalf("auth.email_in_use events = %d, want %d", n, audits+1)
	}
}

func TestLogin_DisabledUserLeavesOnlyLoginFailed(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	e.browser().login(who)
	userID := e.scalar(`SELECT user_id::text FROM user_identities WHERE subject = $1`, who.Subject)
	e.exec(`UPDATE users SET disabled_at = now() WHERE id = $1`, userID)
	sessions := e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, userID)
	lastLogin := e.scalar(`SELECT last_login_at::text FROM users WHERE id = $1`, userID)
	events := e.count(`SELECT count(*) FROM audit_log`)

	b := e.browser()
	state := b.startLogin("/")
	e.idp.Authenticate(state, "c", who)
	if res := b.callback(state, "c"); res.status != http.StatusForbidden || res.problemCode(t) != "auth.user_disabled" {
		t.Fatalf("callback: %d %s", res.status, res.body)
	}
	if n := e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, userID); n != sessions {
		t.Fatal("a disabled user got a session")
	}
	if got := e.scalar(`SELECT last_login_at::text FROM users WHERE id = $1`, userID); got != lastLogin {
		t.Fatal("last_login_at changed")
	}
	if n := e.count(`SELECT count(*) FROM audit_log`); n != events+1 {
		t.Fatalf("audit events: %d new, want 1", n-events)
	}
	if got := e.scalar(`SELECT action || ' ' || (metadata->>'reason') FROM audit_log ORDER BY created_at DESC, id DESC LIMIT 1`); got != "auth.login_failed user_disabled" {
		t.Fatalf("last event = %s", got)
	}
}

// The first login of a new identity from several tabs or devices at once: every
// callback must end in a session of the same, single user, never in a 500.
func TestLogin_ConcurrentFirstLogin(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	const n = 8

	type attempt struct {
		b     *browser
		state string
	}
	attempts := make([]attempt, n)
	for i := range attempts {
		b := e.browser()
		state := b.startLogin("/")
		e.idp.Authenticate(state, fmt.Sprintf("c%d", i), who)
		attempts[i] = attempt{b: b, state: state}
	}

	statuses := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, a := range attempts {
		wg.Go(func() {
			<-start
			u := e.srv.URL + "/auth/callback?state=" + url.QueryEscape(a.state) + "&code=" + fmt.Sprintf("c%d", i)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
			if err != nil {
				return
			}
			res, err := a.b.client.Do(req)
			if err != nil {
				return
			}
			_ = res.Body.Close()
			statuses[i] = res.StatusCode
		})
	}
	close(start)
	wg.Wait()

	for i, s := range statuses {
		if s != http.StatusFound {
			t.Errorf("callback %d: status %d, want 302", i, s)
		}
	}
	if got := e.count(`SELECT count(*) FROM users WHERE email = $1::text::extensions.citext`, who.Email.Reveal()); got != 1 {
		t.Fatalf("%d users for one identity", got)
	}
	if got := e.count(`SELECT count(DISTINCT user_id) FROM sessions WHERE user_id IN
		(SELECT user_id FROM user_identities WHERE subject = $1)`, who.Subject); got != 1 {
		t.Fatalf("sessions belong to %d users", got)
	}
	if got := e.count(`SELECT count(*) FROM sessions WHERE user_id IN
		(SELECT user_id FROM user_identities WHERE subject = $1)`, who.Subject); got != n {
		t.Fatalf("%d sessions, want %d", got, n)
	}
}

func TestLogin_ImpersonationIsRefused(t *testing.T) {
	e := newEnv(t, options{mfaRequired: true})
	victim := e.person()
	e.browser().login(victim)
	userID := e.scalar(`SELECT user_id::text FROM user_identities WHERE subject = $1`, victim.Subject)
	sessions := e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, userID)

	impersonated := victim
	impersonated.Impersonated = true
	b := e.browser()
	state := b.startLogin("/")
	e.idp.Authenticate(state, "c", impersonated)
	if res := b.callback(state, "c"); res.status != http.StatusForbidden || res.problemCode(t) != "auth.impersonation_refused" {
		t.Fatalf("callback: %d %s", res.status, res.body)
	}
	if b.sessionToken() != "" || e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, userID) != sessions {
		t.Fatal("an impersonated login created a session")
	}
	if got := e.scalar(`SELECT action || ' ' || (metadata->>'reason') FROM audit_log ORDER BY created_at DESC, id DESC LIMIT 1`); got != "auth.login_failed impersonation" {
		t.Fatalf("last event = %s", got)
	}
}

func TestLogin_StaleAuthenticationIsRejected(t *testing.T) {
	e := newEnv(t, options{mfaRequired: true})

	t.Run("older than the attempt, beyond the clock margin", func(t *testing.T) {
		who := e.person()
		b := e.browser()
		state := b.startLogin("/")
		who.MFA.AuthTime = e.clock.Now().Add(-domain.AuthClockSkew - time.Second)
		e.idp.Authenticate(state, "c", who)
		if res := b.callback(state, "c"); res.status != http.StatusUnauthorized || res.problemCode(t) != "auth.stale_authentication" {
			t.Fatalf("callback: %d %s", res.status, res.body)
		}
		if n := e.count(`SELECT count(*) FROM user_identities WHERE subject = $1`, who.Subject); n != 0 {
			t.Fatal("a stale authentication created the user")
		}
		if got := e.scalar(`SELECT action || ' ' || (metadata->>'reason') FROM audit_log ORDER BY created_at DESC, id DESC LIMIT 1`); got != "auth.login_failed stale_auth" {
			t.Fatalf("last event = %s", got)
		}
	})

	t.Run("within the clock margin", func(t *testing.T) {
		who := e.person()
		b := e.browser()
		state := b.startLogin("/")
		who.MFA.AuthTime = e.clock.Now().Add(-domain.AuthClockSkew + time.Second)
		e.idp.Authenticate(state, "c2", who)
		if res := b.callback(state, "c2"); res.status != http.StatusFound {
			t.Fatalf("callback: %d %s", res.status, res.body)
		}
	})
}

func TestLogin_UnverifiedEmailIsRejected(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	who.EmailVerified = false
	b := e.browser()
	state := b.startLogin("/")
	e.idp.Authenticate(state, "c", who)
	if res := b.callback(state, "c"); res.status != http.StatusForbidden || res.problemCode(t) != "auth.email_unverified" {
		t.Fatalf("callback: %d %s", res.status, res.body)
	}
	if n := e.count(`SELECT count(*) FROM user_identities WHERE subject = $1`, who.Subject); n != 0 {
		t.Fatal("an unverified email created an identity")
	}
}

func TestLogin_RevokesThePreviousBrowserSession(t *testing.T) {
	e := newEnv(t, options{})
	b := e.browser()
	b.login(e.person())
	first := b.sessionToken()
	b.login(e.person())
	second := b.sessionToken()

	if first == second {
		t.Fatal("the second login must issue a new token")
	}
	if got := e.scalar(`SELECT (revoked_at IS NOT NULL)::text FROM sessions WHERE encode(token_hash, 'hex') = $1`, hashHex(first)); got != "true" {
		t.Fatal("the previous session of the browser is still valid")
	}
	other := e.browser()
	other.client.Jar.SetCookies(mustURL(e.srv.URL), []*http.Cookie{{Name: "__Host-session", Value: first, Path: "/", Secure: true}})
	if res := other.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized {
		t.Fatalf("the revoked token still works: %d", res.status)
	}
}

func TestLogin_EmailChangeTakesTheProvidersVerification(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	e.browser().login(who)
	userID := e.scalar(`SELECT user_id::text FROM user_identities WHERE subject = $1`, who.Subject)
	// The stored flag belongs to the old email; the new one must bring its own (issue #4).
	e.exec(`UPDATE users SET email_verified = false WHERE id = $1`, userID)

	who.Email = pii.Email("changed." + who.Email.Reveal())
	e.browser().login(who)
	if got := e.scalar(`SELECT email::text || ' ' || email_verified::text FROM users WHERE id = $1`, userID); got != who.Email.Reveal()+" true" {
		t.Fatalf("user = %s, want the new email, verified", got)
	}
}

func TestLogin_EmailChangeToATakenEmailUnverifiesTheOldOne(t *testing.T) {
	e := newEnv(t, options{})
	who, other := e.person(), e.person()
	e.browser().login(who)
	e.browser().login(other)
	userID := e.scalar(`SELECT user_id::text FROM user_identities WHERE subject = $1`, who.Subject)

	moved := who
	moved.Email = other.Email
	e.browser().login(moved)
	if got := e.scalar(`SELECT email::text || ' ' || email_verified::text FROM users WHERE id = $1`, userID); got != who.Email.Reveal()+" false" {
		t.Fatalf("user = %s, want the old email, unverified", got)
	}
	if got := e.scalar(`SELECT email_verified::text FROM users WHERE email = $1::text::extensions.citext`, other.Email.Reveal()); got != "true" {
		t.Fatalf("the other user's verification changed: %s", got)
	}
}

func TestSession_InvalidSessionsAre401(t *testing.T) {
	e := newEnv(t, options{})

	t.Run("missing or unknown", func(t *testing.T) {
		b := e.browser()
		if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized || res.problemCode(t) != "auth.session_invalid" {
			t.Fatalf("no cookie: %d %s", res.status, res.body)
		}
		token, _ := domain.NewRandom()
		b.client.Jar.SetCookies(mustURL(e.srv.URL), []*http.Cookie{{Name: "__Host-session", Value: token, Path: "/", Secure: true}})
		if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized {
			t.Fatalf("unknown token: %d", res.status)
		}
	})

	t.Run("idle", func(t *testing.T) {
		b := e.browser()
		b.login(e.person())
		e.clock.Advance(idleTTL)
		if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized {
			t.Fatalf("idle session: %d", res.status)
		}
	})

	t.Run("activity extends idle but not absolute", func(t *testing.T) {
		b := e.browser()
		b.login(e.person())
		for elapsed := time.Duration(0); elapsed+6*24*time.Hour < absoluteTTL; elapsed += 6 * 24 * time.Hour {
			e.clock.Advance(6 * 24 * time.Hour)
			if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusNoContent {
				t.Fatalf("active session after %s: %d", elapsed, res.status)
			}
		}
		e.clock.Advance(6 * 24 * time.Hour)
		if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized {
			t.Fatalf("session past its absolute expiry: %d", res.status)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		b := e.browser()
		b.login(e.person())
		e.exec(`UPDATE sessions SET revoked_at = now() WHERE encode(token_hash, 'hex') = $1`, hashHex(b.sessionToken()))
		if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized {
			t.Fatalf("revoked session: %d", res.status)
		}
	})

	t.Run("disabled user with a live session", func(t *testing.T) {
		b := e.browser()
		who := e.person()
		b.login(who)
		e.exec(`UPDATE users SET disabled_at = now() WHERE id = (SELECT user_id FROM user_identities WHERE subject = $1)`, who.Subject)
		if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized {
			t.Fatalf("disabled user: %d", res.status)
		}
	})
}

// The contract decides which routes need a session; a route it does not declare needs
// one all the same.
func TestSession_FollowsTheContract(t *testing.T) {
	e := newEnv(t, options{})
	anon := e.browser()
	if res := anon.do(http.MethodGet, "/healthz", ""); res.status != http.StatusOK {
		t.Fatalf("security: [] is public: %d %s", res.status, res.body)
	}
	if res := anon.do(http.MethodGet, "/auth/login?return_to=/", ""); res.status != http.StatusFound {
		t.Fatalf("the login is public: %d %s", res.status, res.body)
	}
	for _, path := range []string{"/undeclared", "/whoami", "/does-not-exist"} {
		if res := anon.do(http.MethodGet, path, ""); res.status != http.StatusUnauthorized {
			t.Fatalf("%s without a session: %d %s, want 401", path, res.status, res.body)
		}
	}
}

func TestSession_TouchesAtMostEveryFiveMinutes(t *testing.T) {
	e := newEnv(t, options{})
	b := e.browser()
	b.login(e.person())
	hash := hashHex(b.sessionToken())
	seen := func() string {
		return e.scalar(`SELECT last_seen_at::text FROM sessions WHERE encode(token_hash, 'hex') = $1`, hash)
	}
	start := seen()
	e.clock.Advance(4 * time.Minute)
	b.do(http.MethodGet, "/whoami", "")
	if seen() != start {
		t.Fatal("touched before five minutes")
	}
	e.clock.Advance(time.Minute)
	b.do(http.MethodGet, "/whoami", "")
	if seen() == start {
		t.Fatal("not touched after five minutes")
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t, options{})

	t.Run("revokes, clears the cookie and returns the provider's logout URL", func(t *testing.T) {
		b := e.browser()
		who := e.person()
		b.login(who)
		hash := hashHex(b.sessionToken())
		res := b.do(http.MethodPost, "/auth/logout", frontOrigin)
		if res.status != http.StatusOK {
			t.Fatalf("logout: %d %s", res.status, res.body)
		}
		var body struct {
			LogoutURL *string `json:"logout_url"`
		}
		if err := json.Unmarshal(res.body, &body); err != nil || body.LogoutURL == nil || !strings.Contains(*body.LogoutURL, who.SessionID) {
			t.Fatalf("logout body: %s", res.body)
		}
		if b.sessionToken() != "" {
			t.Fatal("the session cookie is still set")
		}
		if got := e.scalar(`SELECT (revoked_at IS NOT NULL)::text FROM sessions WHERE encode(token_hash, 'hex') = $1`, hash); got != "true" {
			t.Fatal("the session is not revoked")
		}
	})

	t.Run("logout-all revokes every session of the user", func(t *testing.T) {
		who := e.person()
		laptop, phone := e.browser(), e.browser()
		laptop.login(who)
		phone.login(who)
		if res := laptop.do(http.MethodPost, "/auth/logout-all", frontOrigin); res.status != http.StatusOK {
			t.Fatalf("logout-all: %d %s", res.status, res.body)
		}
		if res := phone.do(http.MethodGet, "/whoami", ""); res.status != http.StatusUnauthorized {
			t.Fatalf("the other session survived: %d", res.status)
		}
	})

	t.Run("a foreign or missing Origin is 403", func(t *testing.T) {
		b := e.browser()
		b.login(e.person())
		for _, origin := range []string{"", "https://evil.test"} {
			if res := b.do(http.MethodPost, "/auth/logout", origin); res.status != http.StatusForbidden || res.problemCode(t) != "csrf.origin_rejected" {
				t.Fatalf("origin %q: %d %s", origin, res.status, res.body)
			}
		}
		if res := b.do(http.MethodGet, "/whoami", ""); res.status != http.StatusNoContent {
			t.Fatalf("the session must survive a rejected logout: %d", res.status)
		}
	})
}

func TestLogin_RateLimited(t *testing.T) {
	e := newEnv(t, options{rateLimit: 3})
	b := e.browser()
	for range 3 {
		b.startLogin("/")
	}
	if res := b.do(http.MethodGet, "/auth/login", ""); res.status != http.StatusTooManyRequests || res.problemCode(t) != "rate_limit.exceeded" {
		t.Fatalf("fourth request: %d %s", res.status, res.body)
	}
	if res := b.callback("x", "y"); res.status != http.StatusTooManyRequests {
		t.Fatalf("the callback shares the quota: %d", res.status)
	}
}

func TestLogin_MFA(t *testing.T) {
	e := newEnv(t, options{mfaRequired: true})
	mfaOf := func(who app.Authentication) string {
		return e.scalar(`SELECT (mfa_auth_time = auth_time)::text FROM sessions
			WHERE user_id = (SELECT user_id FROM user_identities WHERE subject = $1)
			ORDER BY created_at DESC LIMIT 1`, who.Subject)
	}

	who := e.person()
	e.browser().login(who)
	if got := mfaOf(who); got != "true" {
		t.Fatalf("with MFA required at the provider, mfa_auth_time = auth_time; got %s", got)
	}

	offList := e.person()
	offList.MFA.CoveredByPolicy = false
	e.browser().login(offList)
	if got := mfaOf(offList); got != "<null>" {
		t.Fatalf("a method off the allowlist is never MFA; got %s", got)
	}

	noMFA := e.person()
	noMFA.MFA.HasAMR, noMFA.MFA.AMR = true, []string{"pwd"}
	b := e.browser()
	state := b.startLogin("/")
	e.idp.Authenticate(state, "c", noMFA)
	if res := b.callback(state, "c"); res.status != http.StatusForbidden || res.problemCode(t) != "auth.mfa_required" {
		t.Fatalf("reported methods without mfa: %d %s", res.status, res.body)
	}
}

func TestNew_RefusesRolesRequiringMFAWithoutProviderMFA(t *testing.T) {
	t.Parallel()

	catalog, err := authz.NewCatalog(
		authz.Role{Name: authz.RoleOwner, Permissions: authz.PlatformPermissions(), RequiresMFA: true, MFAMaxAge: time.Hour},
		authz.Role{Name: "lawyer", RequiresMFA: true, MFAMaxAge: time.Hour},
		authz.Role{Name: "client"},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = identity.New(identity.Deps{
		WorkOS:  config.WorkOS{APIKey: pii.Secret(apiKey), MFARequired: false},
		Catalog: catalog,
	})
	if err == nil || !strings.Contains(err.Error(), "lawyer, owner require MFA but WORKOS_MFA_REQUIRED is false") ||
		!strings.Contains(err.Error(), "docs/runbooks/auth.md") {
		t.Fatalf("New = %v", err)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

type principalView struct {
	UserID      string               `json:"user_id"`
	TenantID    string               `json:"tenant_id"`
	Role        string               `json:"role"`
	Scope       string               `json:"scope"`
	Permissions []authctx.Permission `json:"permissions"`
}

func (r response) principal(t *testing.T) principalView {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("in-tenant: %d %s", r.status, r.body)
	}
	var p principalView
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) userID(who app.Authentication) string {
	e.t.Helper()
	return e.scalar(`SELECT user_id::text FROM user_identities WHERE subject = $1`, who.Subject)
}

// logRecords returns the log records whose msg is msg.
func (e *env) logRecords(msg string) []map[string]any {
	e.t.Helper()
	var out []map[string]any
	for line := range strings.Lines(e.logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			e.t.Fatalf("invalid log line %q: %v", line, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// tenant creates an active tenant as the superuser (POST /tenants arrives in step 5)
// and returns its id.
func (e *env) tenant() string {
	e.t.Helper()
	seq.mu.Lock()
	seq.n++
	n := seq.n
	seq.mu.Unlock()
	slug := fmt.Sprintf("t-%d-%d", n, time.Now().UnixNano())
	return e.scalar(`INSERT INTO tenants (name, slug) VALUES ($1, $2) RETURNING id::text`, "Tenant "+slug, slug)
}

// member makes the user of who a member of tenantID with role.
func (e *env) member(who app.Authentication, tenantID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO memberships (tenant_id, user_id, role)
		SELECT $1, user_id, $3 FROM user_identities WHERE subject = $2`, tenantID, who.Subject, role)
}

func TestCheckRolesInUse_LogsUnknownRoles(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	e.browser().login(who)
	e.member(who, e.tenant(), "ghost-role")
	e.member(who, e.tenant(), "member")

	e.module.CheckRolesInUse(t.Context())
	var found bool
	for line := range strings.Lines(e.logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if m["role"] == "member" {
			t.Fatalf("a role of the catalog was reported: %v", m)
		}
		if m["role"] == "ghost-role" && m["level"] == "ERROR" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no error for the unknown role:\n%s", e.logs.String())
	}
}

func TestTenant_SameForbiddenForEveryReason(t *testing.T) {
	e := newEnv(t, options{})
	who, other := e.person(), e.person()
	b := e.browser()
	b.login(who)
	e.browser().login(other)
	mine, foreign, suspended := e.tenant(), e.tenant(), e.tenant()
	e.member(who, mine, "member")
	e.member(other, foreign, "member")
	e.member(who, suspended, "member")
	e.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, suspended)
	audits := e.count(`SELECT count(*) FROM audit_log`)

	if p := b.inTenant(mine).principal(t); p.TenantID != mine {
		t.Fatalf("own tenant: %+v", p)
	}
	nonexistent := "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f9999"
	cases := []struct{ name, tenant string }{
		{"missing", "-"}, {"malformed", "not-a-uuid"}, {"nonexistent", nonexistent},
		{"foreign", foreign}, {"suspended", suspended},
	}
	var first httpx.Problem
	for i, tc := range cases {
		res := b.inTenant(tc.tenant)
		var p httpx.Problem
		if err := json.Unmarshal(res.body, &p); err != nil || res.status != http.StatusForbidden || p.Code != "auth.tenant_forbidden" {
			t.Fatalf("%s: %d %s", tc.name, res.status, res.body)
		}
		p.RequestID = ""
		if i == 0 {
			first = p
		} else if fmt.Sprint(p) != fmt.Sprint(first) {
			t.Fatalf("%s: problem %+v differs from %+v", tc.name, p, first)
		}
	}

	if n := e.count(`SELECT count(*) FROM audit_log`); n != audits {
		t.Fatalf("a refused tenant must not be audited: %d new events", n-audits)
	}
	warns := e.logRecords("tenant refused")
	if len(warns) != len(cases) {
		t.Fatalf("want one warning per refusal, got %d", len(warns))
	}
	uid := e.userID(who)
	for i, w := range warns {
		if w["level"] != "WARN" || w["user_id"] != uid {
			t.Fatalf("warning %d: %v", i, w)
		}
		got, has := w["requested_tenant_id"]
		switch cases[i].name {
		case "missing", "malformed":
			if has {
				t.Fatalf("%s: a non-UUID must not reach the logs: %v", cases[i].name, w)
			}
		default:
			if got != cases[i].tenant {
				t.Fatalf("%s: requested_tenant_id = %v", cases[i].name, got)
			}
		}
	}
}

func TestTenant_Principal(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	b := e.browser()
	b.login(who)
	uid := e.userID(who)
	member, client, ghost := e.tenant(), e.tenant(), e.tenant()
	e.member(who, member, "member")
	e.member(who, client, "client")
	e.member(who, ghost, "ghost-role")

	p := b.inTenant(member).principal(t)
	if p.UserID != uid || p.Role != "member" || p.Scope != string(authctx.ScopeTenant) ||
		!slices.Contains(p.Permissions, authz.PermMembersRead) {
		t.Fatalf("member: %+v", p)
	}
	rec := e.accessRecord("/in-tenant")
	if rec["user_id"] != uid || rec["tenant_id"] != member || rec["pattern"] != "GET /in-tenant" {
		t.Fatalf("access record = %v, want user_id, tenant_id and pattern (issue #13)", rec)
	}

	if p := b.inTenant(client).principal(t); p.Scope != string(authctx.ScopeOwn) || len(p.Permissions) != 0 {
		t.Fatalf("client (no scope:tenant): %+v", p)
	}
	if p := b.inTenant(ghost).principal(t); p.Scope != string(authctx.ScopeOwn) || len(p.Permissions) != 0 {
		t.Fatalf("an unknown role has no permissions: %+v", p)
	}
	errs := e.logRecords("the member's role is not in the catalog: acting with no permissions")
	if len(errs) != 1 || errs[0]["level"] != "ERROR" || errs[0]["role"] != "ghost-role" {
		t.Fatalf("unknown role log = %v", errs)
	}

	// A route without a tenant ignores the header.
	if res := b.doHeaders(http.MethodGet, "/whoami", "", http.Header{"X-Tenant-Id": {"garbage"}}); res.status != http.StatusNoContent {
		t.Fatalf("/whoami with a junk X-Tenant-ID: %d", res.status)
	}
}

func TestTenant_RoleMFA(t *testing.T) {
	e := newEnv(t, options{mfaRequired: true, catalog: testCatalog(t, 12*time.Hour)})
	tenant := e.tenant()

	noMFA := e.person()
	noMFA.MFA.CoveredByPolicy = false // a method off the allowlist: mfa_auth_time is NULL
	nb := e.browser()
	nb.login(noMFA)
	e.member(noMFA, tenant, authz.RoleOwner)
	if res := nb.inTenant(tenant); res.status != http.StatusForbidden || res.problemCode(t) != "auth.mfa_required" {
		t.Fatalf("owner without MFA: %d %s", res.status, res.body)
	}
	if res := nb.do(http.MethodGet, "/whoami", ""); res.status != http.StatusNoContent {
		t.Fatalf("routes without a tenant do not check the role's MFA: %d", res.status)
	}

	withMFA := e.person()
	b := e.browser()
	b.login(withMFA)
	e.member(withMFA, tenant, authz.RoleOwner)
	if p := b.inTenant(tenant).principal(t); p.Role != authz.RoleOwner {
		t.Fatalf("owner with recent MFA: %+v", p)
	}
	e.clock.Advance(12*time.Hour + time.Minute)
	if res := b.inTenant(tenant); res.status != http.StatusForbidden || res.problemCode(t) != "auth.mfa_required" {
		t.Fatalf("owner with MFA older than 12 h: %d %s", res.status, res.body)
	}
}

// Without httpx.RouteSecurity in front (a wiring bug), both middlewares require
// everything instead of letting the request through.
func TestMiddlewares_FailClosedWithoutRouteSecurity(t *testing.T) {
	e := newEnv(t, options{})
	b := e.browser()
	b.login(e.person())
	reached := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := httpx.Chain(reached, e.module.SessionMiddleware(), e.module.TenantMiddleware())

	call := func(cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "__Host-session", Value: b.sessionToken()})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d, want 401", rec.Code)
	}
	if rec := call(true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "auth.tenant_forbidden") {
		t.Fatalf("no tenant: %d %s, want 403 auth.tenant_forbidden", rec.Code, rec.Body)
	}
}

func TestAuthorizer_DeniedIsAuditedInTheTenant(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	e.browser().login(who)
	uid := e.userID(who)
	tenant := e.tenant()
	e.member(who, tenant, "member")

	p, err := authctx.NewPrincipal(uid, tenant, "member", testCatalog(t, 0).Permissions("member")...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithPrincipal(t.Context(), p)
	a := e.module.Authorizer()
	if err := a.Require(ctx, authz.PermMembersRead); err != nil {
		t.Fatalf("a held permission: %v", err)
	}
	if err := a.Require(ctx, authz.PermMembersManage); !errors.Is(err, authz.ErrDenied()) {
		t.Fatalf("Require = %v, want authz.denied", err)
	}
	got := e.scalar(`SELECT tenant_id::text || '|' || actor_user_id::text || '|' || target_id || '|' || (metadata->>'role')
		FROM audit_log WHERE action = 'authz.denied' AND tenant_id = $1`, tenant)
	if want := tenant + "|" + uid + "|members:manage|member"; got != want {
		t.Fatalf("authz.denied = %s, want %s", got, want)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'authz.denied' AND tenant_id = $1`, tenant); n != 1 {
		t.Fatalf("one denial, %d events", n)
	}
}

func TestMe_OnlyTheCurrentUsersMemberships(t *testing.T) {
	e := newEnv(t, options{})
	who, other := e.person(), e.person()
	b := e.browser()
	b.login(who)
	e.browser().login(other)
	mine, shared, foreign := e.tenant(), e.tenant(), e.tenant()
	e.member(who, mine, "member")
	e.member(who, shared, "owner")
	e.member(other, shared, "member")
	e.member(other, foreign, "admin")
	e.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, mine)

	res := b.do(http.MethodGet, "/me", "")
	if res.status != http.StatusOK {
		t.Fatalf("/me: %d %s", res.status, res.body)
	}
	var me struct {
		User struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
		Memberships []struct {
			TenantID     string `json:"tenant_id"`
			TenantSlug   string `json:"tenant_slug"`
			TenantName   string `json:"tenant_name"`
			TenantStatus string `json:"tenant_status"`
			Role         string `json:"role"`
		} `json:"memberships"`
	}
	if err := json.Unmarshal(res.body, &me); err != nil {
		t.Fatal(err)
	}
	if me.User.ID != e.userID(who) || me.User.Email != who.Email.Reveal() || me.User.DisplayName != who.DisplayName {
		t.Fatalf("user = %+v", me.User)
	}
	got := map[string]string{}
	for _, m := range me.Memberships {
		if m.TenantSlug == "" || m.TenantName == "" {
			t.Fatalf("membership without its tenant: %+v", m)
		}
		got[m.TenantID] = m.Role + "/" + m.TenantStatus
	}
	want := map[string]string{mine: "member/suspended", shared: "owner/active"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("memberships = %v, want %v (never another user's)", got, want)
	}

	if res := e.browser().do(http.MethodGet, "/me", ""); res.status != http.StatusUnauthorized {
		t.Fatalf("/me without a session: %d", res.status)
	}
	if res := b.doHeaders(http.MethodGet, "/me", "", http.Header{"X-Tenant-Id": {foreign}}); res.status != http.StatusOK {
		t.Fatalf("/me needs no tenant and ignores X-Tenant-ID: %d", res.status)
	}
}

// The first real route with a tenant (design 11, isolation): user A gets the same 403
// with tenant B as with any tenant they cannot use.
func TestCurrentTenant(t *testing.T) {
	e := newEnv(t, options{})
	who, other := e.person(), e.person()
	b := e.browser()
	b.login(who)
	e.browser().login(other)
	mine, foreign := e.tenant(), e.tenant()
	e.member(who, mine, "admin")
	e.member(other, foreign, "owner")

	get := func(tenant string) response {
		return b.doHeaders(http.MethodGet, "/tenant", "", http.Header{"X-Tenant-Id": {tenant}})
	}
	res := get(mine)
	if res.status != http.StatusOK {
		t.Fatalf("own tenant: %d %s", res.status, res.body)
	}
	var cur struct {
		ID          string   `json:"id"`
		Slug        string   `json:"slug"`
		Name        string   `json:"name"`
		Role        string   `json:"role"`
		Scope       string   `json:"scope"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(res.body, &cur); err != nil {
		t.Fatal(err)
	}
	wantSlug := e.scalar(`SELECT slug FROM tenants WHERE id = $1`, mine)
	if cur.ID != mine || cur.Slug != wantSlug || cur.Role != "admin" || cur.Scope != "tenant" ||
		!slices.Contains(cur.Permissions, string(authz.PermMembersManage)) ||
		slices.Contains(cur.Permissions, string(authz.PermTenantOwn)) {
		t.Fatalf("current tenant = %+v", cur)
	}

	for name, tenant := range map[string]string{"foreign": foreign, "malformed": "x", "missing": ""} {
		if res := get(tenant); res.status != http.StatusForbidden || res.problemCode(t) != "auth.tenant_forbidden" {
			t.Fatalf("%s tenant: %d %s", name, res.status, res.body)
		}
	}
	if res := e.browser().doHeaders(http.MethodGet, "/tenant", "", http.Header{"X-Tenant-Id": {mine}}); res.status != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", res.status)
	}
}

func TestAuthorizer_DeniedInsideARolledBackTransactionIsStillAudited(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	b := e.browser()
	b.login(who)
	tenant := e.tenant()
	e.member(who, tenant, "member")
	name := e.scalar(`SELECT name FROM tenants WHERE id = $1`, tenant)

	res := b.doHeaders(http.MethodPost, "/deny-in-tx", frontOrigin, http.Header{"X-Tenant-Id": {tenant}})
	if res.status != http.StatusForbidden || res.problemCode(t) != "authz.denied" {
		t.Fatalf("POST /deny-in-tx: %d %s, want 403 authz.denied", res.status, res.body)
	}
	if got := e.scalar(`SELECT name FROM tenants WHERE id = $1`, tenant); got != name {
		t.Fatalf("the use case's write was not rolled back: %q", got)
	}
	n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'authz.denied' AND tenant_id = $1
		AND actor_user_id = $2 AND target_id = 'members:manage'`, tenant, e.userID(who))
	if n != 1 {
		t.Fatalf("authz.denied events = %d, want 1 despite the rollback", n)
	}
}

func TestAuthorizer_DeniesWhenTheAuditFails(t *testing.T) {
	e := newEnv(t, options{})
	who := e.person()
	e.browser().login(who)
	// A tenant that does not exist: the audit insert breaks its foreign key.
	ghostTenant := "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f7777"
	p, err := authctx.NewPrincipal(e.userID(who), ghostTenant, "member")
	if err != nil {
		t.Fatal(err)
	}
	err = e.module.Authorizer().Require(authctx.WithPrincipal(t.Context(), p), authz.PermAuditRead)
	if !errors.Is(err, authz.ErrDenied()) {
		t.Fatalf("Require = %v, want authz.denied", err)
	}
	recs := e.logRecords("record authz.denied")
	if len(recs) != 1 || recs[0]["level"] != "ERROR" {
		t.Fatalf("the failed audit must be logged at Error: %v", recs)
	}
}
