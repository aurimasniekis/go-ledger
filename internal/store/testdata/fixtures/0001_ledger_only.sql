-- Fixture: a database at schema version 1 (only the migration ledger), as the
-- first released binary left it. Upgrading it must apply migration 0002.
-- The migration ledger. The migration runner reads and writes this table; its
-- columns are part of the runner's contract, so change them only through a
-- later migration together with the runner.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT NOT NULL CHECK (name <> ''),
    checksum   TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL CHECK (length(applied_at) = 30 AND applied_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (1, 'schema_migrations', '25048313703a8dafc7f42d4f2c330ed308d20076538c0b8879f6c0f271bed0e7', '2026-01-02T03:04:05.000000000Z');
