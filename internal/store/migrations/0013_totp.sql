-- Second factors (A3, ADR-0030).
--
-- user_totp holds at most one row per user: the active TOTP secret (secret,
-- enabled_at, last_step) and a started, unconfirmed enrolment (pending_secret,
-- pending_at). Either part may be absent, not both. Secrets are sealed with
-- AES-256-GCM under a key derived from the configured key-encryption key and
-- bound to the user ("v1." then base64url of nonce and ciphertext); never the
-- secret itself. last_step is the last accepted RFC 6238 time step: a code of
-- that step or an earlier one is refused, so a code works once.
--
-- user_recovery_codes holds the SHA-256 hashes of a user's unused recovery
-- codes (80 random bits each); a code is deleted when it is used. The codes
-- themselves are shown once, when they are issued.
CREATE TABLE user_totp (
    user_id        TEXT PRIMARY KEY REFERENCES users (id),
    secret         TEXT CHECK (secret IS NULL OR (length(secret) BETWEEN 40 AND 512 AND substr(secret, 1, 3) = 'v1.' AND substr(secret, 4) NOT GLOB '*[^A-Za-z0-9_-]*')),
    enabled_at     TEXT CHECK (enabled_at IS NULL OR (length(enabled_at) = 30 AND enabled_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_step      INTEGER NOT NULL DEFAULT 0 CHECK (last_step >= 0),
    pending_secret TEXT CHECK (pending_secret IS NULL OR (length(pending_secret) BETWEEN 40 AND 512 AND substr(pending_secret, 1, 3) = 'v1.' AND substr(pending_secret, 4) NOT GLOB '*[^A-Za-z0-9_-]*')),
    pending_at     TEXT CHECK (pending_at IS NULL OR (length(pending_at) = 30 AND pending_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK ((secret IS NULL) = (enabled_at IS NULL)),
    CHECK ((pending_secret IS NULL) = (pending_at IS NULL)),
    CHECK (secret IS NOT NULL OR pending_secret IS NOT NULL)
) STRICT;

CREATE TABLE user_recovery_codes (
    user_id    TEXT NOT NULL REFERENCES users (id),
    code_hash  TEXT NOT NULL CHECK (length(code_hash) = 64 AND code_hash NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    PRIMARY KEY (user_id, code_hash)
) STRICT;
