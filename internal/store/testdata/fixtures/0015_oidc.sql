-- Fixture: a database at schema version 15 (the version 14 contents plus the
-- single-sign-on tables of migration 0015), holding every row of the version
-- 14 fixture and one OIDC link of the owner. Upgrading it must apply
-- migration 0016 (device cookies) and keep every row. All identifiers are
-- synthetic.
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
