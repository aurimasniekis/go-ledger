-- ledger:foreign-keys-off
-- Rebuilds accounts to add a NOT NULL column with a CHECK, the SQLite
-- 12-step way. With foreign keys on, DROP TABLE accounts would cascade
-- and delete every entry.
CREATE TABLE accounts_new (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    currency   TEXT NOT NULL DEFAULT 'EUR' CHECK (length(currency) = 3),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '????-??-??T??:??:??.?????????Z')
) STRICT;

INSERT INTO accounts_new (id, name, created_at) SELECT id, name, created_at FROM accounts;
DROP TABLE accounts;
ALTER TABLE accounts_new RENAME TO accounts;
