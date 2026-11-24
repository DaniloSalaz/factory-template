package b

// Violation: reads the user variable outside core/kit/db.
const who = "current_setting('app.user_id')"
