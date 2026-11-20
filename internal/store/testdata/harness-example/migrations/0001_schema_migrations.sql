-- The migration ledger. The migration runner reads and writes this table; its
-- columns are part of the runner's contract, so change them only through a
-- later migration together with the runner.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT NOT NULL CHECK (name <> ''),
    checksum   TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL CHECK (length(applied_at) = 30 AND applied_at GLOB '????-??-??T??:??:??.?????????Z')
) STRICT;
