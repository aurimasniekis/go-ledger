-- Payment state (C3, ADR-0031). Payment status is never stored: it is derived
-- from payment evidence and dates when read (decision D7 option a). This
-- migration adds the user-entered inputs of that derivation:
--
-- manual_payments holds payments the user recorded by hand. They are user
-- data, kept apart from bank facts (ADR-0013) and from match links (task X1).
-- A record pays one occurrence; amount is exact positive decimal text in
-- currency (ADR-0005), never summed in SQL. Code refuses an amount in another
-- currency than the occurrence's (amounts are never converted). Records are
-- not edited: a mistake is deleted and recorded again, so version stays 1
-- and serves the delete's If-Match. A record is payment evidence: it refers
-- to its occurrence with ON DELETE RESTRICT, so a paid occurrence (and with
-- it its commitment) cannot be deleted, and occurrence regeneration keeps it
-- (ADR-0027 DefaultEvidence).
CREATE TABLE manual_payments (
    id            TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    occurrence_id TEXT NOT NULL REFERENCES commitment_occurrences (id) ON DELETE RESTRICT CHECK (length(occurrence_id) = 26 AND occurrence_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    amount        TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency      TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    paid_on       TEXT NOT NULL CHECK (length(paid_on) = 10 AND paid_on GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(paid_on) = paid_on),
    note          TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 2000),
    created_at    TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at    TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version       INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1)
) STRICT;

CREATE INDEX manual_payments_occurrence ON manual_payments (occurrence_id, id);

-- occurrence_status_overrides is the history of manual status overrides,
-- each with the user's reason. At most one row per occurrence is active; it
-- overrides the derived status ("paid" only while evidence exists). Setting
-- an override supersedes the active one and remembers it in previous_id;
-- undoing the active one marks it undone and makes previous_id active again,
-- so an undo restores exactly the state before the change. Rows are never
-- deleted except with their occurrence (CASCADE): an override is not
-- payment evidence, so it does not keep an unpaid occurrence from being
-- regenerated or deleted. Concurrency is the occurrence's version, which
-- every change increases, so this table has no version of its own.
CREATE TABLE occurrence_status_overrides (
    id            TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    occurrence_id TEXT NOT NULL REFERENCES commitment_occurrences (id) ON DELETE CASCADE CHECK (length(occurrence_id) = 26 AND occurrence_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    status        TEXT NOT NULL CHECK (status IN ('paid', 'unpaid')),
    reason        TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
    previous_id   TEXT REFERENCES occurrence_status_overrides (id) ON DELETE CASCADE CHECK (previous_id IS NULL OR (length(previous_id) = 26 AND previous_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    state         TEXT NOT NULL CHECK (state IN ('active', 'superseded', 'undone')),
    set_at        TEXT NOT NULL CHECK (length(set_at) = 30 AND set_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    ended_at      TEXT CHECK (ended_at IS NULL OR (length(ended_at) = 30 AND ended_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK ((state = 'active') = (ended_at IS NULL)),
    CHECK (previous_id IS NULL OR previous_id <> id)
) STRICT;

CREATE UNIQUE INDEX occurrence_status_overrides_active ON occurrence_status_overrides (occurrence_id) WHERE state = 'active';
CREATE INDEX occurrence_status_overrides_occurrence ON occurrence_status_overrides (occurrence_id, set_at);
