-- Merchant identity of bank transactions (E1, ADR-0051).
--
-- Two tables keep apart what a lookup source said about a transaction and what
-- the person decided about it, and neither touches the bank fact:
--
-- merchant_enrichments is the source's answer: the identified name and logo
-- reference, the source, its confidence (permille, an exact integer) and when
-- it was retrieved. An ambiguous answer holds candidate names and no name; no
-- match holds neither. A lookup that failed (status unavailable) never
-- replaces an earlier answer: it only stamps last_failed_at on it. Enrichment
-- is derived, provider-side data that can be fetched again, so the purge of a
-- connection's bank facts deletes it first (bankfacts.Store.PurgeConnection)
-- instead of being blocked by it.
--
-- merchant_reviews is the person's decision: confirmed (this enrichment name is
-- right), corrected (the name is this) or rejected (no identity). It is user
-- data: it refers to the transaction with ON DELETE RESTRICT, like payment
-- matches, so bank facts cannot be purged from under it. It lives apart from
-- the bank transaction row, which re-imports update in place, so a decision
-- survives every re-import.
--
-- Nothing here refers to amounts, occurrences or payment state: merchant
-- identity never changes a figure or a status.
CREATE TABLE merchant_enrichments (
    transaction_id TEXT PRIMARY KEY REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    status         TEXT NOT NULL CHECK (status IN ('identified', 'ambiguous', 'none', 'unavailable')),
    name           TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    logo_ref       TEXT NOT NULL DEFAULT '' CHECK (length(logo_ref) <= 500),
    source         TEXT NOT NULL CHECK (source <> '' AND length(source) <= 64 AND source NOT GLOB '*[^a-z0-9_.-]*'),
    confidence     INTEGER CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1000),
    candidates     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(candidates) AND json_type(candidates) = 'array'),
    retrieved_at   TEXT CHECK (retrieved_at IS NULL OR (length(retrieved_at) = 30 AND retrieved_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_failed_at TEXT CHECK (last_failed_at IS NULL OR (length(last_failed_at) = 30 AND last_failed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK (status <> 'identified' OR (name <> '' AND confidence IS NOT NULL AND retrieved_at IS NOT NULL AND candidates = '[]')),
    CHECK (status <> 'ambiguous' OR (name = '' AND logo_ref = '' AND retrieved_at IS NOT NULL AND json_array_length(candidates) >= 2)),
    CHECK (status <> 'none' OR (name = '' AND logo_ref = '' AND retrieved_at IS NOT NULL AND candidates = '[]')),
    CHECK (status <> 'unavailable' OR (name = '' AND logo_ref = '' AND confidence IS NULL AND retrieved_at IS NULL AND last_failed_at IS NOT NULL AND candidates = '[]')),
    CHECK (status = 'identified' OR confidence IS NULL)
) STRICT;

CREATE TABLE merchant_reviews (
    transaction_id  TEXT PRIMARY KEY REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    decision        TEXT NOT NULL CHECK (decision IN ('confirmed', 'corrected', 'rejected')),
    name            TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    decided_kind    TEXT NOT NULL CHECK (decided_kind IN ('user', 'token', 'system')),
    decided_user    TEXT CHECK (decided_user IS NULL OR length(decided_user) = 26),
    decided_via     TEXT NOT NULL CHECK (decided_via IN ('session', 'token', 'mcp', 'system')),
    decided_at      TEXT NOT NULL CHECK (length(decided_at) = 30 AND decided_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((decision = 'rejected') = (name = ''))
) STRICT;
