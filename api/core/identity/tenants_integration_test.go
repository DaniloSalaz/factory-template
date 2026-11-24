//go:build integration

package identity_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"chaskicode.dev/saas/core/identity/usecases"
)

// Tenants and members over HTTP (auth-y-tenancy §5.5 and §5.7, migration 00005), with the real
// middlewares and database.

// tenantJSON calls path as the browser in tenant (X-Tenant-ID) with body as JSON.
func (b *browser) tenantJSON(tenant, method, path string, body any) response {
	b.e.t.Helper()
	res, err := b.send(tenant, method, path, body)
	if err != nil {
		b.e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

// send is tenantJSON without the testing.T, for goroutines. tenant "" sends no
// X-Tenant-ID.
func (b *browser) send(tenant, method, path string, body any) (response, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(b.e.t.Context(), method, b.e.srv.URL+path, r)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Origin", frontOrigin)
	req.Header.Set("Content-Type", "application/json")
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	res, err := b.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		return response{}, err
	}
	return response{status: res.StatusCode, header: res.Header, body: out}, nil
}

// signedIn logs a fresh person in and returns them with their browser.
func (e *env) signedIn() (usecases.Authentication, *browser) {
	e.t.Helper()
	who := e.person()
	b := e.browser()
	b.login(who)
	return who, b
}

func (e *env) roleIn(tenant string, who usecases.Authentication) string {
	e.t.Helper()
	return e.scalar(`SELECT role FROM memberships m JOIN user_identities i ON i.user_id = m.user_id
		WHERE m.tenant_id = $1 AND i.subject = $2`, tenant, who.Subject)
}

// auditOf returns the tenant's audit events with action as actor/target/metadata.
func (e *env) auditOf(tenant, action string) []string {
	e.t.Helper()
	rows, err := e.super.Query(e.t.Context(), `SELECT concat_ws('|', actor_user_id, target_type, target_id, metadata::text)
		FROM audit_log WHERE tenant_id = $1 AND action = $2 ORDER BY created_at`, tenant, action)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func wantProblem(t *testing.T, res response, status int, code string) {
	t.Helper()
	if res.status != status || res.problemCode(t) != code {
		t.Fatalf("got %d %s, want %d %s", res.status, res.body, status, code)
	}
}

func TestTenants_Create(t *testing.T) {
	e := newEnv(t, options{maxTenants: 2})
	who, b := e.signedIn()
	slug := fmt.Sprintf("acme-%d", time.Now().UnixNano())

	res := b.tenantJSON("", http.MethodPost, "/tenants", map[string]string{"name": "  Acme  ", "slug": slug})
	if res.status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	var created struct{ ID, Slug, Name, Role string }
	if err := json.Unmarshal(res.body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Slug != slug || created.Name != "Acme" || created.Role != "owner" {
		t.Fatalf("created = %+v", created)
	}
	if got := e.roleIn(created.ID, who); got != "owner" {
		t.Fatalf("creator's role = %s, want owner", got)
	}
	uid := e.userID(who)
	if got := e.scalar(`SELECT created_by::text FROM tenants WHERE id = $1`, created.ID); got != uid {
		t.Fatalf("created_by = %s, want %s", got, uid)
	}
	if got := e.auditOf(created.ID, "tenant.created"); len(got) != 1 || !strings.HasPrefix(got[0], uid+"|tenant|"+created.ID+"|") {
		t.Fatalf("tenant.created = %v", got)
	}
	if got := e.auditOf(created.ID, "membership.created"); len(got) != 1 || !strings.Contains(got[0], `"role": "owner"`) {
		t.Fatalf("membership.created = %v", got)
	}
	// The creator can act in it right away (the owner of this catalog needs no MFA).
	if res := b.tenantJSON(created.ID, http.MethodGet, "/tenant", nil); res.status != http.StatusOK {
		t.Fatalf("GET /tenant: %d %s", res.status, res.body)
	}

	for _, tc := range []struct {
		name, slug, code string
		status           int
	}{
		{"invalid slug", "Acme Legal", "tenant.slug_invalid", http.StatusUnprocessableEntity},
		{"too short", "ab", "tenant.slug_invalid", http.StatusUnprocessableEntity},
		{"reserved", "admin", "tenant.slug_reserved", http.StatusUnprocessableEntity},
		{"taken", slug, "tenant.slug_taken", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantProblem(t, b.tenantJSON("", http.MethodPost, "/tenants", map[string]string{"name": "X", "slug": tc.slug}),
				tc.status, tc.code)
		})
	}

	// The second one fits the limit of 2; the third does not.
	if res := b.tenantJSON("", http.MethodPost, "/tenants", map[string]string{"name": "Two", "slug": slug + "-2"}); res.status != http.StatusCreated {
		t.Fatalf("second create: %d %s", res.status, res.body)
	}
	wantProblem(t, b.tenantJSON("", http.MethodPost, "/tenants", map[string]string{"name": "Three", "slug": slug + "-3"}),
		http.StatusForbidden, "tenant.limit_reached")
	if n := e.count(`SELECT count(*) FROM tenants WHERE created_by = $1`, uid); n != 2 {
		t.Fatalf("tenants created = %d, want 2", n)
	}
}

// With an owner role that requires MFA, creating the tenant does not ask for it; acting
// in the tenant does, from the first request.
func TestTenants_CreateDoesNotNeedTheOwnersMFA(t *testing.T) {
	e := newEnv(t, options{catalog: testCatalog(t, 12*time.Hour)})
	_, b := e.signedIn()
	res := b.tenantJSON("", http.MethodPost, "/tenants", map[string]string{"name": "MFA", "slug": fmt.Sprintf("mfa-%d", time.Now().UnixNano())})
	if res.status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	var created struct{ ID string }
	if err := json.Unmarshal(res.body, &created); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, b.tenantJSON(created.ID, http.MethodGet, "/tenant", nil), http.StatusForbidden, "auth.mfa_enrollment_required")
}

func TestTenants_Rename(t *testing.T) {
	e := newEnv(t, options{})
	tenant := e.tenant()
	old := e.scalar(`SELECT name FROM tenants WHERE id = $1`, tenant)
	admin, ab := e.signedIn()
	member, mb := e.signedIn()
	e.member(admin, tenant, "admin")
	e.member(member, tenant, "member")

	wantProblem(t, mb.tenantJSON(tenant, http.MethodPatch, "/tenant", map[string]string{"name": "Nope"}), http.StatusForbidden, "authz.denied")
	wantProblem(t, ab.tenantJSON(tenant, http.MethodPatch, "/tenant", map[string]string{"name": "  "}), http.StatusUnprocessableEntity, "tenant.name_invalid")
	// The slug is immutable and not part of the contract: refused, not silently ignored.
	res := ab.tenantJSON(tenant, http.MethodPatch, "/tenant", map[string]string{"name": "Renamed", "slug": "other"})
	wantProblem(t, res, http.StatusBadRequest, "unknown_field")
	if !strings.Contains(string(res.body), `"field":"slug"`) || e.scalar(`SELECT name FROM tenants WHERE id = $1`, tenant) != old {
		t.Fatalf("PATCH with slug: %d %s", res.status, res.body)
	}
	res = ab.tenantJSON(tenant, http.MethodPatch, "/tenant", map[string]string{"name": "Renamed"})
	if res.status != http.StatusOK || !strings.Contains(string(res.body), `"name":"Renamed"`) {
		t.Fatalf("rename: %d %s", res.status, res.body)
	}
	got := e.auditOf(tenant, "tenant.updated")
	want := e.userID(admin) + "|tenant|" + tenant + `|{"new_name": "Renamed", "old_name": "` + old + `"}`
	if !slices.Equal(got, []string{want}) {
		t.Fatalf("tenant.updated = %v, want [%s]", got, want)
	}
}

// staff is a tenant with two owners, two admins and a member, each signed in.
type staff struct {
	tenant                 string
	owner, owner2          usecases.Authentication
	admin, admin2, member  usecases.Authentication
	ob, o2b, ab, a2b, mb   *browser
	ownerID, owner2ID      string
	adminID, admin2ID, mID string
}

func (e *env) staff() staff {
	e.t.Helper()
	s := staff{tenant: e.tenant()}
	s.owner, s.ob = e.signedIn()
	s.owner2, s.o2b = e.signedIn()
	s.admin, s.ab = e.signedIn()
	s.admin2, s.a2b = e.signedIn()
	s.member, s.mb = e.signedIn()
	// In this order, which is the order of their memberships.
	for _, m := range []struct {
		who  usecases.Authentication
		role string
	}{{s.owner, "owner"}, {s.owner2, "owner"}, {s.admin, "admin"}, {s.admin2, "admin"}, {s.member, "member"}} {
		e.member(m.who, s.tenant, m.role)
	}
	s.ownerID, s.owner2ID = e.userID(s.owner), e.userID(s.owner2)
	s.adminID, s.admin2ID, s.mID = e.userID(s.admin), e.userID(s.admin2), e.userID(s.member)
	return s
}

func role(r string) map[string]string { return map[string]string{"role": r} }

func TestMembers_Hierarchy(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	patch := func(b *browser, userID, r string) response {
		return b.tenantJSON(s.tenant, http.MethodPatch, "/tenant/members/"+userID, role(r))
	}

	t.Run("admin cannot touch an owner", func(t *testing.T) {
		wantProblem(t, patch(s.ab, s.ownerID, "member"), http.StatusForbidden, "membership.target_forbidden")
		wantProblem(t, s.ab.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/"+s.ownerID, nil),
			http.StatusForbidden, "membership.target_forbidden")
	})
	t.Run("admin cannot touch another admin", func(t *testing.T) {
		wantProblem(t, patch(s.ab, s.admin2ID, "member"), http.StatusForbidden, "membership.target_forbidden")
	})
	t.Run("admin cannot name owners", func(t *testing.T) {
		wantProblem(t, patch(s.ab, s.mID, "owner"), http.StatusForbidden, "membership.role_not_assignable")
	})
	t.Run("member cannot manage", func(t *testing.T) {
		wantProblem(t, patch(s.mb, s.adminID, "member"), http.StatusForbidden, "authz.denied")
		wantProblem(t, s.mb.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/"+s.adminID, nil), http.StatusForbidden, "authz.denied")
	})
	t.Run("nobody changes their own role", func(t *testing.T) {
		wantProblem(t, patch(s.ob, s.ownerID, "admin"), http.StatusForbidden, "membership.self_change")
		wantProblem(t, patch(s.ab, s.adminID, "member"), http.StatusForbidden, "membership.self_change")
		wantProblem(t, s.ab.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/"+s.adminID, nil),
			http.StatusForbidden, "membership.self_change")
	})
	t.Run("an unknown role is never assigned", func(t *testing.T) {
		wantProblem(t, patch(s.ob, s.mID, "ghost-role"), http.StatusUnprocessableEntity, "membership.unknown_role")
	})
	if got := e.auditOf(s.tenant, "membership.role_changed"); len(got) != 0 {
		t.Fatalf("refused changes were audited: %v", got)
	}

	t.Run("admin promotes a member", func(t *testing.T) {
		res := patch(s.ab, s.mID, "admin")
		if res.status != http.StatusOK || !strings.Contains(string(res.body), `"role":"admin"`) {
			t.Fatalf("promote: %d %s", res.status, res.body)
		}
		want := s.adminID + "|user|" + s.mID + "|"
		got := e.auditOf(s.tenant, "membership.role_changed")
		if len(got) != 1 || !strings.HasPrefix(got[0], want) ||
			!strings.Contains(got[0], `"to_role": "admin"`) || !strings.Contains(got[0], `"from_role": "member"`) {
			t.Fatalf("membership.role_changed = %v", got)
		}
	})
	t.Run("owner demotes another owner", func(t *testing.T) {
		if res := patch(s.ob, s.owner2ID, "admin"); res.status != http.StatusOK {
			t.Fatalf("demote: %d %s", res.status, res.body)
		}
		if got := e.roleIn(s.tenant, s.owner2); got != "admin" {
			t.Fatalf("role = %s, want admin", got)
		}
	})
	t.Run("owner removes an admin", func(t *testing.T) {
		if res := s.ob.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/"+s.admin2ID, nil); res.status != http.StatusNoContent {
			t.Fatalf("remove: %d %s", res.status, res.body)
		}
		got := e.auditOf(s.tenant, "membership.removed")
		if len(got) != 1 || !strings.HasPrefix(got[0], s.ownerID+"|user|"+s.admin2ID+"|") || !strings.Contains(got[0], `"role": "admin"`) {
			t.Fatalf("membership.removed = %v", got)
		}
	})
}

func TestMembers_LastOwner(t *testing.T) {
	e := newEnv(t, options{})
	tenant := e.tenant()
	owner, ob := e.signedIn()
	admin, ab := e.signedIn()
	e.member(owner, tenant, "owner")
	e.member(admin, tenant, "admin")

	wantProblem(t, ob.tenantJSON(tenant, http.MethodDelete, "/tenant/members/me", nil), http.StatusConflict, "membership.last_owner")
	if got := e.roleIn(tenant, owner); got != "owner" {
		t.Fatalf("owner's role = %s", got)
	}

	// Leaving works for anyone else, whatever their permissions.
	if res := ab.tenantJSON(tenant, http.MethodDelete, "/tenant/members/me", nil); res.status != http.StatusNoContent {
		t.Fatalf("admin leaves: %d %s", res.status, res.body)
	}
	got := e.auditOf(tenant, "membership.left")
	if uid := e.userID(admin); len(got) != 1 || !strings.HasPrefix(got[0], uid+"|user|"+uid+"|") {
		t.Fatalf("membership.left = %v", got)
	}
	// And the one who left is out on the next request.
	wantProblem(t, ab.tenantJSON(tenant, http.MethodGet, "/tenant", nil), http.StatusForbidden, "auth.tenant_forbidden")
}

// Two owners demote (or remove) each other at the same time, from different
// goroutines: one wins and the tenant always keeps exactly one owner. When the two
// requests overlap, the loser gets 409 from the database (membership.last_owner, or
// membership.changed if its role changed between its reads and the lock). When the
// loser's request only starts once the winner has committed, the change has already
// taken effect: demoted, it is no longer an owner and the hierarchy refuses it (403
// membership.target_forbidden); removed, it cannot enter the tenant (403
// auth.tenant_forbidden). Most rounds overlap, so at least one 409 is required.
func TestMembers_ConcurrentOwnersKeepAnOwner(t *testing.T) {
	e := newEnv(t, options{})
	a, ab := e.signedIn()
	b, bb := e.signedIn()
	aID, bID := e.userID(a), e.userID(b)

	for _, op := range []struct {
		name   string
		method string
		body   any
		won    int
		late   string // what the loser gets when it starts after the winner committed
		rounds int
	}{
		{"demote", http.MethodPatch, role("admin"), http.StatusOK, "403 membership.target_forbidden", 50},
		{"remove", http.MethodDelete, nil, http.StatusNoContent, "403 auth.tenant_forbidden", 50},
	} {
		t.Run(op.name, func(t *testing.T) {
			outcomes := map[string]int{}
			for i := range op.rounds {
				tenant := e.tenant()
				e.member(a, tenant, "owner")
				e.member(b, tenant, "owner")

				var wg sync.WaitGroup
				results := make([]response, 2)
				errs := make([]error, 2)
				start := make(chan struct{})
				for j, call := range []struct {
					br     *browser
					target string
				}{{ab, bID}, {bb, aID}} {
					wg.Go(func() {
						<-start
						results[j], errs[j] = call.br.send(tenant, op.method, "/tenant/members/"+call.target, op.body)
					})
				}
				close(start)
				wg.Wait()
				for _, err := range errs {
					if err != nil {
						t.Fatal(err)
					}
				}

				if results[0].status != op.won {
					results[0], results[1] = results[1], results[0]
				}
				lost := fmt.Sprintf("%d %s", results[1].status, codeOf(results[1]))
				outcomes[lost]++
				switch {
				case results[0].status != op.won:
					t.Fatalf("round %d: nobody won: %s / %s", i, results[0].body, results[1].body)
				case lost != "409 membership.last_owner" && lost != "409 membership.changed" && lost != op.late:
					t.Fatalf("round %d: the loser got %s: %s", i, lost, results[1].body)
				}
				if n := e.count(`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND role = 'owner'`, tenant); n != 1 {
					t.Fatalf("round %d: %d owners left, want 1", i, n)
				}
			}
			t.Logf("losers: %v", outcomes)
			if outcomes["409 membership.last_owner"]+outcomes["409 membership.changed"] == 0 {
				t.Fatalf("no round overlapped enough to reach the database's check: %v", outcomes)
			}
		})
	}
}

func TestMembers_TakeEffectOnTheNextRequest(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()

	// Removed: the next request with the same cookie and X-Tenant-ID is refused.
	if res := s.ab.tenantJSON(s.tenant, http.MethodGet, "/tenant/members", nil); res.status != http.StatusOK {
		t.Fatalf("list before: %d %s", res.status, res.body)
	}
	if res := s.ob.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/"+s.admin2ID, nil); res.status != http.StatusNoContent {
		t.Fatalf("remove: %d %s", res.status, res.body)
	}
	wantProblem(t, s.a2b.tenantJSON(s.tenant, http.MethodGet, "/tenant", nil), http.StatusForbidden, "auth.tenant_forbidden")
	wantProblem(t, s.a2b.tenantJSON(s.tenant, http.MethodGet, "/tenant/members", nil), http.StatusForbidden, "auth.tenant_forbidden")

	// Demoted: members:manage is gone on the next request.
	if res := s.ob.tenantJSON(s.tenant, http.MethodPatch, "/tenant/members/"+s.adminID, role("member")); res.status != http.StatusOK {
		t.Fatalf("demote: %d %s", res.status, res.body)
	}
	res := s.ab.tenantJSON(s.tenant, http.MethodGet, "/tenant", nil)
	if res.status != http.StatusOK || strings.Contains(string(res.body), "members:manage") || !strings.Contains(string(res.body), `"role":"member"`) {
		t.Fatalf("GET /tenant after demotion: %d %s", res.status, res.body)
	}
	wantProblem(t, s.ab.tenantJSON(s.tenant, http.MethodPatch, "/tenant/members/"+s.mID, role("admin")), http.StatusForbidden, "authz.denied")
}

