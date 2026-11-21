-- API tokens (A4, ADR-0029).
--
-- A token is "ldgr_" and 43 random base62 characters. It is shown once, when
-- it is created; only its SHA-256 hash is stored, with the prefix and first
-- four random characters and the last four characters so a person can
-- recognise it. A copy of the database cannot be used as a token.
--
-- grants are the token's grants, comma-separated "resource:action" in sorted
-- order; a root token has none stored and holds whatever its owner's role
-- holds. Requests check the owner's current role every time, so demoting or
-- disabling the owner narrows or stops the token at once.
--
-- Revocation sets revoked_at; the row stays, so audit entries and the token
-- list keep pointing at it. Tokens are never edited through the API (only
-- revoked), so the table has no version column.
CREATE TABLE api_tokens (
    id             TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    owner_id       TEXT NOT NULL REFERENCES users (id),
    name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash     TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    display_prefix TEXT NOT NULL CHECK (length(display_prefix) = 9 AND substr(display_prefix, 1, 5) = 'ldgr_' AND substr(display_prefix, 6) NOT GLOB '*[^0-9A-Za-z]*'),
    last_chars     TEXT NOT NULL CHECK (length(last_chars) = 4 AND last_chars NOT GLOB '*[^0-9A-Za-z]*'),
    root           INTEGER NOT NULL CHECK (root IN (0, 1)),
    grants         TEXT NOT NULL CHECK (length(grants) <= 4096 AND grants NOT GLOB '*[^a-z0-9_:,]*'),
    created_at     TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    expires_at     TEXT CHECK (expires_at IS NULL OR (length(expires_at) = 30 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_used_at   TEXT CHECK (last_used_at IS NULL OR (length(last_used_at) = 30 AND last_used_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    revoked_at     TEXT CHECK (revoked_at IS NULL OR (length(revoked_at) = 30 AND revoked_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK ((root = 1) = (grants = ''))
) STRICT;

CREATE INDEX api_tokens_owner ON api_tokens (owner_id, id);
