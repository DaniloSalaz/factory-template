//go:build arch

package arch

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// appImportViolations lists every import that breaks the one-way hook between platform
// and app: nothing under core/ imports app or anything below it, directly or through
// a package outside core/ (a test helper in tests/, generated code), and only
// package main (the module root) imports app itself.
func appImportViolations(modPath string, pkgs []goPackage) []string {
	appRoot := modPath + "/app"
	intoApp := func(path string) bool { return path == appRoot || strings.HasPrefix(path, appRoot+"/") }
	inPlatform := func(path string) bool { return strings.HasPrefix(path, modPath+"/core/") }
	imports := make(map[string][]string, len(pkgs))
	for _, p := range pkgs {
		imports[p.ImportPath] = p.Imports
	}
	var out []string
	for _, p := range pkgs {
		for _, imp := range p.allImports() {
			switch {
			case inPlatform(p.ImportPath) && intoApp(imp):
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
			case inPlatform(p.ImportPath) && !inPlatform(imp):
				// An import inside core/ is checked where it is made; outside it, nothing
				// else would see the app it drags in.
				if chain := pathToApp(imp, imports, intoApp); chain != nil {
					out = append(out, fmt.Sprintf(
						"[platform-imports-app] %s imports %s, which reaches the app through %s: "+
							"the platform never depends on the app, not even through a helper or "+
							"generated code. Import a package that does not depend on the app (in "+
							"tests, testkit instead of testkit/migrationsets)",
						p.ImportPath, imp, strings.Join(chain, " -> ")))
				}
			}
		}
	}
	return out
}

// pathToApp returns the shortest import chain from start to a package of the app,
// following only non-test imports (a dependency's tests are not linked), or nil when
// start does not depend on the app. Packages outside the module have no entry in
// imports and end the walk.
func pathToApp(start string, imports map[string][]string, intoApp func(string) bool) []string {
	prev := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if intoApp(cur) {
			var chain []string
			for p := cur; p != ""; p = prev[p] {
				chain = append(chain, p)
			}
			slices.Reverse(chain)
			return chain
		}
		for _, next := range imports[cur] {
			if _, seen := prev[next]; !seen {
				prev[next] = cur
				queue = append(queue, next)
			}
		}
	}
	return nil
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
		{"kit imports the app hook", "core/kit/httpx", "app", true},
		{"platform module imports an app module's root", "core/b", "app/modules/c", true},
		{"platform module imports an app module's interior", "core/a/usecases", "app/modules/c/adapters/x", true},
		{"platform test file imports the app hook", "core/b_test", "app", true},
		{"non-main package imports the app hook", "gen/sqlc/a", "app", true},
		{"platform test reaches an app module through a test helper", "core/b_test", "tests/sets", true},
		{"platform adapter reaches the app through generated code", "core/a/adapters/postgres", "gen/sqlc/a", true},
		{"platform use cases reach the app through generated code", "core/b/usecases", "gen/sqlc/a", true},
		{"platform test uses a helper without the app", "core/b_test", "tests/testkit", false},
		{"main imports the app hook", "", "app", false},
		{"app hook imports an app module", "app", "app/modules/c", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			importer := strings.TrimSuffix(strings.TrimSuffix(modPath+"/"+tc.importer, "/"), "_test") // "" is the module root
			// ":" ends a direct import, ", which reaches" one through another package.
			direct := importer + " imports " + modPath + "/" + tc.imported + ":"
			indirect := importer + " imports " + modPath + "/" + tc.imported + ", which reaches"
			needle := direct + " | " + indirect
			found := slices.ContainsFunc(got, func(v string) bool {
				return strings.Contains(v, direct) || strings.Contains(v, indirect)
			})
			if found != tc.want {
				t.Fatalf("violation %q reported = %v, want %v\nall violations:\n%s", needle, found, tc.want, strings.Join(got, "\n"))
			}
		})
	}
	if len(got) != 8 {
		t.Fatalf("got %d violations, want 8:\n%s", len(got), strings.Join(got, "\n"))
	}
}
