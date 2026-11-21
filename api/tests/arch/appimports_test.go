//go:build arch

package arch

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// appImportViolations lists every import that breaks the one-way hook between platform
// and app: nothing under platform/ imports app or anything below it, and only package
// main (the module root) imports app itself.
func appImportViolations(modPath string, pkgs []goPackage) []string {
	appRoot := modPath + "/app"
	var out []string
	for _, p := range pkgs {
		inPlatform := strings.HasPrefix(p.ImportPath, modPath+"/platform/")
		for _, imp := range p.allImports() {
			intoApp := imp == appRoot || strings.HasPrefix(imp, appRoot+"/")
			switch {
			case inPlatform && intoApp:
				out = append(out, fmt.Sprintf(
					"[platform-imports-app] %s imports %s: the platform never depends on the app; "+
						"the app hooks in only through api/app (app.Roles, app.Modules), which main "+
						"wires. Move the code into the platform, or have the app pass it in through "+
						"a module's Deps", p.ImportPath, imp))
			case imp == appRoot && p.ImportPath != modPath:
				out = append(out, fmt.Sprintf(
					"[platform-imports-app] %s imports %s: only package main in api/ imports app, "+
						"the single place where the app hooks into the platform. Take what you need "+
						"through Deps from main instead", p.ImportPath, imp))
			}
		}
	}
	return out
}

func TestPlatformImportsApp(t *testing.T) {
	t.Parallel()

	modPath, pkgs := listPackages(t, "../..")
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	for _, v := range appImportViolations(modPath, pkgs) {
		t.Error(v)
	}
}

// The fixture proves the rule catches each kind of violation and allows the legal cases.
func TestPlatformImportsApp_DetectsViolations(t *testing.T) {
	t.Parallel()

	modPath, pkgs := listPackages(t, "testdata/arch")
	got := appImportViolations(modPath, pkgs)

	tests := []struct {
		name     string
		importer string
		imported string
		want     bool
	}{
		{"kit imports the app hook", "platform/kit/httpx", "app", true},
		{"platform module imports an app module's root", "platform/b", "app/modules/c", true},
		{"platform module imports an app module's interior", "platform/a/usecases", "app/modules/c/adapters/x", true},
		{"platform test file imports the app hook", "platform/b_test", "app", true},
		{"non-main package imports the app hook", "gen/sqlc/a", "app", true},
		{"main imports the app hook", "", "app", false},
		{"app hook imports an app module", "app", "app/modules/c", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			importer := strings.TrimSuffix(strings.TrimSuffix(modPath+"/"+tc.importer, "/"), "_test") // "" is the module root
			needle := importer + " imports " + modPath + "/" + tc.imported + ":"
			found := slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, needle) })
			if found != tc.want {
				t.Fatalf("violation %q reported = %v, want %v\nall violations:\n%s", needle, found, tc.want, strings.Join(got, "\n"))
			}
		})
	}
	if len(got) != 5 {
		t.Fatalf("got %d violations, want 5:\n%s", len(got), strings.Join(got, "\n"))
	}
}
