// Package arch checks architecture rules that the linters cannot express. Its tests
// are behind the "arch" build tag and run with `mise run arch`:
//
//   - module boundaries: a module is core/<x> (any x but kit) or app/modules/<x>;
//     outside it, only its root package may be imported (never its domain, usecases or
//     adapters), and the generated code under gen/openapi/<x> and gen/sqlc/<x> is private
//     to module x;
//   - core imports app: nothing under core/ imports app or anything below it, and
//     only package main imports app itself, the single place where an app hooks into the
//     core (app.Roles, app.Modules);
//   - RLS variables only in core/kit/db: no Go file outside core/kit/db (and the
//     schema tests in tests/db), tests and generated code included, calls the function
//     that sets them or names one of them, so tenant data is reachable only through
//     db.WithTenantTx;
//   - the depguard, forbidigo, revive and nolintlint rules in .golangci.yml still catch
//     their fixtures in testdata/lint, so a config change cannot silently disable them.;
//   - doc refs: every reference to a design document (auth-y-tenancy §5.2) or an ADR in
//     the comments of api/, web/src/ and web/e2e/, and in every AGENTS.md, leads to a section or a
//     file that exists.
//
// The packages are listed with the "integration" tag too, so the imports of the tests
// that need Docker are checked as well.
package arch
