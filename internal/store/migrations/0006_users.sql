-- Users and roles (A1, ADR-0021). One row per user account. Credentials
-- (password hashes, TOTP, OIDC links, sessions, API tokens) live in their own
-- tables, added by later migrations. Users are disabled, never deleted, so
-- audit entries and token ownership keep pointing at a real row.
-- The role values follow internal/domain/authz; adding a role is a table
-- rebuild. The last active administrator is protected in code
-- (users.CheckLastAdministrator), inside the write transaction.
CREATE TABLE users (
    id           TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    username     TEXT NOT NULL UNIQUE CHECK (length(username) BETWEEN 1 AND 64 AND username NOT GLOB '*[^a-z0-9._-]*' AND substr(username, 1, 1) GLOB '[a-z0-9]'),
    display_name TEXT CHECK (display_name IS NULL OR length(display_name) BETWEEN 1 AND 100),
    role         TEXT NOT NULL CHECK (role IN ('administrator', 'read-only')),
    disabled     INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
    disabled_at  TEXT CHECK (disabled_at IS NULL OR (length(disabled_at) = 30 AND disabled_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    created_at   TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at   TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version      INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((disabled = 1) = (disabled_at IS NOT NULL))
) STRICT;
