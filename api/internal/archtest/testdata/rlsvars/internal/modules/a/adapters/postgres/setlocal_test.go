package postgres

// Violation: tests are checked too, and SET LOCAL counts as much as the function.
const setLocal = `SET LOCAL app.tenant_id = '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c'`
