-- Stable Google account id (OIDC `sub`) for users who signed in with Google.
-- Email stays the primary identity; sub is recorded so a later email change
-- on the Google side still maps to the same rk account.
ALTER TABLE users ADD COLUMN google_sub text;
CREATE UNIQUE INDEX users_google_sub_idx ON users (google_sub) WHERE google_sub IS NOT NULL;
