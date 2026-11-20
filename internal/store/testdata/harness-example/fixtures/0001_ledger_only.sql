-- Synthetic fixture: a database at schema version 1 of the harness example
-- migrations (only the migration ledger). No real data.
-- The migration ledger. The migration runner reads and writes this table; its
-- columns are part of the runner's contract, so change them only through a
-- later migration together with the runner.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT NOT NULL CHECK (name <> ''),
    checksum   TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL CHECK (length(applied_at) = 30 AND applied_at GLOB '????-??-??T??:??:??.?????????Z')
) STRICT;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (1, 'schema_migrations', 'fba0921a4879c6128a79564939f9c0e54e70f3acd21ed3535dd232041bd61cce', '2026-01-02T03:04:05.000000000Z');
