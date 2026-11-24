package postgres

// Violation: sets a variable outside core/kit/db.
const q = `SELECT SET_CONFIG('x', 'y', true)`
