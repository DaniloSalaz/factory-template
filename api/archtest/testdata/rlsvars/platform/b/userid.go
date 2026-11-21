package b

// Violation: reads the user variable outside platform/kit/db.
const who = "current_setting('app.user_id')"
