package ratelimit_test

import (
	"testing"
	"time"

	"chaskicode.dev/saas/core/kit/ratelimit"
)

func TestMemory(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := ratelimit.NewMemory(2, time.Minute, func() time.Time { return now })
	allow := func(key string) bool {
		t.Helper()
		d, err := l.Allow(t.Context(), key)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if d.Allowed != (d.RetryAfter == 0) {
			t.Fatalf("Allow = %+v: only a refusal has a RetryAfter", d)
		}
		return d.Allowed
	}

	for i := range 2 {
		if !allow("a") {
			t.Fatalf("request %d must pass", i+1)
		}
	}
	now = now.Add(20 * time.Second)
	if allow("a") {
		t.Fatal("the third request in the window must be refused")
	}
	// The refusal says when the window ends: 40 s after its first request's 20.
	if d, _ := l.Allow(t.Context(), "a"); d.RetryAfter != 40*time.Second {
		t.Fatalf("RetryAfter = %v, want 40s", d.RetryAfter)
	}
	if !allow("b") {
		t.Fatal("another key has its own quota")
	}
	now = now.Add(40 * time.Second)
	if !allow("a") {
		t.Fatal("a new window resets the quota")
	}
}
