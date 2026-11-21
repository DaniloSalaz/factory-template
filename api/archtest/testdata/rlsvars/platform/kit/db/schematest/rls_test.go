package schematest

// Allowed: tests below platform/kit/db exercise the variables directly.
const vars = `SELECT set_config('app.scope', 'tenant', true)`
