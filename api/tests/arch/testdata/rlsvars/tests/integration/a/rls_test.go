package a_test

// Violation: only tests/db is exempt, not the rest of tests/; integration tests go
// through db.WithTenantTx like the code they test.
const vars = `SELECT current_setting('app.tenant_id')`
