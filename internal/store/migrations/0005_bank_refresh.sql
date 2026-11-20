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
