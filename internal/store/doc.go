// Package store owns persistence: SQLite connection setup (WAL, busy timeout,
// foreign keys), explicit forward-only migrations and transaction helpers.
//
// It holds no business rules and no HTTP types. Domain packages declare the
// storage operations they need; this package implements them. See ADR-0001
// for the boundaries and ADR-0003 for the driver and the migration rules.
//
// # Connections
//
// [Open] opens two pools on one database file. The writer pool has exactly one
// connection and begins every transaction with BEGIN IMMEDIATE, so writes in
// this process are serialised in Go rather than by SQLite lock retries. The
// reader pool has several query-only connections; WAL lets them read a
// consistent snapshot while a write is in progress. Writes go through
// [DB.WriteTx], reads through [DB.ReadTx]. Never call WriteTx from inside a
// WriteTx function: the writer pool has one connection, so the inner call
// waits until its context ends.
//
// # Migrations
//
// Migrations are numbered SQL files in migrations/, embedded in the binary
// (NNNN_snake_case_name.sql, contiguous from 0001). They only move forward:
// there are no down migrations, a database written by a newer binary is
// refused, and an applied migration whose file changed is refused. Each
// migration runs in its own transaction together with the row that records
// it in schema_migrations, and PRAGMA foreign_key_check must be clean before
// it commits. A migration that rebuilds a table starts with the line
//
//	-- ledger:foreign-keys-off
//
// and then runs with foreign key enforcement off (SQLite ignores that pragma
// inside a transaction, and dropping a parent table with enforcement on would
// cascade); the foreign key check still has to pass before commit.
//
// # Schema conventions
//
// Tables are STRICT. Timestamps are TEXT columns named *_at, written with
// [FormatTime] or [Timestamp] as fixed-width UTC (2006-01-02T15:04:05.000000000Z),
// and guarded by a CHECK constraint (see [TimestampCheck]). Tests enforce both.
// Money is not stored here yet: decision D5 requires an exact decimal with a
// stored scale, and that encoding belongs to the money primitives task.
package store
