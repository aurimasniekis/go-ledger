-- Decision providers (task L1, ADR-0058, decision D13): consent per provider
-- and purpose, the per-task monthly request count, and model suggestions.
--
-- None of these tables refers to or changes a bank fact, a payment, a match,
-- an occurrence or an organisation assignment. A model's answer is kept here
-- as a suggestion until someone applies it through that feature's own rules
-- (always as a model origin, which a person's decision outranks).
--
-- decision_consents: the operator's consent to send the permitted fields to one
-- provider for one purpose. Consent is exact: a row for (typesafe,
-- payment_matching) enables nothing else. A consent is revoked, never edited
-- or deleted, so the history of what was allowed stays; at most one active
-- consent exists per provider and purpose. terms names the consent text the
-- operator accepted (its version), never a secret.
CREATE TABLE decision_consents (
    id              TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    provider        TEXT NOT NULL CHECK (provider IN ('openrouter', 'openai', 'anthropic', 'typesafe')),
    purpose         TEXT NOT NULL CHECK (purpose IN ('categorisation', 'label_suggestion', 'alerts', 'payment_matching')),
    terms           TEXT NOT NULL CHECK (length(terms) BETWEEN 1 AND 200),
    granted_by_kind TEXT NOT NULL CHECK (granted_by_kind IN ('user', 'token', 'system')),
    granted_by_user TEXT CHECK (granted_by_user IS NULL OR (length(granted_by_user) = 26 AND granted_by_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    granted_at      TEXT NOT NULL CHECK (length(granted_at) = 30 AND granted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    revoked_by_kind TEXT CHECK (revoked_by_kind IS NULL OR revoked_by_kind IN ('user', 'token', 'system')),
    revoked_by_user TEXT CHECK (revoked_by_user IS NULL OR (length(revoked_by_user) = 26 AND revoked_by_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    revoked_at      TEXT CHECK (revoked_at IS NULL OR (length(revoked_at) = 30 AND revoked_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((revoked_at IS NULL) = (revoked_by_kind IS NULL)),
    CHECK (revoked_at IS NOT NULL OR revoked_by_user IS NULL)
) STRICT;

CREATE UNIQUE INDEX decision_consents_active ON decision_consents (provider, purpose) WHERE revoked_at IS NULL;

CREATE TRIGGER decision_consents_only_revoked
BEFORE UPDATE ON decision_consents
WHEN OLD.revoked_at IS NOT NULL
  OR NEW.id IS NOT OLD.id OR NEW.provider IS NOT OLD.provider OR NEW.purpose IS NOT OLD.purpose OR NEW.terms IS NOT OLD.terms
  OR NEW.granted_by_kind IS NOT OLD.granted_by_kind OR NEW.granted_by_user IS NOT OLD.granted_by_user OR NEW.granted_at IS NOT OLD.granted_at
BEGIN
    SELECT RAISE(ABORT, 'a decision consent can only be revoked, once');
END;

CREATE TRIGGER decision_consents_no_delete
BEFORE DELETE ON decision_consents
BEGIN
    SELECT RAISE(ABORT, 'decision consents are revoked, never deleted');
END;

-- decision_usage: attempts counted per task and calendar month (UTC) against
-- the task's monthly request cap. Every attempt counts, retries and fallback
-- routes included, whatever came of it.
CREATE TABLE decision_usage (
    task       TEXT NOT NULL CHECK (task IN ('categorisation', 'label_suggestion', 'alerts', 'payment_matching')),
    period     TEXT NOT NULL CHECK (length(period) = 7 AND period GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    attempts   INTEGER NOT NULL CHECK (attempts >= 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    PRIMARY KEY (task, period)
) STRICT;

-- decision_suggestions: validated model answers. decision is the answer with
-- the candidates mapped back to Ledger's identifiers ({"choice": id|null,
-- "confidence": "0.9"}, {"choices": [...], ...} or {"text": "..."}). model is
-- the model the provider reported. A suggestion stays pending until accepted,
-- rejected or superseded by a newer one for the same task and object (at most
-- one is pending); only its state moves, once. auto_apply may only be set on
-- categorisation (decision D13).
CREATE TABLE decision_suggestions (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    task        TEXT NOT NULL CHECK (task IN ('categorisation', 'label_suggestion', 'alerts', 'payment_matching')),
    provider    TEXT NOT NULL CHECK (provider IN ('openrouter', 'openai', 'anthropic', 'typesafe')),
    model       TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
    response_id TEXT CHECK (response_id IS NULL OR length(response_id) BETWEEN 1 AND 200),
    object_type TEXT NOT NULL CHECK (length(object_type) BETWEEN 1 AND 32 AND object_type NOT GLOB '*[^a-z_]*'),
    object_id   TEXT NOT NULL CHECK (length(object_id) BETWEEN 1 AND 200),
    decision    TEXT NOT NULL CHECK (json_valid(decision) AND json_type(decision) = 'object'),
    auto_apply  INTEGER NOT NULL CHECK (auto_apply IN (0, 1)),
    state       TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'rejected', 'superseded')),
    created_at  TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    decided_at  TEXT CHECK (decided_at IS NULL OR (length(decided_at) = 30 AND decided_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    version     INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (auto_apply = 0 OR task = 'categorisation'),
    CHECK ((state = 'pending') = (decided_at IS NULL))
) STRICT;

CREATE UNIQUE INDEX decision_suggestions_one_pending ON decision_suggestions (task, object_type, object_id) WHERE state = 'pending';
CREATE INDEX decision_suggestions_object ON decision_suggestions (object_type, object_id, created_at);

CREATE TRIGGER decision_suggestions_insert_pending
BEFORE INSERT ON decision_suggestions
WHEN NEW.state <> 'pending'
BEGIN
    SELECT RAISE(ABORT, 'a decision suggestion starts pending');
END;

CREATE TRIGGER decision_suggestions_only_state
BEFORE UPDATE ON decision_suggestions
WHEN OLD.state <> 'pending'
  OR NEW.id IS NOT OLD.id OR NEW.task IS NOT OLD.task OR NEW.provider IS NOT OLD.provider OR NEW.model IS NOT OLD.model
  OR NEW.response_id IS NOT OLD.response_id OR NEW.object_type IS NOT OLD.object_type OR NEW.object_id IS NOT OLD.object_id
  OR NEW.decision IS NOT OLD.decision OR NEW.auto_apply IS NOT OLD.auto_apply OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'only a pending decision suggestion can change, and only its state');
END;
