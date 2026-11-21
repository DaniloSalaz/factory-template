package postgres

// Violation: sets a variable outside platform/db.
const q = `SELECT SET_CONFIG('x', 'y', true)`
