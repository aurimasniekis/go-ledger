-- Fixture: a database at schema version 6 (the version 5 contents plus the
-- users table), holding one administrator and one disabled read-only user in
-- addition to the settings, outbox and bank data of the version 5 fixture.
-- Upgrading it must apply migrations 0007 (currency-rate cache) and 0008
-- (audit log) and keep every user row.
-- All identifiers are synthetic.
-- The migration ledger. The migration runner reads and writes this table; its
-- columns are part of the runner's contract, so change them only through a
-- later migration together with the runner.
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
