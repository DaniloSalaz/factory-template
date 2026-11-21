package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"chaskicode.dev/saas/platform/health/domain"
	"chaskicode.dev/saas/platform/health/usecases"
	"chaskicode.dev/saas/platform/kit/apperr"
)

type fakeDBChecker struct {
	err    error
	called bool
}

func (f *fakeDBChecker) CheckDB(ctx context.Context) error {
	f.called = true
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("check called without deadline")
	}
	return f.err
}

func TestCheckReadiness(t *testing.T) {
	t.Parallel()

	dbErr := errors.New("connection refused")
	tests := []struct {
		name    string
		dbErr   error
		wantErr bool
	}{
		{name: "database up"},
		{name: "database down", dbErr: dbErr, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeDBChecker{err: tc.dbErr}
			err := usecases.NewCheckReadiness(fake, time.Second).Execute(t.Context())

			if !fake.called {
				t.Fatal("DBChecker was not called")
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Execute() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, apperr.Unavailable(domain.CodeDatabaseUnavailable, "")) {
				t.Fatalf("Execute() = %v, want code %s", err, domain.CodeDatabaseUnavailable)
			}
			if apperr.KindOf(err) != apperr.KindUnavailable {
				t.Fatalf("kind = %v, want unavailable", apperr.KindOf(err))
			}
			if !errors.Is(err, tc.dbErr) {
				t.Fatalf("Execute() = %v, want it to keep the cause", err)
			}
		})
	}
}
