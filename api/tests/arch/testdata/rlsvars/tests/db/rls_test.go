package db

// Allowed: the schema tests in tests/db exercise the variables directly.
const vars = `SELECT set_config('app.scope', 'tenant', true)`
