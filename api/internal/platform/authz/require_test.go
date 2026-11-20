package authz_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authz"
)

type denial struct {
	tenant, user string
	perm         authctx.Permission
}

type fakeAuditor struct {
	denials []denial
	err     error
}

func (f *fakeAuditor) RecordDenied(_ context.Context, p authctx.Principal, perm authctx.Permission) error {
	f.denials = append(f.denials, denial{tenant: p.TenantID(), user: p.UserID(), perm: perm})
	return f.err
}

const (
	userID   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f0001"
	tenantID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
)

func principalCtx(t *testing.T, perms ...authctx.Permission) context.Context {
	t.Helper()
	p, err := authctx.NewPrincipal(userID, tenantID, "member", perms...)
	if err != nil {
		t.Fatal(err)
	}
	return authctx.WithPrincipal(t.Context(), p)
}

func TestRequire_Granted(t *testing.T) {
	t.Parallel()

	aud := &fakeAuditor{}
	a := authz.NewAuthorizer(aud, slog.New(slog.DiscardHandler))
	if err := a.Require(principalCtx(t, authz.PermMembersRead), authz.PermMembersRead); err != nil {
		t.Fatalf("Require = %v", err)
	}
	if len(aud.denials) != 0 {
		t.Fatalf("a granted permission is not audited: %v", aud.denials)
	}
}

func TestRequire_DeniedIsAudited(t *testing.T) {
	t.Parallel()

	aud := &fakeAuditor{}
	a := authz.NewAuthorizer(aud, slog.New(slog.DiscardHandler))
	err := a.Require(principalCtx(t, authz.PermMembersRead), authz.PermMembersManage)
	if !errors.Is(err, authz.ErrDenied()) || apperr.KindOf(err) != apperr.KindForbidden {
		t.Fatalf("Require = %v, want authz.denied (403)", err)
	}
	want := denial{tenant: tenantID, user: userID, perm: authz.PermMembersManage}
	if len(aud.denials) != 1 || aud.denials[0] != want {
		t.Fatalf("denials = %v, want [%v]", aud.denials, want)
	}
}

func TestRequire_DeniedEvenIfTheAuditFails(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	a := authz.NewAuthorizer(&fakeAuditor{err: errors.New("db down")}, slog.New(slog.NewJSONHandler(&buf, nil)))
	if err := a.Require(principalCtx(t), authz.PermAuditRead); !errors.Is(err, authz.ErrDenied()) {
		t.Fatalf("Require = %v, want authz.denied", err)
	}
	if !strings.Contains(buf.String(), "record authz.denied") || !strings.Contains(buf.String(), "db down") {
		t.Fatalf("the failed audit must be logged: %s", buf.String())
	}
}

func TestRequire_WithoutPrincipal(t *testing.T) {
	t.Parallel()

	aud := &fakeAuditor{}
	a := authz.NewAuthorizer(aud, slog.New(slog.DiscardHandler))
	err := a.Require(t.Context(), authz.PermMembersRead)
	if err == nil || apperr.KindOf(err) != apperr.KindInternal {
		t.Fatalf("Require without a principal = %v, want an internal error", err)
	}
	if len(aud.denials) != 0 {
		t.Fatal("nothing to audit without a principal")
	}
}
