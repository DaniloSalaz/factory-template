// Package archtest checks architecture rules that the linters cannot express. Its tests
// are behind the "arch" build tag and run with `mise run arch`:
//
//   - module boundaries: outside internal/modules/<x>, only the root package of module x
//     may be imported (never its domain, app or adapters), and the generated code under
//     gen/openapi/<x> and gen/sqlc/<x> is private to module x;
//   - RLS variables only in platform/db: no Go file outside internal/platform/db, tests and
//     generated code included, calls the function that sets them or names one of them,
//     so tenant data is reachable only through db.WithTenantTx;
//   - the depguard and forbidigo rules in .golangci.yml still catch their fixtures in
//     testdata/lint, so a config change cannot silently disable them.
package archtest
