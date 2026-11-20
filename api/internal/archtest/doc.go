// Package archtest checks architecture rules that the linters cannot express. Its tests
// are behind the "arch" build tag and run with `mise run arch`:
//
//   - module boundaries: outside internal/modules/<x>, only the root package of module x
//     may be imported (never its domain, app or adapters), and the generated code under
//     gen/openapi/<x> and gen/sqlc/<x> is private to module x;
//   - the depguard and forbidigo rules in .golangci.yml still catch their fixtures in
//     testdata/lint, so a config change cannot silently disable them.
package archtest
