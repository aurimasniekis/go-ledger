-- Recurring commitment definitions (C1, ADR-0024). One row per commitment the
-- user records. These are user-entered definitions only: dated occurrences,
-- payment links and payment state are derived from them by later migrations
-- (tasks C2, C3) and live in their own tables, and bank facts (ADR-0013) are
-- never written here.
-- amount is the exact positive amount due per occurrence in currency
-- (ADR-0005), stored as decimal text; it is never summed or compared in SQL.
-- schedule is the versioned schedule spec as canonical JSON written by
-- internal/domain/commitments (for example
-- {"version":1,"frequency":"monthly","interval":1,"day":15}); code validates
-- the members per version. kind and priority follow
-- internal/domain/commitments; adding a value is a table rebuild.
CREATE TABLE commitments (
    id         TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind       TEXT NOT NULL CHECK (kind IN ('service', 'server', 'domain', 'subscription', 'debt_repayment')),
    amount     TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency   TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    schedule   TEXT NOT NULL CHECK (json_valid(schedule) AND json_type(schedule) = 'object' AND coalesce(json_type(schedule, '$.version'), '') = 'integer' AND json_extract(schedule, '$.version') >= 1),
    start_date TEXT NOT NULL CHECK (length(start_date) = 10 AND start_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(start_date) = start_date),
    end_date   TEXT CHECK (end_date IS NULL OR (length(end_date) = 10 AND end_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(end_date) = end_date)),
    priority   TEXT CHECK (priority IS NULL OR priority IN ('essential', 'important', 'flexible')),
    notes      TEXT CHECK (notes IS NULL OR length(notes) BETWEEN 1 AND 2000),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version    INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (end_date IS NULL OR end_date >= start_date)
) STRICT;

CREATE INDEX commitments_kind ON commitments (kind, id);