func TestMembers_List(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	other := e.tenant()
	outsider, _ := e.signedIn()
	e.member(outsider, other, "owner")

	var seen []string
	cursor := ""
	for page := 0; ; page++ {
		path := "/tenant/members?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		res := s.mb.tenantJSON(s.tenant, http.MethodGet, path, nil)
		if res.status != http.StatusOK {
			t.Fatalf("page %d: %d %s", page, res.status, res.body)
		}
		var p struct {
			Items []struct {
				UserID      string    `json:"user_id"`
				Email       string    `json:"email"`
				DisplayName string    `json:"display_name"`
				Role        string    `json:"role"`
				JoinedAt    time.Time `json:"joined_at"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := json.Unmarshal(res.body, &p); err != nil {
			t.Fatal(err)
		}
		for _, it := range p.Items {
			if it.Email == "" || it.Role == "" || it.JoinedAt.IsZero() || strings.Contains(it.Email, "***") {
				t.Fatalf("incomplete member: %+v", it)
			}
			seen = append(seen, it.UserID)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	want := []string{s.ownerID, s.owner2ID, s.adminID, s.admin2ID, s.mID}
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	got := slices.Clone(seen)
	slices.Sort(got)
	if !slices.Equal(got, sorted) || len(seen) != 5 {
		t.Fatalf("listed %v, want each of %v once", seen, want)
	}
	// Membership order is creation order.
	if !slices.Equal(seen, want) {
		t.Fatalf("order %v, want %v", seen, want)
	}

	wantProblem(t, s.mb.tenantJSON(s.tenant, http.MethodGet, "/tenant/members?cursor=garbage!", nil), http.StatusBadRequest, "membership.cursor_invalid")

	client, cb := e.signedIn()
	e.member(client, s.tenant, "client")
	wantProblem(t, cb.tenantJSON(s.tenant, http.MethodGet, "/tenant/members", nil), http.StatusForbidden, "authz.denied")
}

// With tenant A's context, nothing of tenant B is listed or changed: a member of B is
// "not a member" (404), the same answer as for a user who does not exist.
func TestMembers_Isolation(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()
	other := e.tenant()
	outsider, outsiderBrowser := e.signedIn()
	e.member(outsider, other, "member")
	e.member(s.owner, other, "owner")
	outsiderID := e.userID(outsider)
	const ghost = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f9999"

	for _, target := range []string{outsiderID, ghost} {
		a := s.ob.tenantJSON(s.tenant, http.MethodPatch, "/tenant/members/"+target, role("admin"))
		wantProblem(t, a, http.StatusNotFound, "membership.not_found")
		wantProblem(t, s.ob.tenantJSON(s.tenant, http.MethodDelete, "/tenant/members/"+target, nil), http.StatusNotFound, "membership.not_found")
	}
	res := s.ob.tenantJSON(s.tenant, http.MethodGet, "/tenant/members?limit=100", nil)
	var page struct {
		Items []struct {
			UserID string `json:"user_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.body, &page); err != nil || res.status != http.StatusOK {
		t.Fatalf("list of A: %d %s", res.status, res.body)
	}
	var listed []string
	for _, it := range page.Items {
		listed = append(listed, it.UserID)
	}
	if want := []string{s.ownerID, s.owner2ID, s.adminID, s.admin2ID, s.mID}; !slices.Equal(listed, want) {
		t.Fatalf("A lists %v, want exactly its members %v", listed, want)
	}
	// B is untouched and has no events from A.
	if got := e.roleIn(other, outsider); got != "member" {
		t.Fatalf("outsider's role in B = %s", got)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND action LIKE 'membership.%' AND action <> 'membership.created'`, other); n != 0 {
		t.Fatalf("B has %d membership events", n)
	}
	// A member of B only reaches B with its own header, never A.
	wantProblem(t, outsiderBrowser.tenantJSON(s.tenant, http.MethodGet, "/tenant/members", nil), http.StatusForbidden, "auth.tenant_forbidden")
}

func codeOf(r response) string {
	var p struct{ Code string }
	_ = json.Unmarshal(r.body, &p)
	return p.Code
}

// GET /tenant/roles: the catalog, with what each caller may assign and offer. An admin
// never sees owner as assignable or offerable; a client, without members:read, gets 403.
func TestTenantRoles(t *testing.T) {
	e := newEnv(t, options{})
	s := e.staff()

	// name:mfa_required:can_assign:can_offer, in the order of the response.
	summary := func(b *browser) []string {
		t.Helper()
		res := b.tenantJSON(s.tenant, http.MethodGet, "/tenant/roles", nil)
		var list struct {
			Items []struct {
				Name        string `json:"name"`
				MFARequired bool   `json:"mfa_required"`
				CanAssign   bool   `json:"can_assign"`
				CanOffer    bool   `json:"can_offer"`
			} `json:"items"`
		}
		if err := json.Unmarshal(res.body, &list); err != nil || res.status != http.StatusOK {
			t.Fatalf("GET /tenant/roles: %d %s", res.status, res.body)
		}
		var out []string
		for _, r := range list.Items {
			out = append(out, fmt.Sprintf("%s:%t:%t:%t", r.Name, r.MFARequired, r.CanAssign, r.CanOffer))
		}
		return out
	}
	for _, tc := range []struct {
		who  string
		b    *browser
		want []string
	}{
		{"owner", s.ob, []string{"owner:false:true:true", "admin:false:true:true", "member:false:true:true", "client:false:true:true"}},
		{"admin", s.ab, []string{"owner:false:false:false", "admin:false:true:true", "member:false:true:true", "client:false:true:true"}},
		{"member", s.mb, []string{"owner:false:false:false", "admin:false:false:false", "member:false:false:false", "client:false:false:false"}},
	} {
		if got := summary(tc.b); !slices.Equal(got, tc.want) {
			t.Errorf("%s: roles = %v, want %v", tc.who, got, tc.want)
		}
	}

	client, cb := e.signedIn()
	e.member(client, s.tenant, "client")
	wantProblem(t, cb.tenantJSON(s.tenant, http.MethodGet, "/tenant/roles", nil), http.StatusForbidden, "authz.denied")
	_, outsider := e.signedIn()
	wantProblem(t, outsider.tenantJSON(s.tenant, http.MethodGet, "/tenant/roles", nil), http.StatusForbidden, "auth.tenant_forbidden")
}
