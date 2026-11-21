-- Occurrences of recurring commitments (C2, ADR-0027). One row per due date
-- generated from a commitment definition (0009_commitments): the month it
-- belongs to, the calendar date it falls due in the reporting time zone (D5)
-- and the exact amount due. Occurrences are derived from the definition's
-- terms; payment evidence (task C3) refers to them and makes them immutable,
-- and bank facts (ADR-0013) are never written here. There is no status column:
-- status is derived from evidence and dates (D7).
-- A commitment has at most one occurrence per month (every version 1 rule
-- schedules at most one), so (commitment_id, month) identifies the slot an
-- edit rewrites in place. due_date always lies in month.
-- amount is exact decimal text in currency (ADR-0005); never summed in SQL.
-- commitments cannot be deleted while occurrences refer to them (RESTRICT):
-- the commitment store removes unpaid occurrences first, so only payment
-- history blocks a delete.
CREATE TABLE commitment_occurrences (
    id            TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    commitment_id TEXT NOT NULL REFERENCES commitments (id) ON DELETE RESTRICT CHECK (length(commitment_id) = 26 AND commitment_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    month         TEXT NOT NULL CHECK (length(month) = 7 AND month GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]' AND substr(month, 6, 2) BETWEEN '01' AND '12' AND month >= '0001-01'),
    due_date      TEXT NOT NULL CHECK (length(due_date) = 10 AND due_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(due_date) = due_date),
    amount        TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency      TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    created_at    TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at    TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version       INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (substr(due_date, 1, 7) = month),
    UNIQUE (commitment_id, month)
) STRICT;

CREATE INDEX commitment_occurrences_month ON commitment_occurrences (month, due_date, id);

-- How far each commitment's occurrences are materialised: every month up to
-- and including generated_through is stored (or holds nothing); later months
-- follow the definition's current terms when they are generated. No row means
-- nothing is materialised yet. Server-owned bookkeeping, deleted with the
-- commitment.
CREATE TABLE commitment_occurrence_horizons (
    commitment_id     TEXT PRIMARY KEY REFERENCES commitments (id) ON DELETE CASCADE,
    generated_through TEXT NOT NULL CHECK (length(generated_through) = 7 AND generated_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]' AND substr(generated_through, 6, 2) BETWEEN '01' AND '12' AND generated_through >= '0001-01'),
    updated_at        TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;
