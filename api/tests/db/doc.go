// Package db checks the database schema and its row-level security against a real
// Postgres, bootstrapped and migrated like a deployment, with the platform's and the
// app's migrations (testkit/migrationsets): it lives under api/tests, which may import
// the app, unlike anything under platform/.
//
//   - rules.go (tables with tenant_id, section 7.3), functions.go (SECURITY DEFINER
//     functions, 7.4) and privileges.go (the snapshot of app_runtime's privileges) hold
//     the rules; schema_test.go applies them to the whole schema;
//   - structure_test.go and privileges_test.go prove each rule catches a misbuilt
//     object;
//   - behavior_test.go, members_test.go and invitations_test.go run SQL as app_runtime
//     and check what RLS and the SECURITY DEFINER functions actually allow.
//
// The tests need Docker and carry the "integration" build tag (`mise run
// test:integration`).
package db
