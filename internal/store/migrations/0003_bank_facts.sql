-- Bank facts (B1, ADR-0013): what banking-data providers report, normalized.
-- These tables hold provider-owned facts only. User data (corrections,
-- categories, payment matches, manual payments) lives in other tables that
-- refer to these with ON DELETE RESTRICT, never CASCADE or SET NULL, so a
-- bank fact cannot disappear from under a user decision. Bank facts are
-- deleted only by the explicit purge path (bankfacts.Store.PurgeConnection).
-- Account identifiers, provider handles and session references are
-- sensitive: stored for the owner, never logged.

-- A bank link: one institution through one configured provider application.
-- Mode and account access are recorded at creation (decision D10). Ledger
-- manages these rows, so they carry a version for optimistic concurrency.
CREATE TABLE bank_connections (
    id                  TEXT PRIMARY KEY CHECK (length(id) = 26),
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
    id                    TEXT PRIMARY KEY CHECK (length(id) = 26),
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

CREATE INDEX bank_accounts_connection ON bank_accounts (connection_id);

-- Balance observations, append-only. obtained_at is when Ledger fetched the
-- figure (freshness); reference_date is the date the bank says it applies
-- to. An unknown balance is no row, never a zero.
CREATE TABLE bank_balance_snapshots (
    id             TEXT PRIMARY KEY CHECK (length(id) = 26),
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
    id                      TEXT PRIMARY KEY CHECK (length(id) = 26),
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
