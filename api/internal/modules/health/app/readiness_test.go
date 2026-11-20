package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/health/app"
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
		name       string
		dbErr      error
		wantReady  bool
		wantReason string
	}{
		{name: "database up", wantReady: true},
		{name: "database down", dbErr: dbErr, wantReason: "database unavailable"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeDBChecker{err: tc.dbErr}
			got := app.NewCheckReadiness(fake, time.Second).Execute(t.Context())

			if !fake.called {
				t.Fatal("DBChecker was not called")
			}
			if got.IsReady() != tc.wantReady {
				t.Fatalf("IsReady() = %v, want %v", got.IsReady(), tc.wantReady)
			}
			if got.Reason() != tc.wantReason {
				t.Fatalf("Reason() = %q, want %q", got.Reason(), tc.wantReason)
			}
			if !errors.Is(got.Cause(), tc.dbErr) {
				t.Fatalf("Cause() = %v, want %v", got.Cause(), tc.dbErr)
			}
		})
	}
}
