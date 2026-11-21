-- Payment matches (X1, ADR-0035): links between a bank transaction and a
-- commitment occurrence, each with the amount of the transaction allocated to
-- that occurrence (decision D8 option a). One transaction can pay several
-- occurrences and an occurrence can be paid in parts: one row per
-- (transaction, occurrence) pair and decision. Matches are user data (or
-- machine suggestions awaiting a decision), kept apart from bank facts
-- (ADR-0013): nothing here is written by the bank import, and the bank facts
-- they refer to are kept with ON DELETE RESTRICT.
--
-- origin says who proposed the link: auto_rule (the matching engine, task
-- X2), model_suggestion (an optional model, suggestions only) or user.
-- state is suggested, confirmed, rejected or reversed. Only a confirmed link
-- is payment evidence (ADR-0031 EvidenceSources), and only while its
-- transaction is booked, still reported and not superseded. A rejected link
-- records that the transaction does not pay the occurrence (a rejected
-- suggestion or an undone match); a reversed link records a match that was
-- right but whose payment was reversed. Both are final.
--
-- amount is exact positive decimal text (ADR-0005) in currency, which must
-- be both the transaction's and the occurrence's currency: nothing is ever
-- converted. Sums are never computed in SQL: the store checks, inside the
-- write transaction, that confirmed allocations never exceed the
-- transaction's amount and never overpay the occurrence unless the user
-- explicitly allows it. Concurrency is the row's version; every change also
-- increases the occurrence's version (its ETag covers its payment state).
CREATE TABLE payment_matches (
    id               TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    transaction_id   TEXT NOT NULL REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    occurrence_id    TEXT NOT NULL REFERENCES commitment_occurrences (id) ON DELETE RESTRICT,
    amount           TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency         TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    origin           TEXT NOT NULL CHECK (origin IN ('auto_rule', 'model_suggestion', 'user')),
    state            TEXT NOT NULL CHECK (state IN ('suggested', 'confirmed', 'rejected', 'reversed')),
    created_by_kind  TEXT NOT NULL CHECK (created_by_kind IN ('user', 'token', 'system')),
    created_by_user  TEXT CHECK (created_by_user IS NULL OR (length(created_by_user) = 26 AND created_by_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    decided_by_kind  TEXT CHECK (decided_by_kind IS NULL OR decided_by_kind IN ('user', 'token', 'system')),
    decided_by_user  TEXT CHECK (decided_by_user IS NULL OR (length(decided_by_user) = 26 AND decided_by_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    decided_at       TEXT CHECK (decided_at IS NULL OR (length(decided_at) = 30 AND decided_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    reason           TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    created_at       TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at       TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version          INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((decided_at IS NULL) = (decided_by_kind IS NULL)),
    CHECK (state = 'suggested' OR decided_at IS NOT NULL)
) STRICT;

CREATE INDEX payment_matches_transaction ON payment_matches (transaction_id, id);
CREATE INDEX payment_matches_occurrence ON payment_matches (occurrence_id, id);

-- No double assignment: a transaction and an occurrence have at most one open
-- (suggested or confirmed) link. Rejected and reversed links are history.
CREATE UNIQUE INDEX payment_matches_open_pair ON payment_matches (transaction_id, occurrence_id) WHERE state IN ('suggested', 'confirmed');

-- What a link connects, its currency, origin and creation never change; a
-- different allocation target is a new link.
CREATE TRIGGER payment_matches_identity_immutable
BEFORE UPDATE OF id, transaction_id, occurrence_id, currency, origin, created_by_kind, created_by_user, created_at ON payment_matches
BEGIN
    SELECT RAISE(ABORT, 'a payment match cannot change what it links, its currency or its origin');
END;

-- The allocation is in the transaction's and the occurrence's currency, and
-- only an outgoing (debit) transaction pays a commitment.
CREATE TRIGGER payment_matches_currency_insert
BEFORE INSERT ON payment_matches
WHEN NEW.currency IS NOT (SELECT source_currency FROM bank_transactions WHERE id = NEW.transaction_id)
  OR NEW.currency IS NOT (SELECT currency FROM commitment_occurrences WHERE id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'a payment match must be in the currency of its transaction and its occurrence');
END;

CREATE TRIGGER payment_matches_debit_only
BEFORE INSERT ON payment_matches
WHEN (SELECT source_amount FROM bank_transactions WHERE id = NEW.transaction_id) NOT GLOB '-*'
BEGIN
    SELECT RAISE(ABORT, 'only an outgoing bank transaction can pay an occurrence');
END;

-- A new link is a suggestion or a confirmation. A model suggestion is only
-- ever a suggestion (decision D9); a user's own match is a confirmation.
CREATE TRIGGER payment_matches_insert_state
BEFORE INSERT ON payment_matches
WHEN NEW.state NOT IN ('suggested', 'confirmed')
  OR (NEW.origin = 'model_suggestion' AND NEW.state <> 'suggested')
  OR (NEW.origin = 'user' AND NEW.state <> 'confirmed')
BEGIN
    SELECT RAISE(ABORT, 'a payment match starts as a suggestion or a confirmation its origin allows');
END;

-- Only a booked, still reported, not superseded transaction can confirm a
-- payment: a pending one only ever produces suggestions (decision D8).
CREATE TRIGGER payment_matches_confirm_booked_insert
BEFORE INSERT ON payment_matches
WHEN NEW.state = 'confirmed' AND NOT EXISTS (SELECT 1 FROM bank_transactions t WHERE t.id = NEW.transaction_id
    AND t.booking_status = 'booked' AND t.no_longer_reported_at IS NULL AND t.superseded_by IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'only a booked, reported bank transaction can confirm a payment');
END;

CREATE TRIGGER payment_matches_confirm_booked_update
BEFORE UPDATE OF state ON payment_matches
WHEN NEW.state = 'confirmed' AND OLD.state <> 'confirmed' AND NOT EXISTS (SELECT 1 FROM bank_transactions t WHERE t.id = NEW.transaction_id
    AND t.booking_status = 'booked' AND t.no_longer_reported_at IS NULL AND t.superseded_by IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'only a booked, reported bank transaction can confirm a payment');
END;

-- A suggestion can outlive a currency change of its unpaid occurrence (C2
-- rewrites unpaid occurrences); it can then no longer be confirmed.
CREATE TRIGGER payment_matches_currency_confirm
BEFORE UPDATE OF state ON payment_matches
WHEN NEW.state = 'confirmed' AND NEW.currency IS NOT (SELECT currency FROM commitment_occurrences WHERE id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'a payment match must be in the currency of its transaction and its occurrence');
END;

-- State changes: suggested -> confirmed or rejected; confirmed -> rejected
-- (unmatched) or reversed. Rejected and reversed are final.
CREATE TRIGGER payment_matches_transitions
BEFORE UPDATE ON payment_matches
WHEN OLD.state IN ('rejected', 'reversed')
  OR NOT (NEW.state = OLD.state
       OR (OLD.state = 'suggested' AND NEW.state IN ('confirmed', 'rejected'))
       OR (OLD.state = 'confirmed' AND NEW.state IN ('rejected', 'reversed')))
BEGIN
    SELECT RAISE(ABORT, 'payment match state change not allowed');
END;

-- A confirmed or reversed link is payment history: it is never deleted. Its
-- occurrence is kept as well (ON DELETE RESTRICT above, and C2 treats such an
-- occurrence as paid). Suggestions and rejections of an occurrence that is
-- regenerated away go with it: they are about an occurrence that no longer
-- exists, and the audit log keeps the decisions.
CREATE TRIGGER payment_matches_history_kept
BEFORE DELETE ON payment_matches
WHEN OLD.state IN ('confirmed', 'reversed')
BEGIN
    SELECT RAISE(ABORT, 'a confirmed or reversed payment match is never deleted');
END;

CREATE TRIGGER commitment_occurrences_drop_open_suggestions
BEFORE DELETE ON commitment_occurrences
BEGIN
    DELETE FROM payment_matches WHERE occurrence_id = OLD.id AND state IN ('suggested', 'rejected');
END;

-- payment_match_events is the append-only history of each match: every
-- creation, decision and correction with the allocation after it, who made it
-- and the reason given. It is what the API shows as a match's history; the
-- audit log records the same changes without amounts or reasons (ADR-0025).
CREATE TABLE payment_match_events (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    match_id    TEXT NOT NULL REFERENCES payment_matches (id) ON DELETE CASCADE,
    action      TEXT NOT NULL CHECK (action IN ('suggested', 'confirmed', 'rejected', 'unmatched', 'corrected', 'reversed')),
    state       TEXT NOT NULL CHECK (state IN ('suggested', 'confirmed', 'rejected', 'reversed')),
    amount      TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency    TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    actor_user  TEXT CHECK (actor_user IS NULL OR (length(actor_user) = 26 AND actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    reason      TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    at          TEXT NOT NULL CHECK (length(at) = 30 AND at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE INDEX payment_match_events_match ON payment_match_events (match_id, at, id);

-- A history entry is in its match's currency.
CREATE TRIGGER payment_match_events_currency
BEFORE INSERT ON payment_match_events
WHEN NEW.currency IS NOT (SELECT currency FROM payment_matches WHERE id = NEW.match_id)
BEGIN
    SELECT RAISE(ABORT, 'a payment match history entry must be in the currency of its match');
END;

CREATE TRIGGER payment_match_events_append_only
BEFORE UPDATE ON payment_match_events
BEGIN
    SELECT RAISE(ABORT, 'payment match history is append-only');
END;
