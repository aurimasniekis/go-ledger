# ADR-0003: SQLite driver, connection discipline and migrations

Status: Accepted (2026-11-20)

## Context

Ledger stores everything in one SQLite file. The stack requirements ask for explicit forward-only migrations, enforced foreign keys, transactions for related changes, WAL, a busy timeout, UTC timestamps, and a pure-Go driver so the binary builds without CGO. Two pure-Go drivers were candidates: `modernc.org/sqlite` and `github.com/ncruces/go-sqlite3`. This ADR also answers research item R10 (licence, WAL, busy timeout, online backup and `VACUUM INTO`).

Schema decisions are expensive to undo once a database exists on someone's machine, so the migration rules are fixed here too.

## Decision

1. **Driver: `modernc.org/sqlite` v1.62.0** (SQLite 3.54.1), registered as `database/sql` driver `"sqlite"`, imported only by `internal/store`.
2. **Two pools on one file.** The writer pool has exactly one connection and begins transactions with `BEGIN IMMEDIATE` (`_txlock=immediate`), so writes in the process queue in Go instead of failing with `SQLITE_BUSY`. The reader pool (default 4) is `query_only` and uses deferred, read-only transactions, which in WAL see a consistent snapshot without blocking the writer. Code writes through `DB.WriteTx` and reads through `DB.ReadTx`; `WriteTx` must not nest.
3. **Every connection**: `busy_timeout` (default 5 s, applied first by the driver), `foreign_keys=ON`, `synchronous=FULL`; the writer also sets `journal_mode=WAL`. `Open` reads the pragmas back on a writer and a reader connection and fails if any differs (for example on a file system without WAL support). `synchronous=FULL` is chosen over `NORMAL`: with WAL, `NORMAL` can lose the last commits on power loss, and the cost of `FULL` is negligible for one user's writes.
4. **Paths only.** `Open` takes a file path (no `:memory:`, no `file:` URI, no `?` or `#`), because both pools must reach the same file. It takes the path as a parameter and does not read configuration; the configuration task supplies it.
5. **Migrations** are files `internal/store/migrations/NNNN_snake_case_name.sql`, embedded with `go:embed`, numbered from 0001 without gaps. Loading fails on a gap, duplicate, bad name or empty file.
   - Forward only: there are no down migrations. A database at a version the binary does not know is refused (`ErrDatabaseNewer`); applied versions that are not a contiguous run from 0001 are refused (`ErrOutOfOrder`); an applied migration whose name or SHA-256 checksum differs from the binary's is refused (`ErrChecksumMismatch`). A file with tables but no `schema_migrations` is refused (`ErrUnversioned`).
   - The version table is `schema_migrations(version, name, checksum, applied_at)`, created by migration 0001 itself. Each migration runs in one `BEGIN IMMEDIATE` transaction together with its `schema_migrations` row. Under the lock the runner re-reads the applied set, so concurrent starts apply each migration once.
   - `PRAGMA foreign_key_check` must be clean before each migration commits (`ErrForeignKeyViolation`).
   - A migration whose first line is `-- ledger:foreign-keys-off` runs with enforcement off, for SQLite's 12-step table rebuild: with enforcement on, `DROP TABLE` of a parent cascades. Migrations run on a dedicated connection that is closed afterwards, so no pooled connection is left with enforcement off.
   - Checksums are taken with CRLF read as LF, and `.gitattributes` keeps `*.sql` LF.
   - **Squashing applied migrations is allowed only before the first release and only through a new ADR**, since it changes checksums that existing databases hold.
6. **Schema conventions**, enforced by tests on the migrated schema: every table is `STRICT`; every timestamp column is named `*_at`, is `TEXT` and carries `CHECK (<store.TimestampCheck(col)>)`. Timestamps are written with `store.FormatTime` or `store.Timestamp` as `2006-01-02T15:04:05.000000000Z`: UTC, fixed width (text order is time order), nanoseconds (round trips are exact). The CHECK rejects the driver's own encoding of a bare `time.Time` and any non-Z offset.
7. **Money is not stored by this task.** Decision D5 requires an exact decimal with a stored scale that keeps the source amount exact; the money primitives task (F9) defines that encoding and its columns.
8. **Migration test harness**: `internal/store/testdata/fixtures/NNNN_description.sql` recreate a database exactly as an earlier binary left it at version NNNN, with synthetic data. The harness loads each into a fresh file without the runner, opens it with the current migrations, and checks the version, `integrity_check`, `foreign_key_check`, that history rows are preserved, plus per-fixture data checks. A coverage test requires a fixture for every version below the latest. `testdata/harness-example` exercises the harness on a synthetic three-step schema with data and a foreign-keys-off rebuild. It is an ordinary `go test`, so it runs wherever the Go tests run, including CI.

