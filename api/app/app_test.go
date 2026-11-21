package app

import "testing"

// The example catalog must stay valid: the API refuses to start otherwise.
func TestRoles(t *testing.T) {
	t.Parallel()

	c, err := Roles()
	if err != nil {
		t.Fatal(err)
	}
	if !c.StrictlyIncludes("member", "admin") || !c.StrictlyIncludes("admin", "owner") {
		t.Fatal("the example roles must nest: member < admin < owner")
	}
}
