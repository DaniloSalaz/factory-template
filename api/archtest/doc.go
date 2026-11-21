// Package archtest checks architecture rules that the linters cannot express. Its tests
// are behind the "arch" build tag and run with `mise run arch`:
//
//   - module boundaries: a module is platform/<x> (any x but kit) or app/modules/<x>;
//     outside it, only its root package may be imported (never its domain, usecases or
//     adapters), and the generated code under gen/openapi/<x> and gen/sqlc/<x> is private
//     to module x;
//   - RLS variables only in platform/kit/db: no Go file outside platform/kit/db, tests and
//     generated code included, calls the function that sets them or names one of them,
//     so tenant data is reachable only through db.WithTenantTx;
//   - the depguard and forbidigo rules in .golangci.yml still catch their fixtures in
//     testdata/lint, so a config change cannot silently disable them.
package archtest
