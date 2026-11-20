# ADR-0019: Backup, restore, migration recovery and the instance lock

Status: Accepted (2026-11-20)

## Context

Decision D17 chose `ledger backup` using SQLite `VACUUM INTO` / online backup plus an automatic pre-migration backup (the latter already exists in `store.Open`, ADR-0003). Migrations are forward-only, so every recovery from a bad upgrade is "restore a copy". ADR-0016 left "one Ledger process per database" as a consequence to list. The task (OP1) asks for a tested restore and a documented upgrade-recovery procedure.

## Decision

1. **Backup** (`store.Backup`, `ledger backup [--output FILE]`): `VACUUM INTO` on a connection to the live database, which reads one WAL snapshot and does not block writers or require stopping the server. `VACUUM INTO` rather than the driver's online-backup API: it is plain SQL, already used and tested for the pre-migration backup, produces a compacted copy and needs one code path. (SQLite refuses it on a query-only connection, so the backup connection is an ordinary one that never writes to the database.) The destination is created first with `O_EXCL` and mode 0600 and removed again on failure, so a backup is never world-readable even for an instant and never overwrites anything; the automatic pre-migration backup uses the same helper (it used to chmod after the copy). The result is verified (`integrity_check`, `foreign_key_check`, migration ledger a valid prefix of this binary's migrations) before success is reported; an unverifiable backup is deleted. The command never creates or migrates the database.
2. **Restore** (`store.Restore`, `ledger restore [--force] FILE`): verify the backup; copy it into a private staging file next to the database (same file system, so the final rename is atomic) and verify the copy; then move the existing database and its `-wal`/`-shm` aside to `<db>.pre-restore-<UTC>` and rename the copy into place. Nothing is deleted. An existing database is replaced only with `--force`. A failure before the move leaves the existing database untouched. Restoring a backup from an older schema is allowed and is migrated on the next start (with its own pre-migration backup); a backup from a newer schema is refused, as `Open` refuses such a database. Restore is a whole-database replacement, not a merge.
3. **Instance lock** (`store.LockInstance`): an exclusive non-blocking `flock` on `<db>.lock` (0600, build tag `unix`; a no-op elsewhere, since all release targets are Unix, D19). `ledger serve` and `ledger restore` take it; `ledger backup` does not (it is safe online). The kernel releases it on any process exit, so there is no stale-lock cleanup. It is deliberately not inside `store.Open`: tests and tools open the same file several times in one process by design. It is advisory: it stops two Ledger processes, not other programs, and it can be unreliable on network file systems.
4. **Retention** of automatic pre-migration backups (left to OP1 by ADR-0003): never automatic, because deleting the only copy of financial data without being asked is worse than a full disk. `ledger backup prune --keep N` deletes older `.pre-v*.bak` files only; it never touches `ledger backup` output, restore leftovers or the database.
5. **File modes**: the database directory is created 0700 and the database, lock and backups are 0600 (already so for the database). `ledger serve` logs warnings for files or the directory that others can access; it does not change modes, because silently changing an operator's permissions is surprising.
6. Procedure and failure table: [docs/backup-restore.md](../backup-restore.md).

## Consequences

- Tests (offline, in `internal/store` and `cmd/ledger`): live backup excludes later writes; O_EXCL/0600 behaviour; refusals (exists, same file, missing database, garbage); verification rejects garbage, plain SQLite, newer and tampered ledgers; restore round trip including moved-aside WAL and an untouched database on failure; a failed upgrade rolls back, leaves a pre-migration backup, restore returns the old data and the fixed binary then upgrades it; instance lock; prune; mode warnings; the CLI commands; a second `serve` is refused.
- Backups are not encrypted and not scheduled. Both are operator concerns, documented.
- The lock file stays on disk after exit; this is harmless and documented.
- Not verified: behaviour on network file systems, Windows, and very large databases (`VACUUM INTO` needs free space about the size of the live data). Backup while a long write transaction is open waits up to `database.busy_timeout`.
- Superseded nothing; ADR-0003's "retention belongs to OP1" is resolved here.
