-- Synthetic fixture: a database at schema version 2 of the harness example
-- migrations (two accounts, three entries). No real data.
-- The migration ledger. The migration runner reads and writes this table; its
-- columns are part of the runner's contract, so change them only through a
-- later migration together with the runner.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT NOT NULL CHECK (name <> ''),
    checksum   TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL CHECK (length(applied_at) = 30 AND applied_at GLOB '????-??-??T??:??:??.?????????Z')
) STRICT;

CREATE TABLE accounts (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '????-??-??T??:??:??.?????????Z')
) STRICT;

CREATE TABLE entries (
    id         INTEGER PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    memo       TEXT NOT NULL
) STRICT;

CREATE INDEX entries_account_id ON entries (account_id);

INSERT INTO accounts (id, name, created_at) VALUES
    (1, 'Everyday', '2026-01-02T03:04:05.000000000Z'),
    (2, 'Savings',  '2026-02-03T04:05:06.123456789Z');
INSERT INTO entries (id, account_id, memo) VALUES
    (1, 1, 'rent'),
    (2, 1, 'domain renewal'),
    (3, 2, 'transfer');

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (1, 'schema_migrations', 'fba0921a4879c6128a79564939f9c0e54e70f3acd21ed3535dd232041bd61cce', '2026-01-02T03:04:05.000000000Z');
INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (2, 'accounts', '6544ad79b3c61cfdab386a7e6f4bdc67d9a423bbcfde22b41e4aee81cf5c21e2', '2026-01-02T03:04:05.500000000Z');
