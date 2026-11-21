//go:build arch

package archtest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The patterns are split so that this file does not match them itself.
var rlsVarPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bset` + `_config\b`),
	regexp.MustCompile(`\bapp` + `\.(tenant_id|user_id|scope)\b`),
}

// rlsVarViolations lists every Go file under root, tests and generated code included,
// that calls the function setting RLS variables, or names one of them, outside
// platform/kit/db. Fixtures
// under archtest/testdata are checked by their own test.
func rlsVarViolations(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "platform/kit/db" || rel == "archtest/testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, re := range rlsVarPatterns {
				if m := re.FindString(line); m != "" {
					out = append(out, fmt.Sprintf(
						"[rls-vars-only-in-db] %s:%d mentions %q: only platform/kit/db sets or reads "+
							"the RLS variables; run the statements inside db.WithTenantTx, WithUserTx or "+
							"WithAnonTx, which set them for you", rel, i+1, m))
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return out, nil
}

func TestRLSVarsOnlyInDB(t *testing.T) {
	t.Parallel()

	got, err := rlsVarViolations("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		t.Error(v)
	}
}

// The fixture proves the rule catches each kind of mention and allows platform/kit/db and
// look-alike names.
func TestRLSVarsOnlyInDB_DetectsViolations(t *testing.T) {
	t.Parallel()

	got, err := rlsVarViolations("testdata/rlsvars")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		file string
		want bool
	}{
		{"platform/a/adapters/postgres/setconfig.go", true},
		{"platform/a/adapters/postgres/setlocal_test.go", true},
		{"gen/sqlc/a/query.sql.go", true},
		{"platform/b/userid.go", true},
		{"platform/b/allowed.go", false},
		{"platform/kit/db/tx.go", false},
		{"platform/kit/db/schematest/rls_test.go", false},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()

			found := slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, " "+tc.file+":") })
			if found != tc.want {
				t.Fatalf("violation in %s reported = %v, want %v\nall violations:\n%s", tc.file, found, tc.want, strings.Join(got, "\n"))
			}
		})
	}
	if len(got) != 4 {
		t.Fatalf("got %d violations, want 4:\n%s", len(got), strings.Join(got, "\n"))
	}
}
