//go:build arch

package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// goPackage is the subset of `go list -json` output the rules need.
type goPackage struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
	Error        *struct{ Err string }
}

// allImports is every import of p, its tests' included, sorted and without duplicates.
func (p goPackage) allImports() []string {
	imports := slices.Concat(p.Imports, p.TestImports, p.XTestImports)
	slices.Sort(imports)
	return slices.Compact(imports)
}

// listPackages runs `go list -e -json ./...` in dir and returns its module path and
// packages. With -e an import cycle (the usual result of a forbidden import, such as
// platform/kit/db importing app) still lists every package, so the rules can name the
// import that caused it; the package errors are reported apart, and fail the test.
func listPackages(t *testing.T, dir string) (string, []goPackage) {
	t.Helper()

	modOut, err := runGo(t, dir, "list", "-m")
	if err != nil {
		t.Fatalf("go list -m in %s: %v", dir, err)
	}
	out, err := runGo(t, dir, "list", "-e", "-json", "./...")
	if err != nil {
		t.Fatalf("go list -json in %s: %v", dir, err)
	}

	var pkgs []goPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if p.Error != nil {
			t.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		pkgs = append(pkgs, p)
	}
	return strings.TrimSpace(string(modOut)), pkgs
}

func runGo(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return out, nil
}

// splitModule reports whether path belongs to a module, which one, and whether it is the
// module's interior (anything below its root package). Modules are the children of
// platform/ (except kit, the shared pieces without business) and of app/modules/.
func splitModule(modPath, path string) (name string, interior, ok bool) {
	for _, root := range []string{"/platform/", "/app/modules/"} {
		rest, found := strings.CutPrefix(path, modPath+root)
		if !found {
			continue
		}
		name, sub, _ := strings.Cut(rest, "/")
		if root == "/platform/" && name == "kit" {
			return "", false, false
		}
		return name, sub != "", true
	}
	return "", false, false
}

// privateOwner reports which module owns path when path is private to that module: the
// interior of platform/<x> (x != kit) or app/modules/<x>, or its generated code under
// gen/openapi/<x> and gen/sqlc/<x>.
func privateOwner(modPath, path string) (string, bool) {
	if name, interior, ok := splitModule(modPath, path); ok {
		return name, interior
	}
	for _, gen := range []string{"/gen/openapi/", "/gen/sqlc/"} {
		if rest, ok := strings.CutPrefix(path, modPath+gen); ok {
			name, _, _ := strings.Cut(rest, "/")
			return name, true
		}
	}
	return "", false
}

// moduleOf returns the module a package belongs to, or "" outside every module.
func moduleOf(modPath, path string) string {
	name, _, _ := splitModule(modPath, path)
	return name
}

// boundaryViolations lists every import that reaches into another module's private code.
func boundaryViolations(modPath string, pkgs []goPackage) []string {
	var out []string
	for _, p := range pkgs {
		importer := moduleOf(modPath, p.ImportPath)
		for _, imp := range p.allImports() {
			owner, private := privateOwner(modPath, imp)
			if !private || owner == importer {
				continue
			}
			out = append(out, fmt.Sprintf(
				"[module-boundaries] %s imports %s, which is private to module %q: "+
					"outside module %s import only its root package (module.go) and "+
					"expose what you need there", p.ImportPath, imp, owner, owner))
		}
	}
	return out
}

func TestModuleBoundaries(t *testing.T) {
	t.Parallel()

	modPath, pkgs := listPackages(t, "..")
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	for _, v := range boundaryViolations(modPath, pkgs) {
		t.Error(v)
	}
}

// The fixture proves the rule catches each kind of violation and allows the legal cases.
func TestModuleBoundaries_DetectsViolations(t *testing.T) {
	t.Parallel()

	modPath, pkgs := listPackages(t, "testdata/arch")
	got := boundaryViolations(modPath, pkgs)

	tests := []struct {
		name     string
		importer string
		imported string
		want     bool
	}{
		{"another module's domain", "platform/b", "platform/a/domain", true},
		{"another module's generated code", "platform/b/usecases", "gen/sqlc/a", true},
		{"kit into a module's interior", "platform/kit/httpx", "platform/a/usecases", true},
		{"test files are checked too", "platform/b_test", "platform/a/adapters/postgres", true},
		{"app module into a platform module's interior", "app/modules/c", "platform/a/domain", true},
		{"platform module into an app module's interior", "platform/a/usecases", "app/modules/c/adapters/x", true},
		{"app hook into a platform module's interior", "app", "platform/a/usecases", true},
		{"another module's root package", "platform/b", "platform/a", false},
		{"app module uses a platform module's root", "app/modules/c", "platform/a", false},
		{"app hook uses a platform module's root", "app", "platform/a", false},
		{"app hook uses kit", "app", "platform/kit/authz", false},
		{"app hook uses an app module's root", "app", "app/modules/c", false},
		{"platform module uses an app module's root (boundaries allow it)", "platform/b", "app/modules/c", false},
		{"own interior", "platform/a/usecases", "platform/a/domain", false},
		{"app module's own interior", "app/modules/c", "app/modules/c/adapters/x", false},
		{"own generated code", "platform/a/adapters/postgres", "gen/sqlc/a", false},
		{"composition root uses module roots", "", "platform/a", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			importer := strings.TrimSuffix(strings.TrimSuffix(modPath+"/"+tc.importer, "/"), "_test") // "" is the module root
			needle := importer + " imports " + modPath + "/" + tc.imported + ","
			found := slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, needle) })
			if found != tc.want {
				t.Fatalf("violation %q reported = %v, want %v\nall violations:\n%s", needle, found, tc.want, strings.Join(got, "\n"))
			}
		})
	}
	if len(got) != 7 {
		t.Fatalf("got %d violations, want 7:\n%s", len(got), strings.Join(got, "\n"))
	}
}
