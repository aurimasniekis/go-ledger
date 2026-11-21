-- Single sign-on (A6, ADR-0033).
--
-- oidc_links ties a user account to one identity at the configured OpenID
-- Connect provider, keyed by the stable (issuer, subject) pair, never by email
-- address. An account has at most one link and an identity belongs to at most
-- one account. The subject is compared exactly as the provider sends it.
-- email is the address the provider last reported, kept for display only.
-- Changing the configured issuer leaves existing links unusable, by design.
--
-- recovery_sign_ins holds at most one one-time sign-in per user, issued from
-- the host (break-glass, decision D16). Only the SHA-256 hash of its token is
-- stored; the row is deleted when it is used.
--
-- sessions.method records how a session started (password, oidc or recovery):
-- an OIDC session is not confined to two-factor enrolment while Ledger trusts
-- the provider's authentication (decision D16). Existing sessions were all
-- password sessions.
CREATE TABLE oidc_links (
    id           TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    user_id      TEXT NOT NULL UNIQUE REFERENCES users (id),
    issuer       TEXT NOT NULL CHECK (length(issuer) BETWEEN 1 AND 2048),
    subject      TEXT NOT NULL CHECK (length(CAST(subject AS BLOB)) BETWEEN 1 AND 255),
    email        TEXT NOT NULL DEFAULT '' CHECK (length(email) <= 254),
    created_at   TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    last_used_at TEXT CHECK (last_used_at IS NULL OR (length(last_used_at) = 30 AND last_used_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    UNIQUE (issuer, subject)
) STRICT;

CREATE TABLE recovery_sign_ins (
    user_id    TEXT PRIMARY KEY REFERENCES users (id),
    token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    expires_at TEXT NOT NULL CHECK (length(expires_at) = 30 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

ALTER TABLE sessions ADD COLUMN method TEXT NOT NULL DEFAULT 'password' CHECK (method IN ('password', 'oidc', 'recovery'));
