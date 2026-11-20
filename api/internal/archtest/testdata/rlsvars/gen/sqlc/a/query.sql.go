package a

// Violation: generated code reads a variable (the query in queries/ is the culprit).
const readScope = `SELECT current_setting('app.scope')`
