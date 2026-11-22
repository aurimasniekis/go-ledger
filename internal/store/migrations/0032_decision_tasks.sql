-- Decision tasks (task L6, ADR-0063, decision D13): the reversal record of an
-- automatically applied suggestion, and in-app alerts.
--
-- decision_suggestion_reversals: a person took back a suggestion's effect (the
-- model-origin category it auto-applied). The suggestion itself stays as it was
-- (accepted, with its provider and model): its state cannot move twice, and the
-- history of what the model said and what the person did must both stay. At most
-- one reversal per suggestion; rows are never edited or deleted.
CREATE TABLE decision_suggestion_reversals (
    suggestion_id TEXT PRIMARY KEY REFERENCES decision_suggestions (id) ON DELETE RESTRICT CHECK (length(suggestion_id) = 26 AND suggestion_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    reverted_by_kind TEXT NOT NULL CHECK (reverted_by_kind IN ('user', 'token', 'system')),
    reverted_by_user TEXT CHECK (reverted_by_user IS NULL OR (length(reverted_by_user) = 26 AND reverted_by_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    reverted_at   TEXT NOT NULL CHECK (length(reverted_at) = 30 AND reverted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE TRIGGER decision_suggestion_reversals_immutable_update
BEFORE UPDATE ON decision_suggestion_reversals
BEGIN
    SELECT RAISE(ABORT, 'a decision suggestion reversal is never edited');
END;

CREATE TRIGGER decision_suggestion_reversals_immutable_delete
BEFORE DELETE ON decision_suggestion_reversals
BEGIN
    SELECT RAISE(ABORT, 'a decision suggestion reversal is never deleted');
END;

-- alerts: in-app alerts (decision D13: in-app unless a channel is selected).
-- kind is one of the three D13 use cases. The facts are structured values written
-- by Ledger's own code (amounts as exact decimal text with their currency); message
-- is Ledger's own wording. model_text is optional wording a model wrote for the
-- alerts task: it never replaces message, and carries its provider and model.
-- dedupe_key makes raising idempotent: at most one open alert per key.
CREATE TABLE alerts (
    id             TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    kind           TEXT NOT NULL CHECK (kind IN ('consent_expiring', 'commitment_amount_changed', 'unmatched_overdue')),
    object_type    TEXT NOT NULL CHECK (object_type IN ('bank_connection', 'commitment', 'occurrence')),
    object_id      TEXT NOT NULL CHECK (length(object_id) BETWEEN 1 AND 200),
    dedupe_key     TEXT NOT NULL CHECK (length(dedupe_key) BETWEEN 1 AND 300),
    facts          TEXT NOT NULL CHECK (json_valid(facts) AND json_type(facts) = 'object'),
    message        TEXT NOT NULL CHECK (length(message) BETWEEN 1 AND 500),
    model_text     TEXT CHECK (model_text IS NULL OR length(model_text) BETWEEN 1 AND 280),
    model_provider TEXT CHECK (model_provider IS NULL OR model_provider IN ('openrouter', 'openai', 'anthropic', 'typesafe')),
    model_name     TEXT CHECK (model_name IS NULL OR length(model_name) BETWEEN 1 AND 200),
    state          TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'dismissed', 'resolved')),
    created_at     TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    closed_at      TEXT CHECK (closed_at IS NULL OR (length(closed_at) = 30 AND closed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    version        INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((state = 'open') = (closed_at IS NULL)),
    CHECK ((model_text IS NULL) = (model_provider IS NULL) AND (model_text IS NULL) = (model_name IS NULL))
) STRICT;

CREATE UNIQUE INDEX alerts_one_open_per_key ON alerts (dedupe_key) WHERE state = 'open';
CREATE INDEX alerts_state ON alerts (state, created_at);

CREATE TRIGGER alerts_only_close
BEFORE UPDATE ON alerts
WHEN (OLD.state <> 'open' AND (NEW.state IS NOT OLD.state OR NEW.closed_at IS NOT OLD.closed_at))
  OR NEW.id IS NOT OLD.id OR NEW.kind IS NOT OLD.kind OR NEW.object_type IS NOT OLD.object_type OR NEW.object_id IS NOT OLD.object_id
  OR NEW.dedupe_key IS NOT OLD.dedupe_key OR NEW.facts IS NOT OLD.facts OR NEW.message IS NOT OLD.message OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'an alert only changes by being closed (and its model wording added once)');
END;

CREATE TRIGGER alerts_no_delete
BEFORE DELETE ON alerts
BEGIN
    SELECT RAISE(ABORT, 'alerts are closed, never deleted');
END;
