package postgres

// Violation: sets a variable outside platform/kit/db.
const q = `SELECT SET_CONFIG('x', 'y', true)`
