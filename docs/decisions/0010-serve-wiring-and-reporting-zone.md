# ADR-0010: ledger serve wiring and reporting-zone capture

- Status: accepted
- Date: 2026-11-20

Numbering: 0008 and 0009 are reserved for pending ADRs, so this one is 0010.

## Context

Foundation pieces (configuration, store, HTTP server, calendar) existed separately. `ledger serve` must connect them, and decision D5 requires the reporting zone to be captured once and persisted.

## Decision

- **Startup order.** load config, logger, create the database directory (0700), `store.Open` (migrations, automatic pre-migration backup), resolve the reporting zone, bind the port, serve. A store or zone failure exits 1 before the port is bound. The store is closed after the server has drained, so in-flight requests never see a closed database.
- **File mode.** `store.Open` creates a new database file with mode 0600 whatever the umask; an existing file keeps its mode. SQLite creates `-wal` and `-shm` itself with the database file's permissions, so they follow, and the directory is owner-only. If another umask-sensitive tool creates the directory, keep it owner-only.
- **Settings table** (migration 0002): `settings(key, value, updated_at)`, STRICT, UTC timestamp check. The zone is the row `reporting.timezone`.
- **Resolution** (`internal/reporting`): explicit `reporting.timezone`, else the stored zone, else (first start only) the host IANA name from `calendar.HostZoneName`; none resolves means startup is refused with a message naming `LEDGER_REPORTING_TIMEZONE`. `Local` is rejected. The read and write share one write transaction. An explicit value that differs from the stored one **replaces** the stored value (so removing the setting later does not silently revert) and is logged at warn level with `event=reporting_timezone_changed`, `from` and `to`. A stored value that no longer loads is never overwritten by the host zone; only an explicit value replaces it.
- **Service.** `reporting.Service` exposes `Zone()`, `Name()` and `Source()`; it is fixed for the process lifetime, so a changed zone takes effect after a restart.
- **Readiness.** `GET /readyz` runs the registered checks (D17: only the database, via `DB.Ping`), with a 2 s bound; 503 lists failing check names, never error text.

## Consequences

- TODO(audit-log task): the zone change is only a log line until the audit log exists.
- F4 (health and metrics) adds its own checker list; this change registers a minimal `httpapi.ReadyHandler` that F4 should replace or absorb when merging, keeping the database check.
- A database that holds a zone but is opened by an older binary is refused by the migration runner (schema newer), so the zone cannot be lost by downgrading.
