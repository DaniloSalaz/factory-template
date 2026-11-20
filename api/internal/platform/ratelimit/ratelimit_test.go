package ratelimit_test

import (
	"testing"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/ratelimit"
)

func TestMemory(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := ratelimit.NewMemory(2, time.Minute, func() time.Time { return now })
	allow := func(key string) bool {
		t.Helper()
		ok, err := l.Allow(t.Context(), key)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		return ok
	}

	for i := range 2 {
		if !allow("a") {
			t.Fatalf("request %d must pass", i+1)
		}
	}
	if allow("a") {
		t.Fatal("the third request in the window must be refused")
	}
	if !allow("b") {
		t.Fatal("another key has its own quota")
	}
	now = now.Add(time.Minute)
	if !allow("a") {
		t.Fatal("a new window resets the quota")
	}
}
