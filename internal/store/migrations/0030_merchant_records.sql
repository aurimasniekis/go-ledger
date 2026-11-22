-- ledger:foreign-keys-off
-- Merchant records (task M1, ADR-0057, operator decision D-MER 1a).
--
-- A merchant gets a record of its own: a stable id, the name to show, and the
-- alias names a transaction's merchant name is matched by. The person's
-- decisions (merchant_reviews, ADR-0051) and organisation assignments to a
-- merchant (ADR-0053, which named a merchant by a case-folded name key until
-- now) refer to the record by id with a foreign key, so renaming a merchant or
-- adding a spelling keeps every decision and assignment.
--
-- merchant_aliases.alias_key is the name as matched: Unicode NFC, white space
-- collapsed, lower case (merchants.AliasKey in Go; the SQL function
-- ledger_merchant_key the store registers calls the same code, so the keys
-- the backfill below writes are the keys the store looks up). A key names one
-- merchant at most. A merged merchant keeps its row (archived, merged_into_id
-- set) so its id still resolves, but holds no aliases, decisions or
-- assignments: they moved to the remaining merchant.
--
-- An enrichment answer keeps its own provenance (source, confidence); its
-- merchant_id is the record its identified name resolves to through an alias,
-- or NULL. Enrichment is derived data, so deleting a merchant clears the
-- reference (SET NULL) instead of being blocked by it. recheck marks an answer
-- the next enrichment pass looks up again, because a decision about another
-- transaction of the same merchant changed.
--
-- Backfill, deterministic and lossless:
-- 1. Every alias key comes from the names of confirmed or corrected decisions
--    and from the merchant keys of organisation assignments.
-- 2. One merchant per distinct key, ordered by key. Its id is the time-zero
--    identifier '0000000000M' followed by the ordinal in 15 digits (no digest
--    of a name, which could be reversed). Its display name is the name of the
--    most recent decision with that key (ties: lower transaction id), else
--    the assignment key itself; created_at and updated_at are the earliest
--    and latest decision or assignment time. Its one alias has that name, the
--    origin 'upgrade' and the id '0000000000A' with the same ordinal.
-- 3. Decisions (other than rejections) point at the merchant of their name's
--    key, identified enrichment answers at the merchant of theirs if any, and
--    merchant assignments move from the name key to the merchant id. Decision
--    names are kept as written.
-- 4. The upgrade is refused, and nothing changes, when two assignment keys of
--    the earlier version become one key (they differed only in Unicode
--    composition) for the same category slot or label, or a key is not valid
--    display text: the error names how many. Remove one of the duplicates
--    with the earlier version, then upgrade.

