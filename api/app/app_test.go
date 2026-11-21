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

// The template registers no module of its own; an app adds them in Modules.
func TestModules(t *testing.T) {
	t.Parallel()

	modules, err := Modules(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 0 {
		t.Fatalf("the template has no app modules, got %d", len(modules))
	}
}
