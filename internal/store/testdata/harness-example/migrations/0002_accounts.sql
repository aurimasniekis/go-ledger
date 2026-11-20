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