CREATE TABLE merchants (
    id             TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    display_name   TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    merged_into_id TEXT REFERENCES merchants (id) ON DELETE RESTRICT,
    archived_at    TEXT CHECK (archived_at IS NULL OR (length(archived_at) = 30 AND archived_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    created_at     TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at     TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version        INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (merged_into_id IS NULL OR (merged_into_id <> id AND archived_at IS NOT NULL))
) STRICT;

CREATE INDEX merchants_merged_into ON merchants (merged_into_id);

CREATE TABLE merchant_aliases (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    merchant_id TEXT NOT NULL REFERENCES merchants (id) ON DELETE RESTRICT,
    alias_key   TEXT NOT NULL UNIQUE CHECK (alias_key <> '' AND length(alias_key) <= 200),
    name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    origin      TEXT NOT NULL CHECK (origin IN ('user', 'decision', 'upgrade')),
    created_at  TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE INDEX merchant_aliases_merchant ON merchant_aliases (merchant_id, alias_key);

-- 1. The names and keys.
CREATE TEMP TABLE m30_names (
    alias_key     TEXT NOT NULL,
    name          TEXT NOT NULL,
    from_decision INTEGER NOT NULL,
    at            TEXT NOT NULL,
    tiebreak      TEXT NOT NULL
);

INSERT INTO m30_names (alias_key, name, from_decision, at, tiebreak)
SELECT ledger_merchant_key(name), name, 1, decided_at, transaction_id FROM merchant_reviews WHERE decision IN ('confirmed', 'corrected');
INSERT INTO m30_names (alias_key, name, from_decision, at, tiebreak)
SELECT ledger_merchant_key(object_id), object_id, 0, set_at, object_id FROM category_assignments WHERE object_type = 'merchant';
INSERT INTO m30_names (alias_key, name, from_decision, at, tiebreak)
SELECT ledger_merchant_key(object_id), object_id, 0, set_at, object_id FROM label_assignments WHERE object_type = 'merchant';

-- 4. Refusals, before anything moves.
SELECT ledger_refuse('migration 0030 refused: ' || count(*) || ' merchant names or assignment keys are not valid display text; correct them with the earlier version first')
FROM m30_names WHERE alias_key = '' HAVING count(*) > 0;
SELECT ledger_refuse('migration 0030 refused: ' || count(*) || ' merchant category assignments of the earlier version name the same merchant (keys differing only in Unicode composition); remove the duplicates with the earlier version first')
FROM (SELECT 1 FROM category_assignments WHERE object_type = 'merchant' GROUP BY ledger_merchant_key(object_id) HAVING count(*) > 1) HAVING count(*) > 0;
SELECT ledger_refuse('migration 0030 refused: ' || count(*) || ' merchant label assignments of the earlier version name the same merchant and label (keys differing only in Unicode composition); remove the duplicates with the earlier version first')
FROM (SELECT 1 FROM label_assignments WHERE object_type = 'merchant' GROUP BY ledger_merchant_key(object_id), label_id HAVING count(*) > 1) HAVING count(*) > 0;

-- 2. One merchant and one alias per key.
CREATE TEMP TABLE m30_merchants AS
SELECT '0000000000M' || printf('%015d', row_number() OVER (ORDER BY k.alias_key)) AS id,
       k.alias_key AS alias_key,
       coalesce((SELECT n.name FROM m30_names n WHERE n.alias_key = k.alias_key AND n.from_decision = 1 ORDER BY n.at DESC, n.tiebreak LIMIT 1), k.alias_key) AS display_name,
       (SELECT min(n.at) FROM m30_names n WHERE n.alias_key = k.alias_key) AS created_at,
       (SELECT max(n.at) FROM m30_names n WHERE n.alias_key = k.alias_key) AS updated_at
FROM (SELECT DISTINCT alias_key FROM m30_names) k;

INSERT INTO merchants (id, display_name, created_at, updated_at)
SELECT id, display_name, created_at, updated_at FROM m30_merchants ORDER BY id;
INSERT INTO merchant_aliases (id, merchant_id, alias_key, name, origin, created_at)
SELECT '0000000000A' || substr(id, 12), id, alias_key, display_name, 'upgrade', created_at FROM m30_merchants ORDER BY id;

-- 3. Decisions, answers and assignments refer to the merchant.
CREATE TABLE merchant_reviews_v30 (
    transaction_id  TEXT PRIMARY KEY REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    decision        TEXT NOT NULL CHECK (decision IN ('confirmed', 'corrected', 'rejected')),
    name            TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    decided_kind    TEXT NOT NULL CHECK (decided_kind IN ('user', 'token', 'system')),
    decided_user    TEXT CHECK (decided_user IS NULL OR length(decided_user) = 26),
    decided_via     TEXT NOT NULL CHECK (decided_via IN ('session', 'token', 'mcp', 'system')),
    decided_at      TEXT NOT NULL CHECK (length(decided_at) = 30 AND decided_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    merchant_id     TEXT REFERENCES merchants (id) ON DELETE RESTRICT,
    CHECK ((decision = 'rejected') = (name = '')),
    CHECK ((decision = 'rejected') = (merchant_id IS NULL))
) STRICT;

INSERT INTO merchant_reviews_v30 (transaction_id, decision, name, decided_kind, decided_user, decided_via, decided_at, version, merchant_id)
SELECT r.transaction_id, r.decision, r.name, r.decided_kind, r.decided_user, r.decided_via, r.decided_at, r.version,
       CASE WHEN r.decision = 'rejected' THEN NULL ELSE (SELECT m.id FROM m30_merchants m WHERE m.alias_key = ledger_merchant_key(r.name)) END
FROM merchant_reviews r;
DROP TABLE merchant_reviews;
ALTER TABLE merchant_reviews_v30 RENAME TO merchant_reviews;
CREATE INDEX merchant_reviews_merchant ON merchant_reviews (merchant_id);

CREATE TABLE merchant_enrichments_v30 (
    transaction_id TEXT PRIMARY KEY REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    status         TEXT NOT NULL CHECK (status IN ('identified', 'ambiguous', 'none', 'unavailable')),
    name           TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    logo_ref       TEXT NOT NULL DEFAULT '' CHECK (length(logo_ref) <= 500),
    source         TEXT NOT NULL CHECK (source <> '' AND length(source) <= 64 AND source NOT GLOB '*[^a-z0-9_.-]*'),
    confidence     INTEGER CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1000),
    candidates     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(candidates) AND json_type(candidates) = 'array'),
    retrieved_at   TEXT CHECK (retrieved_at IS NULL OR (length(retrieved_at) = 30 AND retrieved_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_failed_at TEXT CHECK (last_failed_at IS NULL OR (length(last_failed_at) = 30 AND last_failed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    merchant_id    TEXT REFERENCES merchants (id) ON DELETE SET NULL,
    recheck        INTEGER NOT NULL DEFAULT 0 CHECK (recheck IN (0, 1)),
    CHECK (status <> 'identified' OR (name <> '' AND confidence IS NOT NULL AND retrieved_at IS NOT NULL AND candidates = '[]')),
    CHECK (status <> 'ambiguous' OR (name = '' AND logo_ref = '' AND retrieved_at IS NOT NULL AND json_array_length(candidates) >= 2)),
    CHECK (status <> 'none' OR (name = '' AND logo_ref = '' AND retrieved_at IS NOT NULL AND candidates = '[]')),
    CHECK (status <> 'unavailable' OR (name = '' AND logo_ref = '' AND confidence IS NULL AND retrieved_at IS NULL AND last_failed_at IS NOT NULL AND candidates = '[]')),
    CHECK (status = 'identified' OR confidence IS NULL),
    CHECK (status = 'identified' OR merchant_id IS NULL)
) STRICT;

INSERT INTO merchant_enrichments_v30 (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at, merchant_id)
SELECT e.transaction_id, e.status, e.name, e.logo_ref, e.source, e.confidence, e.candidates, e.retrieved_at, e.last_failed_at,
       CASE WHEN e.status = 'identified' THEN (SELECT m.id FROM m30_merchants m WHERE m.alias_key = ledger_merchant_key(e.name)) END
FROM merchant_enrichments e;
DROP TABLE merchant_enrichments;
ALTER TABLE merchant_enrichments_v30 RENAME TO merchant_enrichments;
CREATE INDEX merchant_enrichments_merchant ON merchant_enrichments (merchant_id);

CREATE TABLE category_assignments_v30 (
    object_type    TEXT NOT NULL CHECK (object_type IN ('transaction', 'commitment', 'account', 'merchant')),
    object_id      TEXT NOT NULL CHECK (length(object_id) BETWEEN 1 AND 200),
    transaction_id TEXT REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    commitment_id  TEXT REFERENCES commitments (id) ON DELETE CASCADE,
    account_id     TEXT REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    merchant_id    TEXT REFERENCES merchants (id) ON DELETE RESTRICT,
    category_id    TEXT REFERENCES categories (id) ON DELETE RESTRICT,
    origin         TEXT NOT NULL CHECK (origin IN ('user', 'rule', 'model')),
    source         TEXT NOT NULL DEFAULT '' CHECK (length(source) <= 200),
    set_by_kind  TEXT NOT NULL CHECK (set_by_kind IN ('user', 'token', 'system')),
    set_by_user  TEXT CHECK (set_by_user IS NULL OR length(set_by_user) = 26),
    set_by_via   TEXT NOT NULL CHECK (set_by_via IN ('session', 'token', 'mcp', 'system')),
    set_at TEXT NOT NULL CHECK (length(set_at) = 30 AND set_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    PRIMARY KEY (object_type, object_id),
    CHECK ((object_type = 'transaction') = (transaction_id IS NOT NULL)
       AND (object_type = 'commitment') = (commitment_id IS NOT NULL)
       AND (object_type = 'account') = (account_id IS NOT NULL)
       AND (object_type = 'merchant') = (merchant_id IS NOT NULL)
       AND object_id = coalesce(transaction_id, commitment_id, account_id, merchant_id)),
    CHECK (category_id IS NOT NULL OR origin = 'user'),
    CHECK (origin <> 'user' OR source = ''),
    CHECK (origin = 'user' OR source <> '')
) STRICT;

INSERT INTO category_assignments_v30 (object_type, object_id, transaction_id, commitment_id, account_id, merchant_id, category_id, origin, source, set_by_kind, set_by_user, set_by_via, set_at)
SELECT a.object_type, coalesce(m.id, a.object_id), a.transaction_id, a.commitment_id, a.account_id, m.id, a.category_id, a.origin, a.source, a.set_by_kind, a.set_by_user, a.set_by_via, a.set_at
FROM category_assignments a LEFT JOIN m30_merchants m ON a.object_type = 'merchant' AND m.alias_key = ledger_merchant_key(a.object_id);
DROP TABLE category_assignments;
ALTER TABLE category_assignments_v30 RENAME TO category_assignments;
CREATE INDEX category_assignments_category ON category_assignments (category_id);
CREATE INDEX category_assignments_merchant ON category_assignments (merchant_id);

CREATE TABLE label_assignments_v30 (
    object_type    TEXT NOT NULL CHECK (object_type IN ('transaction', 'commitment', 'account', 'merchant')),
    object_id      TEXT NOT NULL CHECK (length(object_id) BETWEEN 1 AND 200),
    transaction_id TEXT REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    commitment_id  TEXT REFERENCES commitments (id) ON DELETE CASCADE,
    account_id     TEXT REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    merchant_id    TEXT REFERENCES merchants (id) ON DELETE RESTRICT,
    label_id       TEXT NOT NULL REFERENCES labels (id) ON DELETE RESTRICT,
    removed        INTEGER NOT NULL DEFAULT 0 CHECK (removed IN (0, 1)),
    origin         TEXT NOT NULL CHECK (origin IN ('user', 'rule', 'model')),
    source         TEXT NOT NULL DEFAULT '' CHECK (length(source) <= 200),
    set_by_kind  TEXT NOT NULL CHECK (set_by_kind IN ('user', 'token', 'system')),
    set_by_user  TEXT CHECK (set_by_user IS NULL OR length(set_by_user) = 26),
    set_by_via   TEXT NOT NULL CHECK (set_by_via IN ('session', 'token', 'mcp', 'system')),
    set_at TEXT NOT NULL CHECK (length(set_at) = 30 AND set_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    PRIMARY KEY (object_type, object_id, label_id),
    CHECK ((object_type = 'transaction') = (transaction_id IS NOT NULL)
       AND (object_type = 'commitment') = (commitment_id IS NOT NULL)
       AND (object_type = 'account') = (account_id IS NOT NULL)
       AND (object_type = 'merchant') = (merchant_id IS NOT NULL)
       AND object_id = coalesce(transaction_id, commitment_id, account_id, merchant_id)),
    CHECK (removed = 0 OR origin = 'user'),
    CHECK (origin <> 'user' OR source = ''),
    CHECK (origin = 'user' OR source <> '')
) STRICT;

INSERT INTO label_assignments_v30 (object_type, object_id, transaction_id, commitment_id, account_id, merchant_id, label_id, removed, origin, source, set_by_kind, set_by_user, set_by_via, set_at)
SELECT a.object_type, coalesce(m.id, a.object_id), a.transaction_id, a.commitment_id, a.account_id, m.id, a.label_id, a.removed, a.origin, a.source, a.set_by_kind, a.set_by_user, a.set_by_via, a.set_at
FROM label_assignments a LEFT JOIN m30_merchants m ON a.object_type = 'merchant' AND m.alias_key = ledger_merchant_key(a.object_id);
DROP TABLE label_assignments;
ALTER TABLE label_assignments_v30 RENAME TO label_assignments;
CREATE INDEX label_assignments_label ON label_assignments (label_id);
CREATE INDEX label_assignments_merchant ON label_assignments (merchant_id);

DROP TABLE m30_merchants;
DROP TABLE m30_names;
