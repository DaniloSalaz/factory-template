-- +goose Up
-- Login and sessions (design, sections 5.1, 5.5 and 7.1; plan step 3).

-- Issue #4. When the login stores a new email from the provider it must store the
-- provider's verification of that email in the same statement: keeping the old flag
-- would carry the previous email's verification over to the new one, and
-- accept_invitation trusts users.email_verified. This supersedes the 00001 comment that
-- email_verified never changes after sign-up.
GRANT UPDATE (email_verified) ON users TO app_runtime;

-- The identity provider's own session (WorkOS: the sid claim), needed to end it at
-- logout. Opaque, not a credential: it only names the session in the logout URL. The
-- existing table-level INSERT already covers it, and it never changes, so app_runtime
-- gets no UPDATE on it.
ALTER TABLE sessions ADD COLUMN provider_session_id text
  CONSTRAINT sessions_provider_session_id_not_empty CHECK (provider_session_id <> '');

-- +goose Down
ALTER TABLE sessions DROP COLUMN provider_session_id;
REVOKE UPDATE (email_verified) ON users FROM app_runtime;
