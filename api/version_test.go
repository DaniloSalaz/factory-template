package main

import "testing"

// Not parallel: it sets the package's link-time variable.
func TestVersion(t *testing.T) {
	prev := buildVersion
	t.Cleanup(func() { buildVersion = prev })

	buildVersion = "9f1c2ab"
	if got := version(); got != "9f1c2ab" {
		t.Fatalf("version() = %q, want the linked one", got)
	}
	buildVersion = ""
	if got := version(); got == "" {
		t.Fatal("version() is empty without a linked one; want the VCS revision or dev")
	}
}
