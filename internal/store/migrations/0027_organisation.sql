-- Organisation: categories, labels, buckets and their assignments (O1,
-- ADR-0052, decision D12).
--
-- Definitions are what the person (or Terraform) manages; assignments attach a
-- definition to an object. Both are user-side data kept apart from bank facts:
-- a bank refresh updates bank_transactions in place and never touches any
-- table here, so an assignment survives every re-import and booking.
--
-- Categories are single-valued per object with a two-level hierarchy: a
-- category has at most one parent and a parent has no parent (the triggers
-- below enforce it as a backstop to the domain check). Labels are many-valued
-- and flat. Buckets group commitments and accounts to slice the outlook; they
-- never hold or allocate money. name_key is the case-folded, white-space
-- collapsed name the domain computes: names are unique (among siblings for
-- categories) whatever their case, including archived ones, so renaming an
-- archived definition is how its name is freed.
--
-- archived_at keeps a definition and every assignment to it (history), but
-- nothing new may be assigned. Deleting a definition is refused while it is
-- assigned unless the caller names a replacement; the ON DELETE RESTRICT
-- references make that a database guarantee.
--
-- An assignment says where it came from: origin user (the person), rule or
-- model (automatic). A user assignment is never replaced by an automatic one,
-- and a person's "none" (a category_assignments row without a category) or a
-- removed label (removed = 1) is remembered the same way, so automatic runs
-- cannot bring back what the person took away. source names the rule or
-- provider/model for the automatic origins.
--
-- An assignment refers to its object by object_type and object_id. The typed
-- columns carry the foreign keys: bank transactions and accounts are RESTRICT
-- (like payment matches and merchant decisions, a purge of bank facts is
-- blocked by what the person decided about them), commitments are CASCADE (a
-- deleted commitment takes its assignments). A merchant has no table of its
-- own yet (ADR-0051 keys identity by transaction), so its object_id is the
-- merchant key (the case-folded name) with no foreign key.

CREATE TABLE categories (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 320),
    name_key    TEXT NOT NULL CHECK (name_key <> '' AND length(name_key) <= 320),
    parent_id   TEXT REFERENCES categories (id) ON DELETE RESTRICT,
    archived_at TEXT CHECK (archived_at IS NULL OR (length(archived_at) = 30 AND archived_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version     INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (parent_id IS NULL OR parent_id <> id)
) STRICT;

CREATE UNIQUE INDEX categories_sibling_name ON categories (coalesce(parent_id, ''), name_key);
CREATE INDEX categories_parent ON categories (parent_id, id);

CREATE TRIGGER categories_two_levels_insert
BEFORE INSERT ON categories
WHEN NEW.parent_id IS NOT NULL AND (SELECT parent_id FROM categories WHERE id = NEW.parent_id) IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'categories have two levels: a parent cannot have a parent');
END;

CREATE TRIGGER categories_two_levels_update
BEFORE UPDATE OF parent_id ON categories
WHEN NEW.parent_id IS NOT NULL AND (
    (SELECT parent_id FROM categories WHERE id = NEW.parent_id) IS NOT NULL
    OR EXISTS (SELECT 1 FROM categories WHERE parent_id = NEW.id))
BEGIN
    SELECT RAISE(ABORT, 'categories have two levels: a parent cannot have a parent');
END;

CREATE TABLE labels (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 320),
    name_key    TEXT NOT NULL CHECK (name_key <> '' AND length(name_key) <= 320),
    archived_at TEXT CHECK (archived_at IS NULL OR (length(archived_at) = 30 AND archived_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version     INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1)
) STRICT;

CREATE UNIQUE INDEX labels_name ON labels (name_key);

CREATE TABLE buckets (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 320),
    name_key    TEXT NOT NULL CHECK (name_key <> '' AND length(name_key) <= 320),
    archived_at TEXT CHECK (archived_at IS NULL OR (length(archived_at) = 30 AND archived_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version     INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1)
) STRICT;

CREATE UNIQUE INDEX buckets_name ON buckets (name_key);

CREATE TABLE category_assignments (
    object_type    TEXT NOT NULL CHECK (object_type IN ('transaction', 'commitment', 'account', 'merchant')),
    object_id      TEXT NOT NULL CHECK (length(object_id) BETWEEN 1 AND 200),
    transaction_id TEXT REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    commitment_id  TEXT REFERENCES commitments (id) ON DELETE CASCADE,
    account_id     TEXT REFERENCES bank_accounts (id) ON DELETE RESTRICT,
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
       AND object_id = coalesce(transaction_id, commitment_id, account_id, object_id)),
    CHECK (category_id IS NOT NULL OR origin = 'user'),
    CHECK (origin <> 'user' OR source = ''),
    CHECK (origin = 'user' OR source <> '')
) STRICT;

CREATE INDEX category_assignments_category ON category_assignments (category_id);

CREATE TABLE label_assignments (
    object_type    TEXT NOT NULL CHECK (object_type IN ('transaction', 'commitment', 'account', 'merchant')),
    object_id      TEXT NOT NULL CHECK (length(object_id) BETWEEN 1 AND 200),
    transaction_id TEXT REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    commitment_id  TEXT REFERENCES commitments (id) ON DELETE CASCADE,
    account_id     TEXT REFERENCES bank_accounts (id) ON DELETE RESTRICT,
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
       AND object_id = coalesce(transaction_id, commitment_id, account_id, object_id)),
    CHECK (removed = 0 OR origin = 'user'),
    CHECK (origin <> 'user' OR source = ''),
    CHECK (origin = 'user' OR source <> '')
) STRICT;

CREATE INDEX label_assignments_label ON label_assignments (label_id);

CREATE TABLE bucket_members (
    bucket_id      TEXT NOT NULL REFERENCES buckets (id) ON DELETE RESTRICT,
    object_type    TEXT NOT NULL CHECK (object_type IN ('commitment', 'account')),
    object_id      TEXT NOT NULL CHECK (length(object_id) BETWEEN 1 AND 200),
    commitment_id  TEXT REFERENCES commitments (id) ON DELETE CASCADE,
    account_id     TEXT REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    added_by_kind  TEXT NOT NULL CHECK (added_by_kind IN ('user', 'token', 'system')),
    added_by_user  TEXT CHECK (added_by_user IS NULL OR length(added_by_user) = 26),
    added_by_via   TEXT NOT NULL CHECK (added_by_via IN ('session', 'token', 'mcp', 'system')),
    added_at TEXT NOT NULL CHECK (length(added_at) = 30 AND added_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    PRIMARY KEY (bucket_id, object_type, object_id),
    CHECK ((object_type = 'commitment') = (commitment_id IS NOT NULL)
       AND (object_type = 'account') = (account_id IS NOT NULL)
       AND object_id = coalesce(commitment_id, account_id))
) STRICT;
