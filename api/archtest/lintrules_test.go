//go:build arch

package archtest

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintRules runs golangci-lint with the real config against each fixture module in
// testdata/lint (they share the real module path so the rules apply). Each fixture holds
// files that break a rule and files that are allowed; the test checks both.
func TestLintRules(t *testing.T) {
	t.Parallel()

	bin, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Fatal("golangci-lint not found in PATH; run the tests through `mise run arch`")
	}
	cfg, err := filepath.Abs("../.golangci.yml")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}

	tests := []struct {
		fixture string
		want    []string // each must appear in the output
		allowed []string // files that must not be reported
	}{
		{
			fixture: "env-only-in-config",
			want: []string{
				"platform/kit/httpx/port.go", "platform/foo/foo.go",
				"[env-only-in-config]", "add the variable to config.Config",
			},
			allowed: []string{"platform/kit/config/"},
		},
		{
			fixture: "platform-no-modules",
			want: []string{
				"platform/kit/httpx/httpx.go", "platform/kit/httpx/app.go", "[platform-no-modules]", "move the code into",
			},
			allowed: []string{"platform/kit/httpx/kit.go"},
		},
		{
			fixture: "apperr-stdlib-only",
			want:    []string{"platform/kit/apperr/apperr.go", "platform/kit/logx", "[apperr-stdlib-only]", "only the standard library"},
		},
		{
			fixture: "authctx-stdlib-only",
			want:    []string{"platform/kit/authctx/authctx.go", "platform/kit/logx", "[authctx-stdlib-only]", "only the standard library"},
		},
		{
			fixture: "authz-in-use-cases",
			want: []string{
				"platform/foo/adapters/http/handler.go", "app/modules/bar/adapters/http/handler.go",
				"platform/kit/authz", "[authz-in-use-cases]", "Authorizer.Require",
			},
			allowed: []string{"usecases/usecase.go"},
		},
		{
			fixture: "domain-pure",
			want: []string{
				"domain/transport.go", "'net/http'", "transport belongs in adapters/http",
				"platform/foo/domain/platform.go", "app/modules/bar/domain/platform.go",
				"platform/kit/logx", "[domain-pure]", "behind a port",
			},
			allowed: []string{"domain/allowed.go"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()

			// golangci-lint aborts when another instance holds its lock (these subtests, or a
			// lint running in another terminal); waiting for the lock keeps the shared cache safe.
			cmd := exec.CommandContext(t.Context(), bin, "run", "--allow-serial-runners", "--config", cfg, "./...")
			cmd.Dir = filepath.Join("testdata", "lint", tc.fixture)
			out, err := cmd.CombinedOutput()
			output := string(out)

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("golangci-lint should report issues (exit 1), got err=%v:\n%s", err, output)
			}
			for _, want := range tc.want {
				if !strings.Contains(output, want) {
					t.Errorf("output does not contain %q:\n%s", want, output)
				}
			}
			for _, allowed := range tc.allowed {
				if strings.Contains(output, allowed) {
					t.Errorf("allowed file %q was reported:\n%s", allowed, output)
				}
			}
		})
	}
}
