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

// goPackage is the subset of `go list -json` output the rule needs.
type goPackage struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

// listPackages runs `go list -json ./...` in dir and returns its module path and packages.
func listPackages(t *testing.T, dir string) (string, []goPackage) {
	t.Helper()

	modOut, err := runGo(t, dir, "list", "-m")
	if err != nil {
		t.Fatalf("go list -m in %s: %v", dir, err)
	}
	out, err := runGo(t, dir, "list", "-json", "./...")
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

// privateOwner reports which module owns path when path is private to that module: the
// interior of internal/modules/<x> (anything below its root package) or its generated
// code under gen/openapi/<x> and gen/sqlc/<x>.
func privateOwner(modPath, path string) (string, bool) {
	if rest, ok := strings.CutPrefix(path, modPath+"/internal/modules/"); ok {
		name, sub, _ := strings.Cut(rest, "/")
		return name, sub != ""
	}
	for _, gen := range []string{"/gen/openapi/", "/gen/sqlc/"} {
		if rest, ok := strings.CutPrefix(path, modPath+gen); ok {
			name, _, _ := strings.Cut(rest, "/")
			return name, true
		}
	}
	return "", false
}

// moduleOf returns the module a package belongs to, or "" outside internal/modules.
func moduleOf(modPath, path string) string {
	rest, ok := strings.CutPrefix(path, modPath+"/internal/modules/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return name
}

// boundaryViolations lists every import that reaches into another module's private code.
func boundaryViolations(modPath string, pkgs []goPackage) []string {
	var out []string
	for _, p := range pkgs {
		importer := moduleOf(modPath, p.ImportPath)
		imports := slices.Concat(p.Imports, p.TestImports, p.XTestImports)
		slices.Sort(imports)
		for _, imp := range slices.Compact(imports) {
			owner, private := privateOwner(modPath, imp)
			if !private || owner == importer {
				continue
			}
			out = append(out, fmt.Sprintf(
				"[module-boundaries] %s imports %s, which is private to module %q: "+
					"outside internal/modules/%s import only its root package (module.go) and "+
					"expose what you need there", p.ImportPath, imp, owner, owner))
		}
	}
	return out
}

func TestModuleBoundaries(t *testing.T) {
	t.Parallel()

	modPath, pkgs := listPackages(t, "../..")
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
		{"another module's domain", "internal/modules/b", "internal/modules/a/domain", true},
		{"another module's generated code", "internal/modules/b/app", "gen/sqlc/a", true},
		{"platform into a module's interior", "internal/platform/httpx", "internal/modules/a/app", true},
		{"test files are checked too", "internal/modules/b_test", "internal/modules/a/adapters/postgres", true},
		{"another module's root package", "internal/modules/b", "internal/modules/a", false},
		{"own interior", "internal/modules/a/app", "internal/modules/a/domain", false},
		{"own generated code", "internal/modules/a/adapters/postgres", "gen/sqlc/a", false},
		{"composition root uses module roots", "cmd/api", "internal/modules/a", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			importer := strings.TrimSuffix(modPath+"/"+tc.importer, "_test")
			needle := importer + " imports " + modPath + "/" + tc.imported + ","
			found := slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, needle) })
			if found != tc.want {
				t.Fatalf("violation %q reported = %v, want %v\nall violations:\n%s", needle, found, tc.want, strings.Join(got, "\n"))
			}
		})
	}
	if len(got) != 4 {
		t.Fatalf("got %d violations, want 4:\n%s", len(got), strings.Join(got, "\n"))
	}
}