## Why

| | modernc.org/sqlite | ncruces/go-sqlite3 |
|---|---|---|
| Licence | BSD-3-Clause (all linked modules BSD-3 or MIT, see below) | MIT |
| How | SQLite C translated to Go | SQLite compiled to WASM, run by wazero |
| WAL | Yes, standard unix/windows VFS shared memory | Yes on Linux/macOS/Windows (mmap / MapViewOfFile, OFD locks need Linux 3.15+); elsewhere WAL needs `EXCLUSIVE` locking and one connection |
| Busy timeout | `_busy_timeout` DSN key, applied first | `_pragma=busy_timeout(...)` |
| Online backup | `conn.NewBackup` / `NewRestore` via `sql.Conn.Raw` | `Conn.Backup` |
| `VACUUM INTO` | Yes (SQL, tested here) | Yes (SQL) |
| `_txlock=immediate` | Yes | Yes |

Both meet the requirements. modernc.org/sqlite was chosen because it is the most widely used pure-Go driver, it uses SQLite's own VFS on every supported platform (so WAL and locking behave like the C library, without per-platform caveats), and it adds no WASM runtime. Its costs: a large module and slower compilation, and SQLite upgrades arrive only when its maintainer regenerates the translation. ncruces/go-sqlite3 remains a reasonable fallback: the store hides the driver behind `database/sql`, so a switch would touch `store.go` and the DSN, not callers.

Sources checked (2026-11-20): the module source of modernc.org/sqlite v1.61.1/v1.62.0 (`LICENSE`, DSN handling in `sqlite.go`, `tx.go`, `backup.go`) and of ncruces/go-sqlite3 v0.32.0 (`LICENSE`, `driver/driver.go`, `vfs/README.md`); latest versions from the Go module proxy (modernc v1.62.0 and ncruces v0.37.0, both 2026-11-07). Behaviour claimed for modernc (WAL, busy timeout, FK enforcement, `query_only`, `VACUUM INTO`) is covered by tests in `internal/store`; the online backup API was read in source, not exercised.

Modules linked into `internal/store` and their licences (from each module's licence file): modernc.org/sqlite, libc, mathutil, memory (BSD-3-Clause); golang.org/x/sys (BSD-3-Clause); github.com/google/uuid (BSD-3-Clause); github.com/remyoudompheng/bigfft (BSD-3-Clause); github.com/dustin/go-humanize (MIT).

Rejected: `mattn/go-sqlite3` (needs CGO); an ORM or a migration library (golang-migrate, goose): the runner is about 300 lines, and owning it lets it enforce checksums, the version-table contract, the foreign key check and the foreign-keys-off rebuild exactly as above.

## Consequences

- `make check` now also runs `test-nocgo` (the store tests with `CGO_ENABLED=0`) and `CGO_ENABLED=0 go build ./...`, so the pure-Go build covers the driver. ADR-0002's "no required dependencies" no longer holds.
- BSD-3-Clause and MIT require their notices to accompany binary distributions. No `NOTICE` file is needed (none of these is Apache-2.0 with a NOTICE), but the release task (F10) must ship the third-party licence texts with binaries and images.
- Every migration that adds a version must add a fixture for the previous version; the coverage test enforces it.
- Write throughput is one transaction at a time per process, by design. A long `WriteTx` blocks other writers; keep provider calls and other slow work outside it.
- Open: busy-timeout and retry limits beyond the 5 s default, and backup/restore commands, depend on unresolved operational decisions; `DB.WriteTx` does not retry `SQLITE_BUSY` from another process. `Open` retries only while opening connections, within the busy timeout, because switching a new file to WAL can report busy without consulting the busy handler.
