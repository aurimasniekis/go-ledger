-- Unconfirm and provenance of payment match events (X4c, ADR-0045, decisions
-- D42 1a and 3a).
--
-- 1. A confirmed match may go back to suggested ("unconfirmed"), only for the
--    person who confirmed it and only inside the undo window the store checks
--    (ADR-0045). The transition trigger is recreated to allow it; rejected and
--    reversed stay final.
-- 2. Every match event records how it reached the server: session (a signed-in
--    browser), token (an API token over HTTP), mcp (an API token through the
--    MCP server) or system (the server itself, the matching engine, a host CLI
--    command). Rows written before this migration hold NULL: unknown. The
--    match keeps the provenance of its creation and of its latest decision.
-- 3. A history entry records the occurrence's version right after the change,
--    so the store can tell whether anything else touched the occurrence since
--    a confirmation (the undo window). NULL for older rows: their window is
--    closed.
--
-- payment_match_events is rebuilt to widen its action CHECK; rows are copied
-- in their history order, so rowid order stays chronological.
ALTER TABLE payment_matches ADD COLUMN created_by_via TEXT CHECK (created_by_via IS NULL OR created_by_via IN ('session', 'token', 'mcp', 'system'));
ALTER TABLE payment_matches ADD COLUMN decided_by_via TEXT CHECK (decided_by_via IS NULL OR decided_by_via IN ('session', 'token', 'mcp', 'system'));

-- How a match was created never changes, like who created it.
CREATE TRIGGER payment_matches_created_via_immutable
BEFORE UPDATE OF created_by_via ON payment_matches
WHEN NEW.created_by_via IS NOT OLD.created_by_via
BEGIN
    SELECT RAISE(ABORT, 'a payment match cannot change how it was created');
END;

-- State changes: suggested -> confirmed or rejected; confirmed -> rejected
-- (unmatched), reversed or suggested (unconfirmed, ADR-0045). Rejected and
-- reversed are final.
DROP TRIGGER payment_matches_transitions;
CREATE TRIGGER payment_matches_transitions
BEFORE UPDATE ON payment_matches
WHEN OLD.state IN ('rejected', 'reversed')
  OR NOT (NEW.state = OLD.state
       OR (OLD.state = 'suggested' AND NEW.state IN ('confirmed', 'rejected'))
       OR (OLD.state = 'confirmed' AND NEW.state IN ('rejected', 'reversed', 'suggested')))
BEGIN
    SELECT RAISE(ABORT, 'payment match state change not allowed');
END;

-- A suggestion carries no decision: going back to suggested clears it, and
-- the tolerance snapshot of the confirmation with it (D41 3a).
CREATE TRIGGER payment_matches_suggested_undecided
BEFORE UPDATE OF state ON payment_matches
WHEN NEW.state = 'suggested' AND OLD.state <> 'suggested'
  AND (NEW.decided_at IS NOT NULL OR NEW.decided_by_via IS NOT NULL OR NEW.tolerance_amount IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'an unconfirmed payment match carries no decision and no tolerance');
END;

CREATE TABLE payment_match_events_new (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    match_id    TEXT NOT NULL REFERENCES payment_matches (id) ON DELETE CASCADE,
    action      TEXT NOT NULL CHECK (action IN ('suggested', 'confirmed', 'rejected', 'unmatched', 'corrected', 'reversed', 'unconfirmed')),
    state       TEXT NOT NULL CHECK (state IN ('suggested', 'confirmed', 'rejected', 'reversed')),
    amount      TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency    TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    actor_user  TEXT CHECK (actor_user IS NULL OR (length(actor_user) = 26 AND actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    reason      TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    at          TEXT NOT NULL CHECK (length(at) = 30 AND at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    actor_via   TEXT CHECK (actor_via IS NULL OR actor_via IN ('session', 'token', 'mcp', 'system')),
    occurrence_version INTEGER CHECK (occurrence_version IS NULL OR occurrence_version >= 1),
    CHECK (actor_via IS NULL OR (actor_via = 'session' AND actor_kind = 'user') OR (actor_via IN ('token', 'mcp') AND actor_kind = 'token') OR (actor_via = 'system' AND actor_kind = 'system'))
) STRICT;

INSERT INTO payment_match_events_new (id, match_id, action, state, amount, currency, actor_kind, actor_user, reason, at)
    SELECT id, match_id, action, state, amount, currency, actor_kind, actor_user, reason, at FROM payment_match_events ORDER BY at, id;

DROP TABLE payment_match_events;
ALTER TABLE payment_match_events_new RENAME TO payment_match_events;

CREATE INDEX payment_match_events_match ON payment_match_events (match_id, at, id);

-- A history entry is in its match's currency.
CREATE TRIGGER payment_match_events_currency
BEFORE INSERT ON payment_match_events
WHEN NEW.currency IS NOT (SELECT currency FROM payment_matches WHERE id = NEW.match_id)
BEGIN
    SELECT RAISE(ABORT, 'a payment match history entry must be in the currency of its match');
END;

-- The history of a confirmed or reversed match is payment history too: it is
-- never deleted on its own. (A rejected match's history still goes with it
-- when its occurrence is regenerated; whether rejections should survive is an
-- open decision.)
CREATE TRIGGER payment_match_events_history_kept
BEFORE DELETE ON payment_match_events
WHEN (SELECT state FROM payment_matches WHERE id = OLD.match_id) IN ('confirmed', 'reversed')
BEGIN
    SELECT RAISE(ABORT, 'the history of a confirmed or reversed payment match is never deleted');
END;

CREATE TRIGGER payment_match_events_append_only
BEFORE UPDATE ON payment_match_events
BEGIN
    SELECT RAISE(ABORT, 'payment match history is append-only');
END;
