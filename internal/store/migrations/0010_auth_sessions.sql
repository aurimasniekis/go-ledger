-- Passwords and sign-in sessions (A2, ADR-0026).
--
-- user_passwords holds one Argon2id hash (PHC string) per user who has a
-- password; a user without a row cannot sign in with a password. Never the
-- password itself.
--
-- sessions are server-side sign-in sessions. The cookie carries a random token;
-- only its SHA-256 hash is stored, so a copy of the database cannot be replayed
-- as a session. The CSRF token is derived from the cookie token and not stored. A session ends by
-- deleting its row (sign-out, termination, password change, expiry), which the
-- next request notices because every request looks the session up.
CREATE TABLE user_passwords (
    user_id    TEXT PRIMARY KEY REFERENCES users (id),
    hash       TEXT NOT NULL CHECK (length(hash) BETWEEN 40 AND 512 AND substr(hash, 1, 10) = '$argon2id$'),
    changed_at TEXT NOT NULL CHECK (length(changed_at) = 30 AND changed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE TABLE sessions (
    id               TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    user_id          TEXT NOT NULL REFERENCES users (id),
    token_hash       TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    created_at       TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    last_seen_at     TEXT NOT NULL CHECK (length(last_seen_at) = 30 AND last_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    authenticated_at TEXT NOT NULL CHECK (length(authenticated_at) = 30 AND authenticated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    expires_at       TEXT NOT NULL CHECK (length(expires_at) = 30 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    user_agent       TEXT NOT NULL DEFAULT '' CHECK (length(user_agent) <= 256),
    address          TEXT NOT NULL DEFAULT '' CHECK (length(address) <= 64)
) STRICT;

CREATE INDEX sessions_user ON sessions (user_id, created_at);
CREATE INDEX sessions_expires_at ON sessions (expires_at);
CREATE INDEX sessions_last_seen_at ON sessions (last_seen_at);
