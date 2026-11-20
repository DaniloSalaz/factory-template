package db

// Allowed: platform/db is the one place that sets the variables.
const setVars = `SELECT set_config('app.tenant_id', $1, true), set_config('app.user_id', $2, true), set_config('app.scope', $3, true)`
