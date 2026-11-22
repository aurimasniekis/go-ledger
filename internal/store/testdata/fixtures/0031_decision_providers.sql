-- Fixture: a database at schema version 31 (the version 30 contents plus the
-- decision consents, usage and suggestions of migration 0031), holding every
-- row of the version 30 fixture. Upgrading it must apply migration 0032
-- (decision tasks: reversals and alerts) and leave every existing row as it
-- is. All identifiers and hashes are synthetic.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT NOT NULL CHECK (name <> ''),
    checksum   TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL CHECK (length(applied_at) = 30 AND applied_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE TABLE settings (
    key        TEXT PRIMARY KEY CHECK (key <> ''),
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (1, 'schema_migrations', '25048313703a8dafc7f42d4f2c330ed308d20076538c0b8879f6c0f271bed0e7', '2026-01-02T03:04:05.000000000Z');
INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (2, 'settings', '1c6f00996da58a0eb4c77a4a0997e1016feb5809f3c8bd704de707704b0b5fde', '2026-01-03T03:04:05.000000000Z');
INSERT INTO settings (key, value, updated_at) VALUES ('reporting.timezone', 'Europe/Vilnius', '2026-01-03T03:04:05.000000000Z');

-- Transactional outbox (PU1, ADR-0012). A row is inserted in the same
-- transaction as the state change it announces and is never rewritten: the
-- envelope columns are immutable, only the delivery-status columns change.
-- Terminal rows are purged by age (published 7 days, failed 30 days; D17).
CREATE TABLE outbox (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    id              TEXT NOT NULL UNIQUE CHECK (length(id) = 26),
    subject         TEXT NOT NULL CHECK (length(subject) <= 256 AND subject GLOB 'evt.ledger.*.*'),
    type            TEXT NOT NULL CHECK (type GLOB 'ledger.*.*.v[1-9]*'),
    source          TEXT NOT NULL CHECK (source <> ''),
    occurred_at     TEXT NOT NULL CHECK (length(occurred_at) = 30 AND occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    payload         TEXT NOT NULL CHECK (json_valid(payload) AND json_type(payload) = 'object'),
    state           TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'published', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TEXT NOT NULL CHECK (length(next_attempt_at) = 30 AND next_attempt_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    published_at    TEXT CHECK (published_at IS NULL OR (length(published_at) = 30 AND published_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    failed_at       TEXT CHECK (failed_at IS NULL OR (length(failed_at) = 30 AND failed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK ((state = 'published') = (published_at IS NOT NULL)),
    CHECK ((state = 'failed') = (failed_at IS NOT NULL))
) STRICT;

-- The publisher claims due rows in order.
CREATE INDEX outbox_due ON outbox (next_attempt_at, seq) WHERE state = 'pending';
-- Retention purge scans terminal rows by age.
CREATE INDEX outbox_published_at ON outbox (published_at) WHERE state = 'published';
CREATE INDEX outbox_failed_at ON outbox (failed_at) WHERE state = 'failed';

CREATE TRIGGER outbox_envelope_immutable
BEFORE UPDATE OF seq, id, subject, type, source, occurred_at, payload, created_at ON outbox
BEGIN
    SELECT RAISE(ABORT, 'outbox envelope columns are immutable');
END;

CREATE TRIGGER outbox_terminal_is_final
BEFORE UPDATE ON outbox
WHEN OLD.state <> 'pending'
BEGIN
    SELECT RAISE(ABORT, 'a published or failed outbox row is final');
END;

CREATE TRIGGER outbox_keep_pending
BEFORE DELETE ON outbox
WHEN OLD.state = 'pending'
BEGIN
    SELECT RAISE(ABORT, 'a pending outbox row cannot be deleted');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (3, 'outbox', '9c9ebc162255cd5655e73eed989b07300edb5dc1a5402235b679d02d7f6be7fb', '2026-01-04T03:04:05.000000000Z');
INSERT INTO outbox (id, subject, type, source, occurred_at, payload, state, attempts, next_attempt_at, published_at, created_at) VALUES ('01JZ0000000000000000000001', 'evt.ledger.example.created', 'ledger.example.created.v1', 'ledger', '2026-01-04T04:00:00.000000000Z', '{"n":1}', 'published', 1, '2026-01-04T04:00:00.000000000Z', '2026-01-04T04:00:01.000000000Z', '2026-01-04T04:00:00.000000000Z');
INSERT INTO outbox (id, subject, type, source, occurred_at, payload, state, attempts, next_attempt_at, created_at) VALUES ('01JZ0000000000000000000002', 'evt.ledger.example.updated', 'ledger.example.updated.v1', 'ledger', '2026-01-04T05:00:00.000000000Z', '{"n":2}', 'pending', 0, '2026-01-04T05:00:00.000000000Z', '2026-01-04T05:00:00.000000000Z');

-- Bank facts (B1, ADR-0013): what banking-data providers report, normalized.
-- These tables hold provider-owned facts only. User data (corrections,
-- categories, payment matches, manual payments) lives in other tables that
-- refer to these with ON DELETE RESTRICT, never CASCADE or SET NULL, so a
-- bank fact cannot disappear from under a user decision. Bank facts are
-- deleted only by the explicit purge path (bankfacts.Store.PurgeConnection).
-- Only bank_connections is Ledger-managed (versioned, M1); the other tables
-- are provider-owned facts that nothing edits through the API.
-- Account identifiers, provider handles and session references are
-- sensitive: stored for the owner, never logged.

-- A bank link: one institution through one configured provider application.
-- Mode and account access are recorded at creation (decision D10). Ledger
-- manages these rows, so they carry a version for optimistic concurrency.
CREATE TABLE bank_connections (
    id                  TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    provider            TEXT NOT NULL CHECK (provider <> '' AND provider NOT GLOB '*[^a-z0-9_]*'),
    application         TEXT NOT NULL CHECK (application <> ''),
    mode                TEXT NOT NULL CHECK (mode IN ('app', 'developer')),
    environment         TEXT NOT NULL CHECK (environment IN ('production', 'sandbox')),
    account_access      TEXT NOT NULL CHECK (account_access IN ('all', 'linked_only')),
    institution_id      TEXT NOT NULL CHECK (institution_id <> ''),
    institution_name    TEXT NOT NULL,
    institution_country TEXT NOT NULL CHECK (length(institution_country) = 2 AND institution_country GLOB '[A-Z][A-Z]'),
    status              TEXT NOT NULL CHECK (status IN ('pending', 'active', 'reconnect_required', 'failed', 'disconnected')),
    consent_state       TEXT UNIQUE CHECK (consent_state IS NULL OR consent_state <> ''),
    session_reference   TEXT CHECK (session_reference IS NULL OR session_reference <> ''),
    consent_expires_at  TEXT CHECK (consent_expires_at IS NULL OR (length(consent_expires_at) = 30 AND consent_expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    consent_granted_at  TEXT CHECK (consent_granted_at IS NULL OR (length(consent_granted_at) = 30 AND consent_granted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_error_kind     TEXT NOT NULL DEFAULT '' CHECK (last_error_kind IN ('', 'reconnect_required', 'rate_limited', 'retryable', 'permanent', 'canceled')),
    last_error_code     TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at          TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (status <> 'active' OR (session_reference IS NOT NULL AND consent_expires_at IS NOT NULL))
) STRICT;

-- An account at a provider. Idempotency key: (provider, external_id), where
-- external_id is stable across sessions (Enable Banking: identification
-- hash). provider_handle is the per-session reference used for fetching; it
-- changes on re-authorisation.
CREATE TABLE bank_accounts (
    id                    TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    connection_id         TEXT NOT NULL REFERENCES bank_connections (id) ON DELETE RESTRICT,
    provider              TEXT NOT NULL CHECK (provider <> ''),
    external_id           TEXT NOT NULL CHECK (external_id <> ''),
    provider_handle       TEXT,
    iban                  TEXT,
    bban                  TEXT,
    masked_pan            TEXT,
    other_identifier      TEXT,
    name                  TEXT NOT NULL DEFAULT '',
    product               TEXT NOT NULL DEFAULT '',
    currency              TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    first_seen_at         TEXT NOT NULL CHECK (length(first_seen_at) = 30 AND first_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    last_seen_at          TEXT NOT NULL CHECK (length(last_seen_at) = 30 AND last_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    no_longer_reported_at TEXT CHECK (no_longer_reported_at IS NULL OR (length(no_longer_reported_at) = 30 AND no_longer_reported_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    UNIQUE (provider, external_id)
) STRICT;

-- An account never changes provider: transactions copy it (see below).
CREATE TRIGGER bank_accounts_provider_immutable
BEFORE UPDATE OF provider ON bank_accounts
BEGIN
    SELECT RAISE(ABORT, 'a bank account cannot change provider');
END;

CREATE INDEX bank_accounts_connection ON bank_accounts (connection_id);

-- Balance observations, append-only. obtained_at is when Ledger fetched the
-- figure (freshness); reference_date is the date the bank says it applies
-- to. An unknown balance is no row, never a zero.
CREATE TABLE bank_balance_snapshots (
    id             TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    account_id     TEXT NOT NULL REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    balance_type   TEXT NOT NULL CHECK (balance_type IN ('booked', 'available', 'expected', 'other')),
    provider_type  TEXT NOT NULL CHECK (provider_type <> ''),
    amount         TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*'),
    currency       TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    reference_date TEXT CHECK (reference_date IS NULL OR (length(reference_date) = 10 AND reference_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(reference_date) = reference_date)),
    obtained_at    TEXT NOT NULL CHECK (length(obtained_at) = 30 AND obtained_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE INDEX bank_balance_snapshots_account ON bank_balance_snapshots (account_id, obtained_at);

CREATE TRIGGER bank_balance_snapshots_append_only
BEFORE UPDATE ON bank_balance_snapshots
BEGIN
    SELECT RAISE(ABORT, 'balance snapshots are append-only');
END;

-- Transactions. Idempotency key: (account_id, dedupe_key); see
-- banking.KeyTransactions for how the key is built. source_amount is the
-- amount exactly as the bank reported it, signed (negative = money out), in
-- source_currency; instructed_* is the original foreign amount, if reported.
-- imported_at is the first import and never changes; last_seen_at is the
-- latest fetch that reported the row; no_longer_reported_at is set when a
-- covering fetch stopped returning it (decision D8: never hard-deleted).
CREATE TABLE bank_transactions (
    id                      TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    account_id              TEXT NOT NULL REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    provider                TEXT NOT NULL CHECK (provider <> ''),
    dedupe_key              TEXT NOT NULL CHECK (dedupe_key GLOB 'ref:?*#[1-9]*' OR dedupe_key GLOB 'fb:?*#[1-9]*'),
    key_kind                TEXT NOT NULL CHECK (key_kind IN ('entry_reference', 'fallback')),
    entry_reference         TEXT CHECK (entry_reference IS NULL OR entry_reference <> ''),
    provider_transaction_id TEXT,
    booking_status          TEXT NOT NULL CHECK (booking_status IN ('booked', 'pending', 'cancelled', 'other')),
    provider_status         TEXT NOT NULL CHECK (provider_status <> ''),
    source_amount           TEXT NOT NULL CHECK (length(source_amount) BETWEEN 1 AND 62 AND length(replace(replace(source_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND source_amount NOT GLOB '*[^0-9.-]*' AND source_amount NOT GLOB '0[0-9]*' AND source_amount NOT GLOB '-0[0-9]*' AND replace(replace(source_amount, '0', ''), '.', '') <> '-' AND source_amount NOT GLOB '?*-*' AND source_amount NOT GLOB '*.*.*' AND source_amount NOT GLOB '*.' AND source_amount NOT GLOB '.*' AND source_amount NOT GLOB '-.*' AND source_amount GLOB '*[0-9]*'),
    source_currency         TEXT NOT NULL CHECK (length(source_currency) = 3 AND source_currency GLOB '[A-Z][A-Z][A-Z]'),
    instructed_amount       TEXT CHECK (instructed_amount IS NULL OR (length(instructed_amount) BETWEEN 1 AND 62 AND length(replace(replace(instructed_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND instructed_amount NOT GLOB '*[^0-9.-]*' AND instructed_amount NOT GLOB '0[0-9]*' AND instructed_amount NOT GLOB '-0[0-9]*' AND replace(replace(instructed_amount, '0', ''), '.', '') <> '-' AND instructed_amount NOT GLOB '?*-*' AND instructed_amount NOT GLOB '*.*.*' AND instructed_amount NOT GLOB '*.' AND instructed_amount NOT GLOB '.*' AND instructed_amount NOT GLOB '-.*' AND instructed_amount GLOB '*[0-9]*')),
    instructed_currency     TEXT CHECK (instructed_currency IS NULL OR (length(instructed_currency) = 3 AND instructed_currency GLOB '[A-Z][A-Z][A-Z]')),
    booking_date            TEXT CHECK (booking_date IS NULL OR (length(booking_date) = 10 AND booking_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(booking_date) = booking_date)),
    value_date              TEXT CHECK (value_date IS NULL OR (length(value_date) = 10 AND value_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(value_date) = value_date)),
    transaction_date        TEXT CHECK (transaction_date IS NULL OR (length(transaction_date) = 10 AND transaction_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(transaction_date) = transaction_date)),
    raw_description         TEXT NOT NULL DEFAULT '',
    counterparty_name       TEXT NOT NULL DEFAULT '',
    imported_at             TEXT NOT NULL CHECK (length(imported_at) = 30 AND imported_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    last_seen_at            TEXT NOT NULL CHECK (length(last_seen_at) = 30 AND last_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    no_longer_reported_at   TEXT CHECK (no_longer_reported_at IS NULL OR (length(no_longer_reported_at) = 30 AND no_longer_reported_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    superseded_by           TEXT REFERENCES bank_transactions (id) ON DELETE RESTRICT,
    UNIQUE (account_id, dedupe_key),
    CHECK ((key_kind = 'entry_reference') = (entry_reference IS NOT NULL)),
    CHECK ((key_kind = 'entry_reference') = (dedupe_key GLOB 'ref:*')),
    CHECK ((instructed_amount IS NULL) = (instructed_currency IS NULL)),
    CHECK (coalesce(booking_date, value_date, transaction_date) IS NOT NULL),
    CHECK (superseded_by IS NULL OR superseded_by <> id)
) STRICT;

CREATE INDEX bank_transactions_account_date ON bank_transactions (account_id, coalesce(booking_date, value_date, transaction_date));
CREATE INDEX bank_transactions_entry_reference ON bank_transactions (account_id, entry_reference) WHERE entry_reference IS NOT NULL;

-- Identity and provenance never change after import.
CREATE TRIGGER bank_transactions_identity_immutable
BEFORE UPDATE OF id, account_id, provider, dedupe_key, key_kind, entry_reference, source_amount, source_currency, imported_at ON bank_transactions
BEGIN
    SELECT RAISE(ABORT, 'bank transaction identity, source amount and imported_at are immutable');
END;

-- A booked transaction is a bank fact; a later pending report cannot undo it.
CREATE TRIGGER bank_transactions_booked_is_final
BEFORE UPDATE OF booking_status ON bank_transactions
WHEN OLD.booking_status = 'booked' AND NEW.booking_status <> 'booked'
BEGIN
    SELECT RAISE(ABORT, 'a booked bank transaction cannot change status');
END;

-- A transaction's provider is its account's provider.
CREATE TRIGGER bank_transactions_provider_matches_account
BEFORE INSERT ON bank_transactions
WHEN NEW.provider IS NOT (SELECT provider FROM bank_accounts WHERE id = NEW.account_id)
BEGIN
    SELECT RAISE(ABORT, 'bank transaction provider must match its account');
END;

-- superseded_by links two transactions of the same account.
CREATE TRIGGER bank_transactions_superseded_same_account_insert
BEFORE INSERT ON bank_transactions
WHEN NEW.superseded_by IS NOT NULL AND NEW.account_id IS NOT (SELECT account_id FROM bank_transactions WHERE id = NEW.superseded_by)
BEGIN
    SELECT RAISE(ABORT, 'superseded_by must refer to a transaction of the same account');
END;

CREATE TRIGGER bank_transactions_superseded_same_account_update
BEFORE UPDATE OF superseded_by ON bank_transactions
WHEN NEW.superseded_by IS NOT NULL AND NEW.account_id IS NOT (SELECT account_id FROM bank_transactions WHERE id = NEW.superseded_by)
BEGIN
    SELECT RAISE(ABORT, 'superseded_by must refer to a transaction of the same account');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (4, 'bank_facts', '03b8d666f3a3c02bec5f07e98680a825c59844869e32ca9b1d8cc9e400140e91', '2026-01-05T03:04:05.000000000Z');
INSERT INTO bank_connections (id, provider, application, mode, environment, account_access, institution_id, institution_name, institution_country, status, session_reference, consent_expires_at, consent_granted_at, created_at, updated_at, version) VALUES ('01JZ0000000000000000000010', 'fake', 'fake-app', 'app', 'sandbox', 'all', 'Fake Bank', 'Fake Bank', 'FI', 'active', 'fake-session-1', '2026-07-01T00:00:00.000000000Z', '2026-01-05T03:04:05.000000000Z', '2026-01-05T03:04:05.000000000Z', '2026-01-05T03:04:05.000000000Z', 2);
INSERT INTO bank_accounts (id, connection_id, provider, external_id, provider_handle, iban, name, product, currency, first_seen_at, last_seen_at) VALUES ('01JZ0000000000000000000011', '01JZ0000000000000000000010', 'fake', 'fake-hash-eur-0001', 'fake-session-1-acct-1', 'XX00FAKE0000000000000001', 'Everyday', 'Current account', 'EUR', '2026-01-05T03:04:05.000000000Z', '2026-01-05T03:04:05.000000000Z');
INSERT INTO bank_balance_snapshots (id, account_id, balance_type, provider_type, amount, currency, reference_date, obtained_at) VALUES ('01JZ0000000000000000000012', '01JZ0000000000000000000011', 'booked', 'CLBD', '1250.40', 'EUR', '2026-01-04', '2026-01-05T03:04:05.000000000Z');
INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, entry_reference, booking_status, provider_status, source_amount, source_currency, booking_date, value_date, raw_description, imported_at, last_seen_at) VALUES ('01JZ0000000000000000000013', '01JZ0000000000000000000011', 'fake', 'ref:00000000000000000000000000000001#1', 'entry_reference', 'fake-ref-rent', 'booked', 'BOOK', '-800.00', 'EUR', '2026-01-02', '2026-01-02', 'RENT', '2026-01-05T03:04:05.000000000Z', '2026-01-05T03:04:05.000000000Z');
INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, booking_status, provider_status, source_amount, source_currency, transaction_date, raw_description, imported_at, last_seen_at) VALUES ('01JZ0000000000000000000014', '01JZ0000000000000000000011', 'fake', 'fb:00000000000000000000000000000002#1', 'fallback', 'pending', 'PDNG', '-70.00', 'EUR', '2026-01-04', 'HOTEL HOLD', '2026-01-05T03:04:05.000000000Z', '2026-01-05T03:04:05.000000000Z');

-- Bank refresh bookkeeping (B2, ADR-0020): the migration above, then the
-- migration ledger row and the fixture's refresh rows.
-- Bank refresh bookkeeping for the import engine (B2, ADR-0020). Both tables
-- are Ledger-managed operational state, not bank facts and not user data:
-- refresh never writes user data, and these rows are deleted only by the
-- explicit purge of their connection.

-- One row per refresh attempt of a connection: when it started and ended,
-- how it ended and what it changed. A run left 'running' by a stopped
-- process is marked 'interrupted' by the next run of that connection.
-- error_kind and error_code are the classified provider failure (banking
-- error taxonomy); they never hold a payload. transactions_rejected counts
-- bank rows dropped because they failed validation.
CREATE TABLE bank_refresh_runs (
    id                              TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    connection_id                   TEXT NOT NULL REFERENCES bank_connections (id) ON DELETE RESTRICT,
    started_at                      TEXT NOT NULL CHECK (length(started_at) = 30 AND started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    finished_at                     TEXT CHECK (finished_at IS NULL OR (length(finished_at) = 30 AND finished_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    outcome                         TEXT NOT NULL CHECK (outcome IN ('running', 'succeeded', 'partial', 'failed', 'interrupted')),
    error_kind                      TEXT NOT NULL DEFAULT '' CHECK (error_kind IN ('', 'reconnect_required', 'rate_limited', 'retryable', 'permanent', 'canceled')),
    error_code                      TEXT NOT NULL DEFAULT '',
    accounts_reported               INTEGER NOT NULL DEFAULT 0 CHECK (accounts_reported >= 0),
    accounts_succeeded              INTEGER NOT NULL DEFAULT 0 CHECK (accounts_succeeded >= 0),
    accounts_failed                 INTEGER NOT NULL DEFAULT 0 CHECK (accounts_failed >= 0),
    accounts_no_longer_reported     INTEGER NOT NULL DEFAULT 0 CHECK (accounts_no_longer_reported >= 0),
    balances_added                  INTEGER NOT NULL DEFAULT 0 CHECK (balances_added >= 0),
    balances_rejected               INTEGER NOT NULL DEFAULT 0 CHECK (balances_rejected >= 0),
    transactions_inserted           INTEGER NOT NULL DEFAULT 0 CHECK (transactions_inserted >= 0),
    transactions_updated            INTEGER NOT NULL DEFAULT 0 CHECK (transactions_updated >= 0),
    transactions_unchanged          INTEGER NOT NULL DEFAULT 0 CHECK (transactions_unchanged >= 0),
    transactions_kept_booked        INTEGER NOT NULL DEFAULT 0 CHECK (transactions_kept_booked >= 0),
    transactions_no_longer_reported INTEGER NOT NULL DEFAULT 0 CHECK (transactions_no_longer_reported >= 0),
    transactions_superseded         INTEGER NOT NULL DEFAULT 0 CHECK (transactions_superseded >= 0),
    transactions_rejected           INTEGER NOT NULL DEFAULT 0 CHECK (transactions_rejected >= 0),
    CHECK ((outcome = 'running') = (finished_at IS NULL)),
    CHECK (finished_at IS NULL OR finished_at >= started_at),
    CHECK (outcome NOT IN ('running', 'succeeded') OR (error_kind = '' AND error_code = ''))
) STRICT;

CREATE INDEX bank_refresh_runs_connection ON bank_refresh_runs (connection_id, started_at);

-- At most one running refresh per connection.
CREATE UNIQUE INDEX bank_refresh_runs_one_running ON bank_refresh_runs (connection_id) WHERE outcome = 'running';

-- A finished run is history.
CREATE TRIGGER bank_refresh_runs_finished_is_final
BEFORE UPDATE ON bank_refresh_runs
WHEN OLD.outcome <> 'running'
BEGIN
    SELECT RAISE(ABORT, 'a finished refresh run cannot change');
END;

-- Per-account refresh state. last_attempt_at is when the latest refresh of
-- the account started; last_success_at when one last completed without an
-- error (balances and every transaction window). last_error_* describe the
-- latest attempt's failure and are cleared by a success, so a failed attempt
-- after a success stays visible next to the data it could not replace.
-- last_notice_code is a code for something the latest attempt dropped without
-- failing (for example 'transactions_rejected'); an attempt without one
-- clears it.
-- transactions_through is the last day of the newest transaction window
-- imported completely: the next refresh resumes from it, minus an overlap.
CREATE TABLE bank_account_sync (
    account_id           TEXT PRIMARY KEY REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    last_attempt_at      TEXT NOT NULL CHECK (length(last_attempt_at) = 30 AND last_attempt_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    last_success_at      TEXT CHECK (last_success_at IS NULL OR (length(last_success_at) = 30 AND last_success_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_error_kind      TEXT NOT NULL DEFAULT '' CHECK (last_error_kind IN ('', 'reconnect_required', 'rate_limited', 'retryable', 'permanent', 'canceled')),
    last_error_code      TEXT NOT NULL DEFAULT '',
    last_notice_code     TEXT NOT NULL DEFAULT '',
    transactions_through TEXT CHECK (transactions_through IS NULL OR (length(transactions_through) = 10 AND transactions_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(transactions_through) = transactions_through)),
    CHECK (last_error_kind <> '' OR last_error_code = '')
) STRICT;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (5, 'bank_refresh', 'b59078d9fc18ced1c8e4d60893d39c251701b88d0a22d7d7edc74a58e062a041', '2026-01-06T03:04:05.000000000Z');
INSERT INTO bank_refresh_runs (id, connection_id, started_at, finished_at, outcome, accounts_reported, accounts_succeeded, balances_added, transactions_inserted, transactions_unchanged) VALUES ('01JZ0000000000000000000020', '01JZ0000000000000000000010', '2026-01-06T04:00:00.000000000Z', '2026-01-06T04:00:05.000000000Z', 'succeeded', 1, 1, 1, 2, 0);
INSERT INTO bank_refresh_runs (id, connection_id, started_at, finished_at, outcome, error_kind, error_code, accounts_reported, accounts_failed, transactions_rejected) VALUES ('01JZ0000000000000000000021', '01JZ0000000000000000000010', '2026-01-07T04:00:00.000000000Z', '2026-01-07T04:00:02.000000000Z', 'failed', 'rate_limited', 'fake-rate-limit', 1, 1, 0);
INSERT INTO bank_account_sync (account_id, last_attempt_at, last_success_at, last_error_kind, last_error_code, transactions_through) VALUES ('01JZ0000000000000000000011', '2026-01-07T04:00:02.000000000Z', '2026-01-06T04:00:05.000000000Z', 'rate_limited', 'fake-rate-limit', '2026-01-05');

-- Users and roles (migration 0006, A1).
CREATE TABLE users (
    id           TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    username     TEXT NOT NULL UNIQUE CHECK (length(username) BETWEEN 1 AND 64 AND username NOT GLOB '*[^a-z0-9._-]*' AND substr(username, 1, 1) GLOB '[a-z0-9]'),
    display_name TEXT CHECK (display_name IS NULL OR length(display_name) BETWEEN 1 AND 100),
    role         TEXT NOT NULL CHECK (role IN ('administrator', 'read-only')),
    disabled     INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
    disabled_at  TEXT CHECK (disabled_at IS NULL OR (length(disabled_at) = 30 AND disabled_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    created_at   TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at   TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    version      INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((disabled = 1) = (disabled_at IS NOT NULL))
) STRICT;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (6, 'users', 'e80355d8b6692e71df243b1ed470a462259ad17f253f5850194cec4084404b91', '2026-01-09T03:04:05.000000000Z');
INSERT INTO users (id, username, display_name, role, disabled, disabled_at, created_at, updated_at, version) VALUES ('01JZ0000000000000000000030', 'owner', 'Owner', 'administrator', 0, NULL, '2026-01-09T03:04:05.000000000Z', '2026-01-09T03:04:05.000000000Z', 1);
INSERT INTO users (id, username, display_name, role, disabled, disabled_at, created_at, updated_at, version) VALUES ('01JZ0000000000000000000031', 'viewer', NULL, 'read-only', 1, '2026-01-10T03:04:05.000000000Z', '2026-01-09T03:04:05.000000000Z', '2026-01-10T03:04:05.000000000Z', 2);

-- Currency-rate cache (migration 0007, FX1).
CREATE TABLE fx_rates (
    provider           TEXT NOT NULL CHECK (provider <> '' AND provider NOT GLOB '*[^a-z0-9_]*'),
    source             TEXT NOT NULL DEFAULT '',
    base_currency      TEXT NOT NULL CHECK (length(base_currency) = 3 AND base_currency GLOB '[A-Z][A-Z][A-Z]'),
    quote_currency     TEXT NOT NULL CHECK (length(quote_currency) = 3 AND quote_currency GLOB '[A-Z][A-Z][A-Z]'),
    rate               TEXT NOT NULL CHECK (length(rate) BETWEEN 1 AND 62 AND length(replace(replace(rate, '-', ''), '.', '')) BETWEEN 1 AND 60 AND rate NOT GLOB '*[^0-9.-]*' AND rate NOT GLOB '0[0-9]*' AND rate NOT GLOB '-0[0-9]*' AND replace(replace(rate, '0', ''), '.', '') <> '-' AND rate NOT GLOB '?*-*' AND rate NOT GLOB '*.*.*' AND rate NOT GLOB '*.' AND rate NOT GLOB '.*' AND rate NOT GLOB '-.*' AND rate GLOB '*[0-9]*' AND rate NOT GLOB '-*' AND replace(replace(rate, '0', ''), '.', '') <> ''),
    rate_date          TEXT NOT NULL CHECK (length(rate_date) = 10 AND rate_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(rate_date) = rate_date),
    first_retrieved_at TEXT NOT NULL CHECK (length(first_retrieved_at) = 30 AND first_retrieved_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    retrieved_at       TEXT NOT NULL CHECK (length(retrieved_at) = 30 AND retrieved_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    confirmed_through  TEXT NOT NULL CHECK (length(confirmed_through) = 10 AND confirmed_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(confirmed_through) = confirmed_through),
    previous_rate      TEXT CHECK (previous_rate IS NULL OR (length(previous_rate) BETWEEN 1 AND 62 AND length(replace(replace(previous_rate, '-', ''), '.', '')) BETWEEN 1 AND 60 AND previous_rate NOT GLOB '*[^0-9.-]*' AND previous_rate NOT GLOB '0[0-9]*' AND previous_rate NOT GLOB '-0[0-9]*' AND replace(replace(previous_rate, '0', ''), '.', '') <> '-' AND previous_rate NOT GLOB '?*-*' AND previous_rate NOT GLOB '*.*.*' AND previous_rate NOT GLOB '*.' AND previous_rate NOT GLOB '.*' AND previous_rate NOT GLOB '-.*' AND previous_rate GLOB '*[0-9]*' AND previous_rate NOT GLOB '-*' AND replace(replace(previous_rate, '0', ''), '.', '') <> '')),
    revised_at         TEXT CHECK (revised_at IS NULL OR (length(revised_at) = 30 AND revised_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK (base_currency <> quote_currency),
    CHECK (confirmed_through >= rate_date),
    CHECK ((previous_rate IS NULL) = (revised_at IS NULL)),
    CHECK (first_retrieved_at <= retrieved_at),
    PRIMARY KEY (provider, source, base_currency, quote_currency, rate_date)
) STRICT, WITHOUT ROWID;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (7, 'fx_rates', 'aa96ad39b3c724e1101381b03b879329435785083a8b7918e66016fe50832dc9', '2026-01-11T03:04:05.000000000Z');
INSERT INTO fx_rates (provider, source, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at, confirmed_through, previous_rate, revised_at) VALUES ('fake', '', 'EUR', 'USD', '1.0842', '2026-01-09', '2026-01-10T07:00:00.000000000Z', '2026-01-11T07:00:00.000000000Z', '2026-01-10', NULL, NULL);
INSERT INTO fx_rates (provider, source, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at, confirmed_through, previous_rate, revised_at) VALUES ('fake', 'ecb', 'EUR', 'GBP', '0.8571', '2026-01-09', '2026-01-11T07:00:00.000000000Z', '2026-01-11T07:00:00.000000000Z', '2026-01-09', '0.8569', '2026-01-11T07:00:00.000000000Z');

-- Audit log (migration 0008, A5).
CREATE TABLE audit_log (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    id             TEXT NOT NULL UNIQUE CHECK (length(id) = 26),
    action         TEXT NOT NULL CHECK (length(action) BETWEEN 3 AND 64 AND action GLOB '[a-z]*' AND action NOT GLOB '*[^a-z0-9._]*'),
    actor_kind     TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system', 'anonymous')),
    actor_user_id  TEXT NOT NULL DEFAULT '' CHECK (actor_user_id = '' OR length(actor_user_id) = 26),
    actor_token_id TEXT NOT NULL DEFAULT '' CHECK (actor_token_id = '' OR length(actor_token_id) = 26),
    target_type    TEXT NOT NULL DEFAULT '' CHECK (length(target_type) <= 32),
    target_id      TEXT NOT NULL DEFAULT '' CHECK (length(target_id) <= 64),
    occurred_at    TEXT NOT NULL CHECK (length(occurred_at) = 30 AND occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    details        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details) AND json_type(details) = 'object' AND length(details) <= 4096),
    CHECK ((actor_kind IN ('user', 'token')) = (actor_user_id <> '')),
    CHECK ((actor_kind = 'token') = (actor_token_id <> ''))
) STRICT;

CREATE INDEX audit_log_action ON audit_log (action, seq);
CREATE INDEX audit_log_actor_user ON audit_log (actor_user_id, seq) WHERE actor_user_id <> '';
CREATE INDEX audit_log_actor_token ON audit_log (actor_token_id, seq) WHERE actor_token_id <> '';
CREATE INDEX audit_log_target ON audit_log (target_type, target_id, seq);
CREATE INDEX audit_log_occurred_at ON audit_log (occurred_at);

CREATE TRIGGER audit_log_no_update
BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

CREATE TRIGGER audit_log_no_delete
BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (8, 'audit_log', 'f0d3774ff897e1e85e2d9dcf1f74bdb80165cc70e67f935d30746af42e1a1a52', '2026-01-12T03:04:05.000000000Z');
INSERT INTO audit_log (id, action, actor_kind, actor_user_id, actor_token_id, target_type, target_id, occurred_at, details) VALUES ('01JZ0000000000000000000040', 'user.created', 'system', '', '', 'user', '01JZ0000000000000000000030', '2026-01-12T03:04:05.000000000Z', '{"username":"owner","role":"administrator"}');
INSERT INTO audit_log (id, action, actor_kind, actor_user_id, actor_token_id, target_type, target_id, occurred_at, details) VALUES ('01JZ0000000000000000000041', 'auth.signin_failed', 'anonymous', '', '', 'user', '', '2026-01-12T04:00:00.000000000Z', '{"method":"password","reason":"invalid_credentials"}');

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

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (9, 'commitments', 'b17255944e1ce602779e41686d1b55517be18a6f56bdc386895b9b3bbdff0338', '2026-01-13T03:04:05.000000000Z');
INSERT INTO commitments (id, name, kind, amount, currency, schedule, start_date, end_date, priority, notes, created_at, updated_at, version) VALUES ('01JZ0000000000000000000050', 'VPS', 'server', '12.50', 'EUR', '{"version":1,"frequency":"monthly","interval":1,"day":31}', '2026-01-31', NULL, 'essential', NULL, '2026-01-13T03:04:05.000000000Z', '2026-01-13T03:04:05.000000000Z', 1);
INSERT INTO commitments (id, name, kind, amount, currency, schedule, start_date, end_date, priority, notes, created_at, updated_at, version) VALUES ('01JZ0000000000000000000051', 'example.com', 'domain', '15.000', 'USD', '{"version":1,"frequency":"annual","month":2,"day":29}', '2024-02-29', '2030-12-31', NULL, 'auto-renew off', '2026-01-13T04:05:06.000000000Z', '2026-01-13T04:05:06.000000000Z', 2);

CREATE TABLE user_passwords (
    user_id    TEXT PRIMARY KEY REFERENCES users (id),
    hash       TEXT NOT NULL CHECK (length(hash) BETWEEN 40 AND 512 AND substr(hash, 1, 10) = '$argon2id$'),
    changed_at TEXT NOT NULL CHECK (length(changed_at) = 30 AND changed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE TABLE sessions (
    id               TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    user_id          TEXT NOT NULL REFERENCES users (id),
    token_hash       TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    created_at       TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    last_seen_at     TEXT NOT NULL CHECK (length(last_seen_at) = 30 AND last_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    authenticated_at TEXT NOT NULL CHECK (length(authenticated_at) = 30 AND authenticated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    expires_at       TEXT NOT NULL CHECK (length(expires_at) = 30 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    user_agent       TEXT NOT NULL DEFAULT '' CHECK (length(user_agent) <= 256),
    address          TEXT NOT NULL DEFAULT '' CHECK (length(address) <= 64)
) STRICT;

CREATE INDEX sessions_user ON sessions (user_id, created_at);
CREATE INDEX sessions_expires_at ON sessions (expires_at);
CREATE INDEX sessions_last_seen_at ON sessions (last_seen_at);

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (10, 'auth_sessions', 'd32d6546afacbe798638643401c7f8a34ce6f66116c5619bc2b247988beebb25', '2026-01-14T03:04:05.000000000Z');
INSERT INTO user_passwords (user_id, hash, changed_at) VALUES ('01JZ0000000000000000000030', '$argon2id$v=19$m=65536,t=3,p=4$c3ludGhldGljc2FsdA$c3ludGhldGljdGFnc3ludGhldGljdGFn', '2026-01-14T04:00:00.000000000Z');
INSERT INTO sessions (id, user_id, token_hash, created_at, last_seen_at, authenticated_at, expires_at, user_agent, address) VALUES ('01JZ0000000000000000000100', '01JZ0000000000000000000030', '0000000000000000000000000000000000000000000000000000000000000001', '2026-01-14T04:00:00.000000000Z', '2026-01-14T04:10:00.000000000Z', '2026-01-14T04:00:00.000000000Z', '2026-01-14T16:00:00.000000000Z', 'fixture', '192.0.2.10');

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
-- pending_effective_from is the effective date of the latest terms edit that
-- takes effect after the day it was made: the definition holds only the latest
-- terms, so until that date the stored rows are the only record of the older
-- terms, and an edit effective earlier would overwrite them (ADR-0027). Null
-- when no such edit is pending; ignored once the date has passed.
CREATE TABLE commitment_occurrence_horizons (
    commitment_id     TEXT PRIMARY KEY REFERENCES commitments (id) ON DELETE CASCADE,
    generated_through TEXT NOT NULL CHECK (length(generated_through) = 7 AND generated_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]' AND substr(generated_through, 6, 2) BETWEEN '01' AND '12' AND generated_through >= '0001-01'),
    pending_effective_from TEXT CHECK (pending_effective_from IS NULL OR (length(pending_effective_from) = 10 AND pending_effective_from GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(pending_effective_from) = pending_effective_from)),
    updated_at        TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (11, 'commitment_occurrences', 'e6ac2f78a35aa6f59db95ef2bb345e362fe38bc5310561c367120e3f4c470b6c', '2026-01-15T03:04:05.000000000Z');
INSERT INTO commitment_occurrences (id, commitment_id, month, due_date, amount, currency, created_at, updated_at, version) VALUES ('01JZ0000000000000000000110', '01JZ0000000000000000000050', '2026-01', '2026-01-31', '12.50', 'EUR', '2026-01-15T04:00:00.000000000Z', '2026-01-15T04:00:00.000000000Z', 1);
INSERT INTO commitment_occurrence_horizons (commitment_id, generated_through, pending_effective_from, updated_at) VALUES ('01JZ0000000000000000000050', '2026-01', NULL, '2026-01-15T04:00:00.000000000Z');

CREATE TABLE api_tokens (
    id             TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    owner_id       TEXT NOT NULL REFERENCES users (id),
    name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash     TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    display_prefix TEXT NOT NULL CHECK (length(display_prefix) = 9 AND substr(display_prefix, 1, 5) = 'ldgr_' AND substr(display_prefix, 6) NOT GLOB '*[^0-9A-Za-z]*'),
    last_chars     TEXT NOT NULL CHECK (length(last_chars) = 4 AND last_chars NOT GLOB '*[^0-9A-Za-z]*'),
    root           INTEGER NOT NULL CHECK (root IN (0, 1)),
    grants         TEXT NOT NULL CHECK (length(grants) <= 4096 AND grants NOT GLOB '*[^a-z0-9_:,]*'),
    created_at     TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    expires_at     TEXT CHECK (expires_at IS NULL OR (length(expires_at) = 30 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_used_at   TEXT CHECK (last_used_at IS NULL OR (length(last_used_at) = 30 AND last_used_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    revoked_at     TEXT CHECK (revoked_at IS NULL OR (length(revoked_at) = 30 AND revoked_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK ((root = 1) = (grants = ''))
) STRICT;

CREATE INDEX api_tokens_owner ON api_tokens (owner_id, id);

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (12, 'api_tokens', '9273c12d90e8c934be988f3ed646b82f729a2c60fe86c6463ae057b594622dcf', '2026-01-15T03:04:05.000000000Z');
INSERT INTO api_tokens (id, owner_id, name, token_hash, display_prefix, last_chars, root, grants, created_at, expires_at, last_used_at, revoked_at) VALUES ('01JZ0000000000000000000120', '01JZ0000000000000000000030', 'backup script', '00000000000000000000000000000000000000000000000000000000000000a2', 'ldgr_Fx12', 'Zz09', 0, 'commitments:read', '2026-01-15T04:00:00.000000000Z', NULL, '2026-01-15T05:00:00.000000000Z', NULL);

CREATE TABLE user_totp (
    user_id        TEXT PRIMARY KEY REFERENCES users (id),
    secret         TEXT CHECK (secret IS NULL OR (length(secret) BETWEEN 40 AND 512 AND substr(secret, 1, 3) = 'v1.' AND substr(secret, 4) NOT GLOB '*[^A-Za-z0-9_-]*')),
    enabled_at     TEXT CHECK (enabled_at IS NULL OR (length(enabled_at) = 30 AND enabled_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    last_step      INTEGER NOT NULL DEFAULT 0 CHECK (last_step >= 0),
    pending_secret TEXT CHECK (pending_secret IS NULL OR (length(pending_secret) BETWEEN 40 AND 512 AND substr(pending_secret, 1, 3) = 'v1.' AND substr(pending_secret, 4) NOT GLOB '*[^A-Za-z0-9_-]*')),
    pending_at     TEXT CHECK (pending_at IS NULL OR (length(pending_at) = 30 AND pending_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK ((secret IS NULL) = (enabled_at IS NULL)),
    CHECK ((pending_secret IS NULL) = (pending_at IS NULL)),
    CHECK (secret IS NOT NULL OR pending_secret IS NOT NULL)
) STRICT;

CREATE TABLE user_recovery_codes (
    user_id    TEXT NOT NULL REFERENCES users (id),
    code_hash  TEXT NOT NULL CHECK (length(code_hash) = 64 AND code_hash NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    PRIMARY KEY (user_id, code_hash)
) STRICT;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (13, 'totp', '3fca5b1d67e9bb9664d8b4a2633b0c0aa8c6322451d1d63e9e29f461210b0cb2', '2026-01-16T03:04:05.000000000Z');

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

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (14, 'payment_state', '90d35392cfc52b69987b18ffbbd27cbf333a65aad9af0a0e76fc61e811c235b7', '2026-01-17T03:04:05.000000000Z');

-- Single sign-on (A6, ADR-0033).
--
-- oidc_links ties a user account to one identity at the configured OpenID
-- Connect provider, keyed by the stable (issuer, subject) pair, never by email
-- address. An account has at most one link and an identity belongs to at most
-- one account. The subject is compared exactly as the provider sends it.
-- email is the address the provider last reported, kept for display only.
-- Changing the configured issuer leaves existing links unusable, by design.
--
-- recovery_sign_ins holds at most one one-time sign-in per user, issued from
-- the host (break-glass, decision D16). Only the SHA-256 hash of its token is
-- stored; the row is deleted when it is used.
--
-- sessions.method records how a session started (password, oidc or recovery):
-- an OIDC session is not confined to two-factor enrolment while Ledger trusts
-- the provider's authentication (decision D16). Existing sessions were all
-- password sessions.
CREATE TABLE oidc_links (
    id           TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    user_id      TEXT NOT NULL UNIQUE REFERENCES users (id),
    issuer       TEXT NOT NULL CHECK (length(issuer) BETWEEN 1 AND 2048),
    subject      TEXT NOT NULL CHECK (length(CAST(subject AS BLOB)) BETWEEN 1 AND 255),
    email        TEXT NOT NULL DEFAULT '' CHECK (length(email) <= 254),
    created_at   TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    last_used_at TEXT CHECK (last_used_at IS NULL OR (length(last_used_at) = 30 AND last_used_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    UNIQUE (issuer, subject)
) STRICT;

CREATE TABLE recovery_sign_ins (
    user_id    TEXT PRIMARY KEY REFERENCES users (id),
    token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    expires_at TEXT NOT NULL CHECK (length(expires_at) = 30 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

ALTER TABLE sessions ADD COLUMN method TEXT NOT NULL DEFAULT 'password' CHECK (method IN ('password', 'oidc', 'recovery'));

INSERT INTO oidc_links (id, user_id, issuer, subject, email, created_at, last_used_at) VALUES ('01JZ0000000000000000000150', '01JZ0000000000000000000030', 'https://idp.example.com', 'subject-0001', 'owner@example.com', '2026-01-18T03:04:05.000000000Z', NULL);

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (15, 'oidc', '8116d65cca9164158de661cf7a9daab1c83b6dab1bc3b6eede9d7c65a28045a4', '2026-01-18T03:04:05.000000000Z');

CREATE TABLE device_cookies (
    id         TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    user_id    TEXT NOT NULL REFERENCES users (id),
    token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    expires_at TEXT NOT NULL CHECK (length(expires_at) = 30 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (expires_at > created_at)
) STRICT;

CREATE INDEX device_cookies_user ON device_cookies (user_id, created_at);

INSERT INTO device_cookies (id, user_id, token_hash, created_at, expires_at) VALUES ('01JZ0000000000000000000160', '01JZ0000000000000000000030', 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd', '2026-01-19T03:04:05.000000000Z', '2026-04-19T03:04:05.000000000Z');

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (16, 'device_cookies', 'ca9bc9cdb936268e913c3643acf9d607b45fed829d779c7a7cccfa91b5a641bd', '2026-01-19T03:04:05.000000000Z');

-- Migration 0017_payment_matches.
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

INSERT INTO payment_matches (id, transaction_id, occurrence_id, amount, currency, origin, state, created_by_kind, created_by_user, decided_by_kind, decided_by_user, decided_at, reason, created_at, updated_at, version) VALUES ('01JZ0000000000000000000170', '01JZ0000000000000000000013', '01JZ0000000000000000000110', '12.50', 'EUR', 'user', 'confirmed', 'user', '01JZ0000000000000000000030', 'user', '01JZ0000000000000000000030', '2026-01-20T04:00:00.000000000Z', NULL, '2026-01-20T04:00:00.000000000Z', '2026-01-20T04:00:00.000000000Z', 1);
INSERT INTO payment_match_events (id, match_id, action, state, amount, currency, actor_kind, actor_user, reason, at) VALUES ('01JZ0000000000000000000171', '01JZ0000000000000000000170', 'confirmed', 'confirmed', '12.50', 'EUR', 'user', '01JZ0000000000000000000030', NULL, '2026-01-20T04:00:00.000000000Z');

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (17, 'payment_matches', '4e63825de5c1bd9078ab96ff7280aa98468fdd06b058230a8841c7f2a1d7940f', '2026-01-20T03:04:05.000000000Z');

ALTER TABLE api_tokens ADD COLUMN parent_token_id TEXT REFERENCES api_tokens (id);

CREATE INDEX api_tokens_parent ON api_tokens (parent_token_id) WHERE parent_token_id IS NOT NULL;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (18, 'token_parents', 'a6577ff7eaadeb17f4c509141616da7bffdaf751c0a879e9b14e2882b34182f0', '2026-01-21T03:04:05.000000000Z');

INSERT INTO api_tokens (id, owner_id, name, token_hash, display_prefix, last_chars, root, grants, created_at, expires_at, last_used_at, revoked_at, parent_token_id) VALUES ('01JZ0000000000000000000180', '01JZ0000000000000000000030', 'child script', '00000000000000000000000000000000000000000000000000000000000000a3', 'ldgr_Gh34', 'Yy98', 0, 'commitments:read', '2026-01-21T04:00:00.000000000Z', NULL, NULL, NULL, '01JZ0000000000000000000120');

-- Soft-delete manual payments (C3c, decision D26-3a, ADR-0031 point 3). Deleting
-- a manual payment keeps its row: deleted_at and deleted_by mark it, and it
-- stops counting as payment evidence and drops out of lists by default (code
-- filters on deleted_at IS NULL). deleted_by is the actor that deleted it
-- ("user:<id>", "token:<id>" or "system"). The row still refers to its
-- occurrence with ON DELETE RESTRICT, so the occurrence keeps its payment
-- history and cannot be removed with it, but it does not make the occurrence
-- paid: terms edits still apply. Existing rows are not deleted.
ALTER TABLE manual_payments ADD COLUMN deleted_at TEXT CHECK (deleted_at IS NULL OR (length(deleted_at) = 30 AND deleted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE manual_payments ADD COLUMN deleted_by TEXT CHECK (deleted_by IS NULL OR length(deleted_by) BETWEEN 1 AND 100);

CREATE TRIGGER manual_payments_deleted_pair
BEFORE UPDATE ON manual_payments
WHEN (NEW.deleted_at IS NULL) <> (NEW.deleted_by IS NULL) OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NOT OLD.deleted_at)
BEGIN
    SELECT RAISE(ABORT, 'a manual payment is deleted with both deleted_at and deleted_by, and stays deleted');
END;

-- A manual payment is never removed outright: deleting is marking.
CREATE TRIGGER manual_payments_kept
BEFORE DELETE ON manual_payments
BEGIN
    SELECT RAISE(ABORT, 'a manual payment is never removed, only marked deleted');
END;

-- Cancelled occurrences and archived commitments (C3f, decision D31,
-- ADR-0027, ADR-0031). When removing an occurrence is intended (a start or end
-- date correction, an edit that no longer schedules the month, a commitment
-- delete) but a soft-deleted manual payment still refers to it, the occurrence
-- is not deleted: cancelled_at and cancelled_reason mark it. It keeps its
-- payment history, but leaves the monthly totals, the overdue counts and the
-- outlook, and takes no new payment or match. cancelled_reason is one of
-- out_of_range, terms_change, commitment_deleted. A later edit that schedules
-- the month again revives the row (both columns are cleared).
ALTER TABLE commitment_occurrences ADD COLUMN cancelled_at TEXT CHECK (cancelled_at IS NULL OR (length(cancelled_at) = 30 AND cancelled_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE commitment_occurrences ADD COLUMN cancelled_reason TEXT CHECK (cancelled_reason IS NULL OR cancelled_reason IN ('out_of_range', 'terms_change', 'commitment_deleted'));

CREATE TRIGGER commitment_occurrences_cancelled_pair
BEFORE UPDATE OF cancelled_at, cancelled_reason ON commitment_occurrences
WHEN (NEW.cancelled_at IS NULL) <> (NEW.cancelled_reason IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'an occurrence is cancelled with both cancelled_at and cancelled_reason');
END;

CREATE TRIGGER manual_payments_not_on_cancelled
BEFORE INSERT ON manual_payments
WHEN (SELECT cancelled_at FROM commitment_occurrences WHERE id = NEW.occurrence_id) IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a cancelled occurrence takes no new payment');
END;

CREATE TRIGGER payment_matches_not_on_cancelled
BEFORE INSERT ON payment_matches
WHEN (SELECT cancelled_at FROM commitment_occurrences WHERE id = NEW.occurrence_id) IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a cancelled occurrence takes no new payment match');
END;

-- A commitment is deleted outright when no occurrence refers to it any more.
-- When cancelled occurrences keep payment history, the commitment row is kept
-- and marked deleted_at (archived): every read, list, edit and delete of
-- commitments treats it as gone, and no occurrences are generated for it.
ALTER TABLE commitments ADD COLUMN deleted_at TEXT CHECK (deleted_at IS NULL OR (length(deleted_at) = 30 AND deleted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (19, 'manual_payment_soft_delete', '0960ea2d0e6195cf08570177022ddece82a1847e74622c0ef60b78661805ea42', '2026-01-22T03:04:05.000000000Z');

-- Per-commitment amount tolerance for automatic payment matching (task X2,
-- decision D9: auto-confirm needs the exact amount "or within the user's
-- tolerance"; ADR-0039). Both columns are NULL when the user set none: then
-- only an exact amount can confirm automatically. When set, the tolerance is a
-- non-negative exact decimal in the commitment's own currency (amounts are
-- never converted, ADR-0005); the pair is set or cleared together, and a
-- commitment's currency cannot change away from its tolerance's. Existing
-- commitments get no tolerance. It is never summed or compared in SQL.
ALTER TABLE commitments ADD COLUMN match_tolerance_amount TEXT CHECK (match_tolerance_amount IS NULL OR (length(match_tolerance_amount) BETWEEN 1 AND 62 AND length(replace(replace(match_tolerance_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND match_tolerance_amount NOT GLOB '*[^0-9.-]*' AND match_tolerance_amount NOT GLOB '0[0-9]*' AND match_tolerance_amount NOT GLOB '-0[0-9]*' AND replace(replace(match_tolerance_amount, '0', ''), '.', '') <> '-' AND match_tolerance_amount NOT GLOB '?*-*' AND match_tolerance_amount NOT GLOB '*.*.*' AND match_tolerance_amount NOT GLOB '*.' AND match_tolerance_amount NOT GLOB '.*' AND match_tolerance_amount NOT GLOB '-.*' AND match_tolerance_amount GLOB '*[0-9]*' AND match_tolerance_amount NOT GLOB '-*'));
ALTER TABLE commitments ADD COLUMN match_tolerance_currency TEXT CHECK (match_tolerance_currency IS NULL OR (length(match_tolerance_currency) = 3 AND match_tolerance_currency GLOB '[A-Z][A-Z][A-Z]'));

CREATE TRIGGER commitments_match_tolerance_insert
BEFORE INSERT ON commitments
WHEN (NEW.match_tolerance_amount IS NULL) <> (NEW.match_tolerance_currency IS NULL) OR NEW.match_tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a match tolerance has an amount and the commitment''s currency');
END;

CREATE TRIGGER commitments_match_tolerance_update
BEFORE UPDATE OF currency, match_tolerance_amount, match_tolerance_currency ON commitments
WHEN (NEW.match_tolerance_amount IS NULL) <> (NEW.match_tolerance_currency IS NULL) OR NEW.match_tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a match tolerance has an amount and the commitment''s currency');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (20, 'commitment_match_tolerance', 'ed585e685d2a012a303fc7a4a59de3ccc741e96c3c9e8994a6ae1a34eb2fce98', '2026-01-23T03:04:05.000000000Z');

-- Migration 0021, as the binary at version 21 left it.
-- The tolerance a payment match was confirmed within (D39 5a, ADR-0042).
-- When a bank match is confirmed (or its amount corrected while confirmed) and
-- the occurrence's counted evidence is confirmed bank matches alone, short of
-- the occurrence's amount by no more than the commitment's match tolerance,
-- the store records that tolerance here, inside the write transaction. The
-- payment state is derived from this record, never from the commitment's
-- current tolerance, so editing a tolerance later does not change past
-- occurrences. Both columns are NULL when the match was not confirmed within
-- a tolerance (every existing match: none was). The pair is set or cleared
-- together, in the match's own currency; nothing is converted (ADR-0005).
ALTER TABLE payment_matches ADD COLUMN tolerance_amount TEXT CHECK (tolerance_amount IS NULL OR (length(tolerance_amount) BETWEEN 1 AND 62 AND length(replace(replace(tolerance_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND tolerance_amount NOT GLOB '*[^0-9.-]*' AND tolerance_amount NOT GLOB '0[0-9]*' AND tolerance_amount NOT GLOB '-0[0-9]*' AND replace(replace(tolerance_amount, '0', ''), '.', '') <> '-' AND tolerance_amount NOT GLOB '?*-*' AND tolerance_amount NOT GLOB '*.*.*' AND tolerance_amount NOT GLOB '*.' AND tolerance_amount NOT GLOB '.*' AND tolerance_amount NOT GLOB '-.*' AND tolerance_amount GLOB '*[0-9]*' AND tolerance_amount NOT GLOB '-*'));
ALTER TABLE payment_matches ADD COLUMN tolerance_currency TEXT CHECK (tolerance_currency IS NULL OR (length(tolerance_currency) = 3 AND tolerance_currency GLOB '[A-Z][A-Z][A-Z]'));

CREATE TRIGGER payment_matches_tolerance_insert
BEFORE INSERT ON payment_matches
WHEN (NEW.tolerance_amount IS NULL) <> (NEW.tolerance_currency IS NULL) OR NEW.tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a recorded tolerance has an amount and the match''s currency');
END;

CREATE TRIGGER payment_matches_tolerance_update
BEFORE UPDATE OF currency, tolerance_amount, tolerance_currency ON payment_matches
WHEN (NEW.tolerance_amount IS NULL) <> (NEW.tolerance_currency IS NULL) OR NEW.tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a recorded tolerance has an amount and the match''s currency');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (21, 'match_confirmed_tolerance', '229bd318c3f54da5fa23e16b8dd617d04276f6e68a9d1038129254f31fcfb5d8', '2026-01-24T03:04:05.000000000Z');

-- A connection still waiting for its redirect, to see migration 0022 date it.
INSERT INTO bank_connections (id, provider, application, mode, environment, account_access, institution_id, institution_name, institution_country, status, consent_state, created_at, updated_at, version) VALUES ('01JZ0000000000000000000090', 'fake', 'fake-app', 'app', 'sandbox', 'all', 'Fake Bank', 'Fake Bank', 'FI', 'pending', 'waiting-state', '2026-01-22T03:04:05.000000000Z', '2026-01-22T04:00:00.000000000Z', 1);

-- Migration 0022, as the binary at version 22 left it.
-- When a consent started and who started it (task B4b, ADR-0040). A consent
-- state is single use: it is claimed (cleared) before the provider is called,
-- it expires after a time to live, and only the person who started it may
-- complete it. consent_initiator is the starting user's id (a token acts as
-- its owner); it holds no secret. Rows that already wait for a redirect take
-- their last update as the start, so an abandoned one expires like a new one.
ALTER TABLE bank_connections ADD COLUMN consent_started_at TEXT CHECK (consent_started_at IS NULL OR (length(consent_started_at) = 30 AND consent_started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE bank_connections ADD COLUMN consent_initiator TEXT NOT NULL DEFAULT '';
UPDATE bank_connections SET consent_started_at = updated_at WHERE consent_state IS NOT NULL;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (22, 'bank_consent_origin', 'd12f7d3b431c95dfa6ef40414dcfaa084c04031662642997601cf38af32e6971', '2026-01-25T03:04:05.000000000Z');

-- A suggestion on the pending transaction, made by the engine and corrected by
-- a user, to see migration 0023 copy its history in order.
INSERT INTO payment_matches (id, transaction_id, occurrence_id, amount, currency, origin, state, created_by_kind, created_by_user, decided_by_kind, decided_by_user, decided_at, reason, created_at, updated_at, version) VALUES ('01JZ0000000000000000000172', '01JZ0000000000000000000014', '01JZ0000000000000000000110', '12.00', 'EUR', 'auto_rule', 'suggested', 'system', NULL, NULL, NULL, NULL, 'corrected by hand', '2026-01-25T04:00:00.000000000Z', '2026-01-25T05:00:00.000000000Z', 2);
INSERT INTO payment_match_events (id, match_id, action, state, amount, currency, actor_kind, actor_user, reason, at) VALUES ('01JZ0000000000000000000174', '01JZ0000000000000000000172', 'suggested', 'suggested', '70.00', 'EUR', 'system', NULL, NULL, '2026-01-25T04:00:00.000000000Z');
INSERT INTO payment_match_events (id, match_id, action, state, amount, currency, actor_kind, actor_user, reason, at) VALUES ('01JZ0000000000000000000173', '01JZ0000000000000000000172', 'corrected', 'suggested', '12.00', 'EUR', 'user', '01JZ0000000000000000000030', 'corrected by hand', '2026-01-25T05:00:00.000000000Z');

-- Migration 0023, as the binary at version 23 left it.
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

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (23, 'match_unconfirm_provenance', '9dd8ee4b481440b5c18f57803fab5a37704cc25fdf904ec2083236150a1eeb87', '2026-01-26T03:04:05.000000000Z');

-- A manual payment of the occurrence, to see migration 0024 leave it counting.
INSERT INTO manual_payments (id, occurrence_id, amount, currency, paid_on, note, created_at, updated_at, version) VALUES ('01JZ0000000000000000000180', '01JZ0000000000000000000110', '5.00', 'EUR', '2026-01-20', NULL, '2026-01-26T04:00:00.000000000Z', '2026-01-26T04:00:00.000000000Z', 1);

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

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (24, 'manual_payment_replacements', 'b6607f614d2339d611dbe7239d49981a02ff4a4c6eaf0424b71a660053432a81', '2026-01-27T03:04:05.000000000Z');

-- Deferral plans (DF1, ADR-0050). A deferral plan records that the user means
-- to pay an outstanding occurrence later than its due date: a personal plan, an
-- extension the creditor agreed to, or a documented grace period (decision D14:
-- a creditor-approved change is a distinct plan type). A plan is separate from
-- the occurrence: it never changes the occurrence's due date, amount or
-- payment state, so the obligation, its overdue history (decision D7: overdue
-- counts from the original due date) and its payments stay as they are. A plan
-- is never payment evidence: nothing here makes an occurrence paid.
--
-- original_due_date and original_amount are the occurrence's due date and
-- amount when the plan was made, and outstanding_amount what was still owed
-- then; they are history and never change (the occurrence's live state is
-- derived when read). Amounts are exact decimal text (ADR-0005), never summed in
-- SQL. A plan refers to its occurrence with ON DELETE RESTRICT: an occurrence
-- with a plan (active or undone) is cancelled instead of deleted when its
-- commitment's terms no longer schedule it (D31), so the plan's history stays.
--
-- An occurrence has at most one active plan. Revising changes plan_type,
-- planned_date and reason and increases version; undoing sets undone_at, after
-- which the row never changes again. Every change is one row of
-- deferral_plan_events.
CREATE TABLE deferral_plans (
    id                 TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    occurrence_id      TEXT NOT NULL REFERENCES commitment_occurrences (id) ON DELETE RESTRICT CHECK (length(occurrence_id) = 26 AND occurrence_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_type          TEXT NOT NULL CHECK (plan_type IN ('personal', 'creditor_agreed', 'documented_grace')),
    planned_date       TEXT NOT NULL CHECK (length(planned_date) = 10 AND planned_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(planned_date) = planned_date),
    reason             TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    original_due_date  TEXT NOT NULL CHECK (length(original_due_date) = 10 AND original_due_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(original_due_date) = original_due_date),
    original_amount    TEXT NOT NULL CHECK (length(original_amount) BETWEEN 1 AND 62 AND length(replace(replace(original_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND original_amount NOT GLOB '*[^0-9.-]*' AND original_amount NOT GLOB '0[0-9]*' AND original_amount NOT GLOB '-0[0-9]*' AND replace(replace(original_amount, '0', ''), '.', '') <> '-' AND original_amount NOT GLOB '?*-*' AND original_amount NOT GLOB '*.*.*' AND original_amount NOT GLOB '*.' AND original_amount NOT GLOB '.*' AND original_amount NOT GLOB '-.*' AND original_amount GLOB '*[0-9]*' AND original_amount NOT GLOB '-*' AND replace(replace(original_amount, '0', ''), '.', '') <> ''),
    original_currency  TEXT NOT NULL CHECK (length(original_currency) = 3 AND original_currency GLOB '[A-Z][A-Z][A-Z]'),
    outstanding_amount   TEXT NOT NULL CHECK (length(outstanding_amount) BETWEEN 1 AND 62 AND length(replace(replace(outstanding_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND outstanding_amount NOT GLOB '*[^0-9.-]*' AND outstanding_amount NOT GLOB '0[0-9]*' AND outstanding_amount NOT GLOB '-0[0-9]*' AND replace(replace(outstanding_amount, '0', ''), '.', '') <> '-' AND outstanding_amount NOT GLOB '?*-*' AND outstanding_amount NOT GLOB '*.*.*' AND outstanding_amount NOT GLOB '*.' AND outstanding_amount NOT GLOB '.*' AND outstanding_amount NOT GLOB '-.*' AND outstanding_amount GLOB '*[0-9]*' AND outstanding_amount NOT GLOB '-*' AND replace(replace(outstanding_amount, '0', ''), '.', '') <> ''),
    outstanding_currency TEXT NOT NULL CHECK (length(outstanding_currency) = 3 AND outstanding_currency GLOB '[A-Z][A-Z][A-Z]'),
    created_at         TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at         TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    undone_at          TEXT CHECK (undone_at IS NULL OR (length(undone_at) = 30 AND undone_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (outstanding_currency = original_currency)
) STRICT;

CREATE INDEX deferral_plans_occurrence ON deferral_plans (occurrence_id, id);
CREATE INDEX deferral_plans_planned ON deferral_plans (planned_date, id) WHERE undone_at IS NULL;

-- At most one active plan per occurrence: a deferred occurrence is planned
-- once, so its amount is never counted twice.
CREATE UNIQUE INDEX deferral_plans_one_active ON deferral_plans (occurrence_id) WHERE undone_at IS NULL;

-- What a plan is about and what was owed when it was made never change.
CREATE TRIGGER deferral_plans_identity_immutable
BEFORE UPDATE OF id, occurrence_id, original_due_date, original_amount, original_currency, outstanding_amount, outstanding_currency, created_at ON deferral_plans
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan cannot change its occurrence or what was owed when it was made');
END;

-- An undone plan is history: it never changes again.
CREATE TRIGGER deferral_plans_undone_final
BEFORE UPDATE ON deferral_plans
WHEN OLD.undone_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'an undone deferral plan cannot change');
END;

-- A plan is in its occurrence's currency.
CREATE TRIGGER deferral_plans_currency
BEFORE INSERT ON deferral_plans
WHEN NEW.original_currency IS NOT (SELECT currency FROM commitment_occurrences WHERE id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan must be in the currency of its occurrence');
END;

-- Plans are planning history: never deleted.
CREATE TRIGGER deferral_plans_kept
BEFORE DELETE ON deferral_plans
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan is never deleted; undo it instead');
END;

-- deferral_plan_adjustments are the fees, interest and other charges the user
-- records explicitly on a plan (decision D14): a plan never adds a fee by
-- itself. Each is a positive amount in the plan's currency. Removing one sets
-- removed_at once; rows are never deleted.
CREATE TABLE deferral_plan_adjustments (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_id     TEXT NOT NULL REFERENCES deferral_plans (id) ON DELETE RESTRICT CHECK (length(plan_id) = 26 AND plan_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    kind        TEXT NOT NULL CHECK (kind IN ('fee', 'interest', 'other')),
    amount      TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency    TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    note        TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500),
    created_at  TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    removed_at  TEXT CHECK (removed_at IS NULL OR (length(removed_at) = 30 AND removed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'))
) STRICT;

CREATE INDEX deferral_plan_adjustments_plan ON deferral_plan_adjustments (plan_id, created_at, id);

CREATE TRIGGER deferral_plan_adjustments_currency
BEFORE INSERT ON deferral_plan_adjustments
WHEN NEW.currency IS NOT (SELECT original_currency FROM deferral_plans WHERE id = NEW.plan_id)
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment must be in the currency of its plan');
END;

CREATE TRIGGER deferral_plan_adjustments_only_removed
BEFORE UPDATE ON deferral_plan_adjustments
WHEN OLD.removed_at IS NOT NULL OR NEW.removed_at IS NULL
  OR NEW.id IS NOT OLD.id OR NEW.plan_id IS NOT OLD.plan_id OR NEW.kind IS NOT OLD.kind OR NEW.amount IS NOT OLD.amount
  OR NEW.currency IS NOT OLD.currency OR NEW.note IS NOT OLD.note OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment can only be removed, once');
END;

CREATE TRIGGER deferral_plan_adjustments_kept
BEFORE DELETE ON deferral_plan_adjustments
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment is never deleted');
END;

-- deferral_plan_events is the append-only history of each plan: every change
-- with the plan's type and planned date after it, who made it, how (D42 3a)
-- and the reason recorded with it. plan_version is the plan's version after
-- the change, so the history reads in order. The audit log records the same
-- changes without reasons or amounts (ADR-0025).
CREATE TABLE deferral_plan_events (
    id            TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_id       TEXT NOT NULL REFERENCES deferral_plans (id) ON DELETE RESTRICT CHECK (length(plan_id) = 26 AND plan_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_version  INTEGER NOT NULL CHECK (plan_version >= 1),
    action        TEXT NOT NULL CHECK (action IN ('created', 'revised', 'undone', 'adjustment_added', 'adjustment_removed')),
    plan_type     TEXT NOT NULL CHECK (plan_type IN ('personal', 'creditor_agreed', 'documented_grace')),
    planned_date  TEXT NOT NULL CHECK (length(planned_date) = 10 AND planned_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(planned_date) = planned_date),
    reason        TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    adjustment_id TEXT REFERENCES deferral_plan_adjustments (id) ON DELETE RESTRICT CHECK (adjustment_id IS NULL OR (length(adjustment_id) = 26 AND adjustment_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    actor_user  TEXT CHECK (actor_user IS NULL OR (length(actor_user) = 26 AND actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_via   TEXT CHECK (actor_via IS NULL OR actor_via IN ('session', 'token', 'mcp', 'system')),
    at            TEXT NOT NULL CHECK (length(at) = 30 AND at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK ((adjustment_id IS NOT NULL) = (action IN ('adjustment_added', 'adjustment_removed'))),
    CHECK (actor_via IS NULL OR (actor_via = 'session' AND actor_kind = 'user') OR (actor_via IN ('token', 'mcp') AND actor_kind = 'token') OR (actor_via = 'system' AND actor_kind = 'system'))
) STRICT;

CREATE UNIQUE INDEX deferral_plan_events_order ON deferral_plan_events (plan_id, plan_version);

CREATE TRIGGER deferral_plan_events_append_only
BEFORE UPDATE ON deferral_plan_events
BEGIN
    SELECT RAISE(ABORT, 'deferral plan history is append-only');
END;

CREATE TRIGGER deferral_plan_events_kept
BEFORE DELETE ON deferral_plan_events
BEGIN
    SELECT RAISE(ABORT, 'deferral plan history is never deleted');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (25, 'deferral_plans', '3e2701053fbfc027830110036f3a6cae23a5e1d54e59e6fe4e9b4d2833e1d32d', '2026-01-28T03:04:05.000000000Z');

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

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (26, 'merchants', '620dc4063667ab53f34ca7b5007f6b03767314153382691e8c0e023bc1a4e8b5', '2026-01-29T03:04:05.000000000Z');

-- Migration 0027 applied to the version 26 contents above.
-- Commitment leeway (task DF2, ADR-0052, decision D14): what the user knows
-- about the grace a creditor allows after a due date. Leeway is user-entered
-- only: nothing writes it from payment history, bank data or model output, and
-- there is no default.
--
-- leeway_kind is NULL when the user recorded nothing (existing commitments
-- stay that way). 'unknown' is the user's explicit statement that they do not
-- know and 'none' that no grace is allowed; neither carries days or a date, and
-- neither is read as zero. 'period' needs leeway_days (1 to 365, counted from
-- each occurrence's due date) and 'deadline' leeway_until (a fixed last day of
-- grace). The optional text members hold the conditions the grace depends on,
-- the fees or other consequences the user knows of, and where the information
-- comes from; any are NULL when not given. Changing leeway never touches a
-- commitment's occurrences, their due dates or payment evidence: a grace only
-- annotates an overdue occurrence (decision D7, ADR-0031).
ALTER TABLE commitments ADD COLUMN leeway_kind TEXT CHECK (leeway_kind IS NULL OR leeway_kind IN ('unknown', 'none', 'period', 'deadline'));
ALTER TABLE commitments ADD COLUMN leeway_days INTEGER CHECK (leeway_days IS NULL OR leeway_days BETWEEN 1 AND 365);
ALTER TABLE commitments ADD COLUMN leeway_until TEXT CHECK (leeway_until IS NULL OR (length(leeway_until) = 10 AND leeway_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(leeway_until) = leeway_until));
ALTER TABLE commitments ADD COLUMN leeway_conditions TEXT CHECK (leeway_conditions IS NULL OR length(leeway_conditions) BETWEEN 1 AND 500);
ALTER TABLE commitments ADD COLUMN leeway_consequences TEXT CHECK (leeway_consequences IS NULL OR length(leeway_consequences) BETWEEN 1 AND 500);
ALTER TABLE commitments ADD COLUMN leeway_source TEXT CHECK (leeway_source IS NULL OR length(leeway_source) BETWEEN 1 AND 200);

-- The members fit the kind; nothing may be recorded without a kind.
CREATE TRIGGER commitments_leeway_insert
BEFORE INSERT ON commitments
WHEN (NEW.leeway_kind IS NULL AND (NEW.leeway_days IS NOT NULL OR NEW.leeway_until IS NOT NULL OR NEW.leeway_conditions IS NOT NULL OR NEW.leeway_consequences IS NOT NULL OR NEW.leeway_source IS NOT NULL))
  OR (NEW.leeway_kind = 'period') <> (NEW.leeway_days IS NOT NULL)
  OR (NEW.leeway_kind = 'deadline') <> (NEW.leeway_until IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'leeway members must match the leeway kind');
END;

CREATE TRIGGER commitments_leeway_update
BEFORE UPDATE OF leeway_kind, leeway_days, leeway_until, leeway_conditions, leeway_consequences, leeway_source ON commitments
WHEN (NEW.leeway_kind IS NULL AND (NEW.leeway_days IS NOT NULL OR NEW.leeway_until IS NOT NULL OR NEW.leeway_conditions IS NOT NULL OR NEW.leeway_consequences IS NOT NULL OR NEW.leeway_source IS NOT NULL))
  OR (NEW.leeway_kind = 'period') <> (NEW.leeway_days IS NOT NULL)
  OR (NEW.leeway_kind = 'deadline') <> (NEW.leeway_until IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'leeway members must match the leeway kind');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (27, 'commitment_leeway', 'a6b7eaf5369f434d8357cd2a26edbc2a2f8fe810503bab303c7b09753bb0427d', '2026-01-30T03:04:05.000000000Z');

-- Migration 0028 applied to the version 27 contents above.
-- Organisation: categories, labels, buckets and their assignments (O1,
-- ADR-0053, decision D12).
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

-- A plan with a live and a removed adjustment and its history, so the later upgrades are checked against rows.
INSERT INTO deferral_plans (id, occurrence_id, plan_type, planned_date, reason, original_due_date, original_amount, original_currency, outstanding_amount, outstanding_currency, created_at, updated_at, undone_at, version) VALUES ('01JZ0000000000000000000130', '01JZ0000000000000000000110', 'personal', '2026-02-10', NULL, '2026-01-31', '12.50', 'EUR', '12.50', 'EUR', '2026-01-20T04:00:00.000000000Z', '2026-01-21T04:00:00.000000000Z', NULL, 4);
INSERT INTO deferral_plan_adjustments (id, plan_id, kind, amount, currency, note, created_at, removed_at) VALUES ('01JZ0000000000000000000131', '01JZ0000000000000000000130', 'fee', '2.500', 'EUR', NULL, '2026-01-20T04:00:00.000000000Z', NULL);
INSERT INTO deferral_plan_adjustments (id, plan_id, kind, amount, currency, note, created_at, removed_at) VALUES ('01JZ0000000000000000000132', '01JZ0000000000000000000130', 'interest', '1.00', 'EUR', 'removed again', '2026-01-20T04:00:00.000000000Z', '2026-01-21T04:00:00.000000000Z');
INSERT INTO deferral_plan_events (id, plan_id, plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at) VALUES ('01JZ0000000000000000000140', '01JZ0000000000000000000130', 1, 'created', 'personal', '2026-02-10', NULL, NULL, 'system', NULL, 'system', '2026-01-20T04:00:00.000000000Z');
INSERT INTO deferral_plan_events (id, plan_id, plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at) VALUES ('01JZ0000000000000000000141', '01JZ0000000000000000000130', 2, 'adjustment_added', 'personal', '2026-02-10', NULL, '01JZ0000000000000000000131', 'system', NULL, 'system', '2026-01-20T04:00:00.000000000Z');
INSERT INTO deferral_plan_events (id, plan_id, plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at) VALUES ('01JZ0000000000000000000142', '01JZ0000000000000000000130', 3, 'adjustment_added', 'personal', '2026-02-10', NULL, '01JZ0000000000000000000132', 'system', NULL, 'system', '2026-01-20T04:00:00.000000000Z');
INSERT INTO deferral_plan_events (id, plan_id, plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at) VALUES ('01JZ0000000000000000000143', '01JZ0000000000000000000130', 4, 'adjustment_removed', 'personal', '2026-02-10', NULL, '01JZ0000000000000000000132', 'system', NULL, 'system', '2026-01-20T04:00:00.000000000Z');

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (28, 'organisation', 'acf155fdb1388015e9b23242e631c32819b9ed073e8be7bb84c582428481eb18', '2026-01-31T03:04:05.000000000Z');

-- Migration 0029 applied to the version 28 contents above.
-- Deferral plan fees with their own paid marker; a plan the person did not keep
-- is visible (DF1c, ADR-0050, operator decision D-DF 1 and 2).
--
-- A recorded fee or interest adjustment stays planned until it is itself marked
-- paid or removed: paying the principal never settles it, and it is never
-- payment evidence for the occurrence. paid_at is when the person marked it
-- paid, paid_actor_kind, paid_actor_user and paid_via who did and how (D42 3a).
-- Marking paid is reversible: clearing the marker (all four columns NULL
-- again) restores the fee as planned, and every change is one row of
-- deferral_plan_events and one audit event. An adjustment already removed
-- cannot change. Existing adjustments are unpaid.
--
-- A missed plan (planned date passed while the occurrence still owes something)
-- is derived when read and never stored: nothing here records it.
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_at TEXT CHECK (paid_at IS NULL OR (length(paid_at) = 30 AND paid_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_actor_kind TEXT CHECK (paid_actor_kind IS NULL OR paid_actor_kind IN ('user', 'token', 'system'));
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_actor_user TEXT CHECK (paid_actor_user IS NULL OR (length(paid_actor_user) = 26 AND paid_actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'));
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_via TEXT CHECK (paid_via IS NULL OR paid_via IN ('session', 'token', 'mcp', 'system'));

-- The marker is whole or absent, and its actor fits the channel as in the history.
CREATE TRIGGER deferral_plan_adjustments_paid_insert
BEFORE INSERT ON deferral_plan_adjustments
WHEN NEW.paid_at IS NOT NULL OR NEW.paid_actor_kind IS NOT NULL OR NEW.paid_actor_user IS NOT NULL OR NEW.paid_via IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment starts unpaid');
END;

DROP TRIGGER deferral_plan_adjustments_only_removed;

-- Only removal (once) and the paid marker (set or cleared, never on a removed
-- adjustment) can change an adjustment.
CREATE TRIGGER deferral_plan_adjustments_only_removed_or_paid
BEFORE UPDATE ON deferral_plan_adjustments
WHEN OLD.removed_at IS NOT NULL
  OR NEW.id IS NOT OLD.id OR NEW.plan_id IS NOT OLD.plan_id OR NEW.kind IS NOT OLD.kind OR NEW.amount IS NOT OLD.amount
  OR NEW.currency IS NOT OLD.currency OR NEW.note IS NOT OLD.note OR NEW.created_at IS NOT OLD.created_at
  OR (NEW.removed_at IS NOT OLD.removed_at AND (NEW.removed_at IS NULL OR NEW.paid_at IS NOT OLD.paid_at OR NEW.paid_actor_kind IS NOT OLD.paid_actor_kind
      OR NEW.paid_actor_user IS NOT OLD.paid_actor_user OR NEW.paid_via IS NOT OLD.paid_via))
  OR (NEW.paid_at IS NULL) IS NOT (NEW.paid_actor_kind IS NULL)
  OR (NEW.paid_at IS NULL AND (NEW.paid_actor_user IS NOT NULL OR NEW.paid_via IS NOT NULL))
  OR (NEW.paid_via IS NOT NULL AND NOT ((NEW.paid_via = 'session' AND NEW.paid_actor_kind = 'user') OR (NEW.paid_via IN ('token', 'mcp') AND NEW.paid_actor_kind = 'token') OR (NEW.paid_via = 'system' AND NEW.paid_actor_kind = 'system')))
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment can only be removed once or marked paid or unpaid while it is not removed');
END;

-- The history gains adjustment_paid and adjustment_unpaid. The table is
-- rebuilt because SQLite cannot change a CHECK; it is a leaf (nothing refers to
-- it) and its rows are copied unchanged.
CREATE TABLE deferral_plan_events_new (
    id            TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_id       TEXT NOT NULL REFERENCES deferral_plans (id) ON DELETE RESTRICT CHECK (length(plan_id) = 26 AND plan_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_version  INTEGER NOT NULL CHECK (plan_version >= 1),
    action        TEXT NOT NULL CHECK (action IN ('created', 'revised', 'undone', 'adjustment_added', 'adjustment_removed', 'adjustment_paid', 'adjustment_unpaid')),
    plan_type     TEXT NOT NULL CHECK (plan_type IN ('personal', 'creditor_agreed', 'documented_grace')),
    planned_date  TEXT NOT NULL CHECK (length(planned_date) = 10 AND planned_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(planned_date) = planned_date),
    reason        TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    adjustment_id TEXT REFERENCES deferral_plan_adjustments (id) ON DELETE RESTRICT CHECK (adjustment_id IS NULL OR (length(adjustment_id) = 26 AND adjustment_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    actor_user  TEXT CHECK (actor_user IS NULL OR (length(actor_user) = 26 AND actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_via   TEXT CHECK (actor_via IS NULL OR actor_via IN ('session', 'token', 'mcp', 'system')),
    at            TEXT NOT NULL CHECK (length(at) = 30 AND at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK ((adjustment_id IS NOT NULL) = (action IN ('adjustment_added', 'adjustment_removed', 'adjustment_paid', 'adjustment_unpaid'))),
    CHECK (actor_via IS NULL OR (actor_via = 'session' AND actor_kind = 'user') OR (actor_via IN ('token', 'mcp') AND actor_kind = 'token') OR (actor_via = 'system' AND actor_kind = 'system'))
) STRICT;

INSERT INTO deferral_plan_events_new SELECT id, plan_id, plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at FROM deferral_plan_events;

DROP TABLE deferral_plan_events;
ALTER TABLE deferral_plan_events_new RENAME TO deferral_plan_events;

CREATE UNIQUE INDEX deferral_plan_events_order ON deferral_plan_events (plan_id, plan_version);

CREATE TRIGGER deferral_plan_events_append_only
BEFORE UPDATE ON deferral_plan_events
BEGIN
    SELECT RAISE(ABORT, 'deferral plan history is append-only');
END;

CREATE TRIGGER deferral_plan_events_kept
BEFORE DELETE ON deferral_plan_events
BEGIN
    SELECT RAISE(ABORT, 'deferral plan history is never deleted');
END;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (29, 'deferral_fee_paid', 'e7f77758369835051625761e928004b340bbf1f80893ff36ee690e3cde9d9e9e', '2026-02-01T03:04:05.000000000Z');

-- Representative merchant data of version 29 for the merchant-record upgrade
-- (migration 0030, ADR-0058): decisions with names that share a key in
-- different case or Unicode composition, an enrichment answer per status, a
-- rejection, and organisation assignments keyed by merchant name keys (one
-- matching a decision only after NFC, one with no decision at all).
INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, booking_status, provider_status, source_amount, source_currency, transaction_date, raw_description, counterparty_name, imported_at, last_seen_at) VALUES ('01JZ0000000000000000000015', '01JZ0000000000000000000011', 'fake', 'fb:00000000000000000000000000000015#1', 'fallback', 'booked', 'BOOK', '-12.30', 'EUR', '2026-01-20', 'CARD PURCHASE LIDL 1523 VILNIUS', 'LIDL 1523', '2026-02-01T05:00:00.000000000Z', '2026-02-01T05:00:00.000000000Z');
INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, booking_status, provider_status, source_amount, source_currency, transaction_date, raw_description, counterparty_name, imported_at, last_seen_at) VALUES ('01JZ0000000000000000000016', '01JZ0000000000000000000011', 'fake', 'fb:00000000000000000000000000000016#1', 'fallback', 'booked', 'BOOK', '-12.30', 'EUR', '2026-01-20', 'CARD PURCHASE LIDL 0042 KAUNAS', 'Lidl 0042', '2026-02-01T05:00:00.000000000Z', '2026-02-01T05:00:00.000000000Z');
INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, booking_status, provider_status, source_amount, source_currency, transaction_date, raw_description, counterparty_name, imported_at, last_seen_at) VALUES ('01JZ0000000000000000000017', '01JZ0000000000000000000011', 'fake', 'fb:00000000000000000000000000000017#1', 'fallback', 'booked', 'BOOK', '-12.30', 'EUR', '2026-01-20', 'CAFE EXAMPLE 77', '', '2026-02-01T05:00:00.000000000Z', '2026-02-01T05:00:00.000000000Z');
INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, booking_status, provider_status, source_amount, source_currency, transaction_date, raw_description, counterparty_name, imported_at, last_seen_at) VALUES ('01JZ0000000000000000000018', '01JZ0000000000000000000011', 'fake', 'fb:00000000000000000000000000000018#1', 'fallback', 'booked', 'BOOK', '-12.30', 'EUR', '2026-01-20', 'UNKNOWN 1234', '', '2026-02-01T05:00:00.000000000Z', '2026-02-01T05:00:00.000000000Z');
INSERT INTO merchant_enrichments (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at) VALUES ('01JZ0000000000000000000015', 'identified', 'Lidl', '', 'curated', 950, '[]', '2026-02-01T06:00:00.000000000Z', NULL);
INSERT INTO merchant_enrichments (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at) VALUES ('01JZ0000000000000000000016', 'identified', 'Lidl', '', 'local_mapping', 700, '[]', '2026-02-01T06:00:00.000000000Z', NULL);
INSERT INTO merchant_enrichments (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at) VALUES ('01JZ0000000000000000000014', 'identified', 'Hotel Example', '', 'curated', 850, '[]', '2026-02-01T06:00:00.000000000Z', NULL);
INSERT INTO merchant_enrichments (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at) VALUES ('01JZ0000000000000000000017', 'ambiguous', '', '', 'curated', NULL, '["Cafe A","Cafe B"]', '2026-02-01T06:00:00.000000000Z', NULL);
INSERT INTO merchant_enrichments (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at) VALUES ('01JZ0000000000000000000018', 'none', '', '', 'local', NULL, '[]', '2026-02-01T06:00:00.000000000Z', NULL);
INSERT INTO merchant_enrichments (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at) VALUES ('01JZ0000000000000000000013', 'unavailable', '', '', 'remote', NULL, '[]', NULL, '2026-02-01T06:00:00.000000000Z');
INSERT INTO merchant_reviews (transaction_id, decision, name, decided_kind, decided_user, decided_via, decided_at, version) VALUES ('01JZ0000000000000000000015', 'confirmed', 'Lidl', 'user', '01JZ0000000000000000000030', 'session', '2026-02-02T10:00:00.000000000Z', 1);
INSERT INTO merchant_reviews (transaction_id, decision, name, decided_kind, decided_user, decided_via, decided_at, version) VALUES ('01JZ0000000000000000000016', 'corrected', 'LIDL', 'token', '01JZ0000000000000000000030', 'mcp', '2026-02-03T10:00:00.000000000Z', 2);
INSERT INTO merchant_reviews (transaction_id, decision, name, decided_kind, decided_user, decided_via, decided_at, version) VALUES ('01JZ0000000000000000000017', 'corrected', 'Café Example', 'user', '01JZ0000000000000000000030', 'session', '2026-02-04T10:00:00.000000000Z', 1);
INSERT INTO merchant_reviews (transaction_id, decision, name, decided_kind, decided_user, decided_via, decided_at, version) VALUES ('01JZ0000000000000000000013', 'rejected', '', 'user', '01JZ0000000000000000000030', 'session', '2026-02-04T11:00:00.000000000Z', 1);
INSERT INTO categories (id, name, name_key, parent_id, archived_at, created_at, updated_at, version) VALUES ('01JZ0000000000000000000300', 'Groceries', 'groceries', NULL, NULL, '2026-02-01T07:00:00.000000000Z', '2026-02-01T07:00:00.000000000Z', 1);
INSERT INTO categories (id, name, name_key, parent_id, archived_at, created_at, updated_at, version) VALUES ('01JZ0000000000000000000301', 'Eating out', 'eating out', NULL, NULL, '2026-02-01T07:00:00.000000000Z', '2026-02-01T07:00:00.000000000Z', 1);
INSERT INTO labels (id, name, name_key, archived_at, created_at, updated_at, version) VALUES ('01JZ0000000000000000000310', 'Weekly', 'weekly', NULL, '2026-02-01T07:00:00.000000000Z', '2026-02-01T07:00:00.000000000Z', 1);
INSERT INTO category_assignments (object_type, object_id, transaction_id, commitment_id, account_id, category_id, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES ('merchant', 'lidl', NULL, NULL, NULL, '01JZ0000000000000000000300', 'user', '', 'user', '01JZ0000000000000000000030', 'session', '2026-02-05T09:00:00.000000000Z');
INSERT INTO category_assignments (object_type, object_id, transaction_id, commitment_id, account_id, category_id, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES ('merchant', 'café example', NULL, NULL, NULL, '01JZ0000000000000000000301', 'rule', 'rule-cafes', 'system', NULL, 'system', '2026-02-05T10:00:00.000000000Z');
INSERT INTO category_assignments (object_type, object_id, transaction_id, commitment_id, account_id, category_id, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES ('merchant', 'acme shop', NULL, NULL, NULL, NULL, 'user', '', 'user', '01JZ0000000000000000000030', 'session', '2026-02-05T11:00:00.000000000Z');
INSERT INTO category_assignments (object_type, object_id, transaction_id, commitment_id, account_id, category_id, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES ('transaction', '01JZ0000000000000000000018', '01JZ0000000000000000000018', NULL, NULL, '01JZ0000000000000000000300', 'user', '', 'user', '01JZ0000000000000000000030', 'session', '2026-02-05T12:00:00.000000000Z');
INSERT INTO label_assignments (object_type, object_id, transaction_id, commitment_id, account_id, label_id, removed, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES ('merchant', 'lidl', NULL, NULL, NULL, '01JZ0000000000000000000310', 0, 'rule', 'rule-weekly', 'system', NULL, 'system', '2026-02-05T13:00:00.000000000Z');
INSERT INTO label_assignments (object_type, object_id, transaction_id, commitment_id, account_id, label_id, removed, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES ('merchant', 'acme shop', NULL, NULL, NULL, '01JZ0000000000000000000310', 1, 'user', '', 'user', '01JZ0000000000000000000030', 'session', '2026-02-05T14:00:00.000000000Z');

-- ledger:foreign-keys-off
-- Merchant records (task M1, ADR-0058, operator decision D-MER 1a).
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

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (30, 'merchant_records', '2fb9735bc57c35b068d80143220fb6812b38da79e9673168a666112356d00b57', '2026-02-02T03:04:05.000000000Z');

-- Decision providers (task L1, ADR-0060, decision D13): consent per provider
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
-- operator accepted (its version), never a secret. fields is the JSON array of
-- fields the operator consented to send: sending is refused when the configured
-- fields are not all in it (narrowing is fine, widening needs a new consent).
CREATE TABLE decision_consents (
    id              TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    provider        TEXT NOT NULL CHECK (provider IN ('openrouter', 'openai', 'anthropic', 'typesafe')),
    purpose         TEXT NOT NULL CHECK (purpose IN ('categorisation', 'label_suggestion', 'alerts', 'payment_matching')),
    terms           TEXT NOT NULL CHECK (length(terms) BETWEEN 1 AND 200),
    fields          TEXT NOT NULL CHECK (json_valid(fields) AND json_type(fields) = 'array' AND json_array_length(fields) > 0),
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
  OR NEW.id IS NOT OLD.id OR NEW.provider IS NOT OLD.provider OR NEW.purpose IS NOT OLD.purpose OR NEW.terms IS NOT OLD.terms OR NEW.fields IS NOT OLD.fields
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

INSERT INTO decision_consents (id, provider, purpose, terms, fields, granted_by_kind, granted_by_user, granted_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZC01', 'openai', 'categorisation', 'terms-v1', '["description","merchant_name","candidate_names"]', 'user', NULL, '2026-02-03T03:04:05.000000000Z');
INSERT INTO decision_usage (task, period, attempts, updated_at) VALUES ('categorisation', '2026-02', 3, '2026-02-03T03:04:05.000000000Z');
INSERT INTO decision_usage (task, period, attempts, updated_at) VALUES ('label_suggestion', '2026-02', 1, '2026-02-03T03:04:05.000000000Z');
INSERT INTO decision_suggestions (id, task, provider, model, response_id, object_type, object_id, decision, auto_apply, created_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZC02', 'categorisation', 'openai', 'm1', NULL, 'transaction', '01JD3Y4C8V9M2Q7W5R6T0XZC03', '{"choice":"01JD3Y4C8V9M2Q7W5R6T0XZC04","confidence":"0.8"}', 1, '2026-02-03T03:04:05.000000000Z');
UPDATE decision_suggestions SET state = 'accepted', decided_at = '2026-02-03T03:05:05.000000000Z', version = 2 WHERE id = '01JD3Y4C8V9M2Q7W5R6T0XZC02';
INSERT INTO decision_suggestions (id, task, provider, model, response_id, object_type, object_id, decision, auto_apply, created_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZC05', 'categorisation', 'openai', 'm1', 'resp-1', 'transaction', '01JD3Y4C8V9M2Q7W5R6T0XZC06', '{"choice":null,"confidence":"0.2"}', 0, '2026-02-03T03:04:05.000000000Z');
UPDATE decision_suggestions SET state = 'rejected', decided_at = '2026-02-03T04:04:05.000000000Z', version = 2 WHERE id = '01JD3Y4C8V9M2Q7W5R6T0XZC05';
INSERT INTO decision_suggestions (id, task, provider, model, response_id, object_type, object_id, decision, auto_apply, created_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZC07', 'label_suggestion', 'openai', 'm1', NULL, 'transaction', '01JD3Y4C8V9M2Q7W5R6T0XZC03', '{"choices":["01JD3Y4C8V9M2Q7W5R6T0XZC08"],"confidence":"0.6"}', 0, '2026-02-03T03:04:05.000000000Z');
INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (31, 'decision_providers', '05bfc900a4b71b9c5f1dccb6804e7801494800541057a2c0b4168d25108a858a', '2026-02-03T03:04:05.000000000Z');
