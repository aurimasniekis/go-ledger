-- Audit log (A5, ADR-0025). Append-only: one row per audited event, never
-- rewritten or deleted (triggers refuse both). There is no free-form payload:
-- details is a small JSON object of the fields the action declares
-- (internal/domain/audit), validated before insert, and no column holds a
-- request body. Actor and target IDs are plain text, not foreign keys, because
-- the actor can be the system or nobody, and tokens arrive in a later
-- migration; users are disabled, never deleted, so IDs stay meaningful.
CREATE TABLE audit_log (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    id             TEXT NOT NULL UNIQUE CHECK (length(id) = 26),
    action         TEXT NOT NULL CHECK (length(action) BETWEEN 3 AND 64 AND action GLOB '[a-z]*' AND action NOT GLOB '*[^a-z0-9._]*'),
    actor_kind     TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system', 'anonymous')),
    actor_user_id  TEXT NOT NULL DEFAULT '' CHECK (actor_user_id = '' OR length(actor_user_id) = 26),
    actor_token_id TEXT NOT NULL DEFAULT '' CHECK (actor_token_id = '' OR length(actor_token_id) = 26),
    target_type    TEXT NOT NULL DEFAULT '' CHECK (length(target_type) <= 32),
    target_id      TEXT NOT NULL DEFAULT '' CHECK (length(target_id) <= 64),
    occurred_at    TEXT NOT NULL CHECK (length(occurred_at) = 30 AND occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    details        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details) AND json_type(details) = 'object' AND length(details) <= 4096),
    CHECK ((actor_kind IN ('user', 'token')) = (actor_user_id <> '')),
    CHECK ((actor_kind = 'token') = (actor_token_id <> ''))
) STRICT;

CREATE INDEX audit_log_action ON audit_log (action, seq);
CREATE INDEX audit_log_actor_user ON audit_log (actor_user_id, seq) WHERE actor_user_id <> '';
CREATE INDEX audit_log_actor_token ON audit_log (actor_token_id, seq) WHERE actor_token_id <> '';
CREATE INDEX audit_log_target ON audit_log (target_type, target_id, seq);
CREATE INDEX audit_log_occurred_at ON audit_log (occurred_at);

CREATE TRIGGER audit_log_no_update
BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

CREATE TRIGGER audit_log_no_delete
BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
