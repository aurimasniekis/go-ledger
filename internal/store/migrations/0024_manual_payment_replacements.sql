-- Manual payments replaced by a bank match (X5, ADR-0049, decision D45).
--
-- When a bank transaction turns out to be the payment the person already
-- recorded by hand, they confirm its suggested match "as the payment I
-- recorded": the match is confirmed and the chosen manual payment records are
-- marked replaced by it, in one write transaction. A replaced record is kept
-- (never deleted, still listed and in the history) but stops counting as
-- payment evidence: the bank match counts instead, so the payment is not
-- counted twice.
--
-- manual_payment_replacements is that history, one row per record and
-- replacement. A row is active while restored_at is NULL; at most one active
-- row exists per record. Restoring (the confirmation was undone with
-- unconfirm inside its undo window, or the match was unmatched) stamps
-- restored_at and the history entry that did it: the record counts again.
-- Rows are never deleted and, once restored, never change. replaced_event_id
-- and restored_event_id name the match history entries (the confirmation, and
-- the unconfirm or unmatch); they are checked at commit because the store
-- writes the replacement before the entry in the same transaction.
-- Concurrency is the match's and the occurrence's version, both of which every
-- change increases, so this table has no version of its own.
CREATE TABLE manual_payment_replacements (
    id                TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    manual_payment_id TEXT NOT NULL REFERENCES manual_payments (id) ON DELETE RESTRICT CHECK (length(manual_payment_id) = 26 AND manual_payment_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    match_id          TEXT NOT NULL REFERENCES payment_matches (id) ON DELETE RESTRICT CHECK (length(match_id) = 26 AND match_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    replaced_event_id TEXT NOT NULL REFERENCES payment_match_events (id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED CHECK (length(replaced_event_id) = 26 AND replaced_event_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    replaced_at       TEXT NOT NULL CHECK (length(replaced_at) = 30 AND replaced_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    restored_event_id TEXT REFERENCES payment_match_events (id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED CHECK (restored_event_id IS NULL OR (length(restored_event_id) = 26 AND restored_event_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    restored_at       TEXT CHECK (restored_at IS NULL OR (length(restored_at) = 30 AND restored_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK ((restored_at IS NULL) = (restored_event_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX manual_payment_replacements_active ON manual_payment_replacements (manual_payment_id) WHERE restored_at IS NULL;
CREATE INDEX manual_payment_replacements_match ON manual_payment_replacements (match_id, restored_at);

-- A record is replaced only by a match of its own occurrence, in its own
-- currency (amounts are never converted), and only while it counts: not
-- deleted.
CREATE TRIGGER manual_payment_replacements_target
BEFORE INSERT ON manual_payment_replacements
WHEN (SELECT occurrence_id FROM manual_payments WHERE id = NEW.manual_payment_id) IS NOT (SELECT occurrence_id FROM payment_matches WHERE id = NEW.match_id)
  OR (SELECT currency FROM manual_payments WHERE id = NEW.manual_payment_id) IS NOT (SELECT currency FROM payment_matches WHERE id = NEW.match_id)
  OR (SELECT deleted_at FROM manual_payments WHERE id = NEW.manual_payment_id) IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a manual payment is replaced only by a match of its occurrence, in its currency, while it is not deleted');
END;

-- Only an open match replaces a record: the store writes the replacement while
-- confirming the suggestion, in the same transaction.
CREATE TRIGGER manual_payment_replacements_open_match
BEFORE INSERT ON manual_payment_replacements
WHEN (SELECT state FROM payment_matches WHERE id = NEW.match_id) NOT IN ('suggested', 'confirmed')
BEGIN
    SELECT RAISE(ABORT, 'a manual payment is replaced only by an open payment match');
END;

-- History: a row only ever gains its restoration, once.
CREATE TRIGGER manual_payment_replacements_restore_only
BEFORE UPDATE ON manual_payment_replacements
WHEN OLD.restored_at IS NOT NULL
  OR NEW.restored_at IS NULL
  OR NEW.id IS NOT OLD.id OR NEW.manual_payment_id IS NOT OLD.manual_payment_id OR NEW.match_id IS NOT OLD.match_id
  OR NEW.replaced_event_id IS NOT OLD.replaced_event_id OR NEW.replaced_at IS NOT OLD.replaced_at
BEGIN
    SELECT RAISE(ABORT, 'a manual payment replacement only gains its restoration, once');
END;

CREATE TRIGGER manual_payment_replacements_kept
BEFORE DELETE ON manual_payment_replacements
BEGIN
    SELECT RAISE(ABORT, 'a manual payment replacement is history and is never deleted');
END;
