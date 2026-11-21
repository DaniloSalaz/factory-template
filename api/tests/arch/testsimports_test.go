//go:build arch

package arch

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// testsImportViolations lists every import of api/tests from code that ships: a package
// outside tests/ whose non-test files (Imports, not TestImports or XTestImports) import
// tests/ or anything below it. The helpers there start containers and hold fixtures;
// they never go into the binary.
func testsImportViolations(modPath string, pkgs []goPackage) []string {
	testsRoot := modPath + "/tests"
	var out []string
	for _, p := range pkgs {
		if p.ImportPath == testsRoot || strings.HasPrefix(p.ImportPath, testsRoot+"/") {
			continue
		}
		for _, imp := range p.Imports {
			if imp != testsRoot && !strings.HasPrefix(imp, testsRoot+"/") {
				continue
			}
			out = append(out, fmt.Sprintf(
				"[tests-not-in-prod] %s imports %s outside a _test.go file: api/tests holds test "+
					"helpers and fixtures that never ship in the binary. Import it only from "+
					"_test.go files, or move the code you need out of tests/", p.ImportPath, imp))
		}
	}
	return out
}

func TestTestsNotInProd(t *testing.T) {
	t.Parallel()

	modPath, pkgs := listPackages(t, "../..")
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	for _, v := range testsImportViolations(modPath, pkgs) {
		t.Error(v)
	}
}

// The fixture proves the rule catches production code, integration-tagged files
// included, and allows test files and code under tests/.
func TestTestsNotInProd_DetectsViolations(t *testing.T) {
	t.Parallel()

	modPath, pkgs := listPackages(t, "testdata/arch")
	got := testsImportViolations(modPath, pkgs)

	tests := []struct {
		name     string
		importer string
		want     bool
	}{
		{"kit file behind the integration tag", "platform/kit/fakes", true},
		{"app module's production code", "app/modules/c/seed", true},
		{"external test package", "platform/b_test", false},
		{"internal test file", "platform/a", false},
		{"code under tests/", "tests/db", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			importer := strings.TrimSuffix(modPath+"/"+tc.importer, "_test")
			needle := importer + " imports " + modPath + "/tests/testkit "
			found := slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, needle) })
			if found != tc.want {
				t.Fatalf("violation %q reported = %v, want %v\nall violations:\n%s", needle, found, tc.want, strings.Join(got, "\n"))
			}
		})
	}
	if len(got) != 2 {
		t.Fatalf("got %d violations, want 2:\n%s", len(got), strings.Join(got, "\n"))
	}
}
