# Backup, restore and upgrade recovery

Ledger keeps everything in one SQLite file (`database.path`, default `data/ledger.db`). This page is the operator procedure; the reasoning is in [ADR-0019](decisions/0019-backup-restore-and-instance-lock.md).

All commands read the same configuration as `ledger serve` (flags, `LEDGER_*` variables, config file, `.env`), so they find the same database.

## Back up

```sh
ledger backup                         # writes <database>.backup-<UTC time>.db
ledger backup --output /safe/place/ledger-2026-11-20.db
```

- The file is flushed to disk (`fsync`) and its directory synced before success is reported; a sync error fails the command.
- Safe while `ledger serve` is running: it copies one consistent snapshot (`VACUUM INTO`) and does not block writes.
- The file is created with `O_EXCL` and mode `0600`: an existing file is never overwritten, and the copy is owner-only whatever the umask.
- The copy is verified before the command reports success (`integrity_check`, `foreign_key_check`, migration ledger). A failed or unverifiable backup is deleted, not left behind.
- A backup holds the same financial data as the database. Store it somewhere with the same protection, and encrypt it before it leaves the machine: Ledger does not encrypt backups.
- Backups are not scheduled by Ledger: run `ledger backup` from cron or a systemd timer and copy the result off the machine.

## Automatic backup before a migration

When `ledger serve` starts a binary whose migrations are newer than the database, it first writes `<database>.pre-vNNNN-<UTC time>.bak` (NNNN is the schema version being left; mode `0600`). If that backup fails, the migrations do not run and startup fails. A new database is not backed up (there is nothing to lose).

These files are never deleted automatically. Remove old ones with:

```sh
ledger backup prune --keep 3          # keeps the 3 newest .pre-v*.bak files; touches nothing else
```

## Restore

1. Stop `ledger serve`. Restore refuses to run while a Ledger process holds the instance lock.
2. Restore:

   ```sh
   ledger restore --force /path/to/backup.db     # flags first, then the file
   ```

   The backup is verified, copied to a private staging file next to the database and verified again. Only then is the current database moved aside to `<database>.pre-restore-<UTC time>` (with its `-wal`/`-shm` files, so stale WAL frames cannot be replayed into the restored data) and the copy renamed into place. Any `-wal`/`-shm` file next to the database path is moved aside too, even when there is no database file, so leftovers can never be replayed into the restored data. If anything fails before that point the current database is untouched; if a move or the final rename fails afterwards, restore tries to put the previous files back and the error names the moved-aside path. Without `--force` restore refuses to replace an existing database. The moved-aside database is never deleted by Ledger: delete it yourself once you are satisfied.
3. Start `ledger serve`. If the backup is from an older version, the pending migrations are applied (with an automatic pre-migration backup). A backup from a **newer** binary than the one you run is refused (`schema is newer than this binary`): use the newer binary.

A backup may have its own `-wal` file (a `.pre-restore-*` file that was a live database keeps its WAL next to it as `.pre-restore-*-wal`). Keep the two together: when `<backup>-wal` exists and is not empty, restore (and verification) copies both to a private temporary directory, lets SQLite replay the WAL there and restores that result, so data that was only in the WAL is not lost. The original files are never modified.

What a restore does not do: it does not merge, it replaces the whole database. Anything written after the backup is gone from the active database (it stays in the moved-aside file). Data freshness after a restore is that of the backup: bank data shows its original "last updated" times and is refreshed by the next sync.

## Upgrade recovery

Upgrading means starting a newer binary on an existing database. Migrations only go forward (there are no down migrations), so recovery always means restoring a copy.

**Before upgrading:** run `ledger backup` yourself (the automatic pre-migration backup is a second line of defence, not a replacement) and keep the old binary.

**If startup fails with a migration error** (for example `migration 0005_...: ...`): each migration runs in its own transaction, so the failed one is rolled back and the database is still at the previous schema version and valid. The pre-migration backup is next to it. Options, in order:

1. Run the old binary again on the same database: it works, nothing was applied that it does not know.
2. If a *previous* migration of this upgrade already committed and you want the old binary back, it will refuse the database (`schema is newer than this binary`). Restore the pre-migration backup: `ledger restore --force <database>.pre-vNNNN-<time>.bak` (with the old binary), then start the old binary.
3. Report the migration error (it names the migration and the SQLite error; it contains no row data).

**If the upgrade succeeded but the new version misbehaves:** stop it, restore the pre-migration backup with the **old** binary, start the old binary. Data written since the upgrade is not in the backup; it stays in the `.pre-restore-*` file.

**Other startup refusals and what they mean:**

| Message | Meaning | Action |
|---|---|---|
| `schema is newer than this binary` | The database was migrated by a newer Ledger. | Use that newer binary, or restore an older backup. |
| `applied migration has changed` | A migration file differs from the one applied (modified build or database). | Use the matching binary; do not edit the database. |
| `applied migrations are out of order` | The migration ledger is not contiguous from 0001. | Restore a backup. |
| `database has tables but no schema_migrations` | Not a Ledger database. | Check `database.path`. |
| `another Ledger process is using this database` | Another `serve` or `restore` holds `<database>.lock`. | Stop it. The lock is released by the kernel when its process exits; a leftover `.lock` file is harmless. |

## File permissions

- `ledger serve` creates the database directory with mode `0700` and the database, lock file and backups with `0600`. SQLite gives the `-wal` and `-shm` files the database's mode.
- At startup, `ledger serve` logs a warning (`database file permissions`) for any database, WAL, lock, pre-migration backup, `ledger backup` output or `.pre-restore-*` file (and their WAL/SHM files) that group or others can access, and for a database directory that others can write to. It does not change modes itself. An existing file keeps its mode, so a database created by hand with a loose umask needs `chmod 600`.
- Mode bits are not meaningful on Windows; no check is made there (release targets are Linux and macOS).

## Limits

- One Ledger process per database, enforced with an advisory `flock` on `<database>.lock` on Unix. It stops Ledger processes only, not other programs, and is not checked on platforms without `flock`.
- `ledger backup` does not require stopping the server; `ledger restore` does.
- Restoring a backup taken on a network file system, or across file systems, is not tested; keep the backup and the database on local disks, or copy the backup locally first.
- External tools (Litestream and similar) were the rejected alternative in D17; nothing here prevents running one, but it is not tested or supported.
