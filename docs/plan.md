# Ledger plan (living)

Short, in-repo view of the initial build. Updated when a task changes state. The full plan, with dependencies and open decisions, is kept outside this repository.

Last updated: 2026-11-20.

## Done

- **F1 Repository skeleton and conventions**: module, `cmd/ledger`, internal package layout (ADR-0001), Apache-2.0 licence, `.gitignore`, golangci-lint v2 config, Makefile, CONTRIBUTING, ADR log (ADR-0002 conventions).

## In review

- **F5 SQLite store and migrations**: `modernc.org/sqlite` (ADR-0003), one writer and query-only readers, WAL, foreign keys, busy timeout, embedded forward-only migrations with checksums, UTC timestamp convention, migration fixture harness. Not yet wired into `cmd/ledger` (needs the configuration task's database path).
- **F9 Money and time primitives**: exact `money.Decimal`/`Money`/`Totals` (per-currency, mixed currencies rejected), half-even rounding at presentation only, `calendar.Date`/`YearMonth`/`Zone`, exact TEXT encoding for SQLite (`store/sqlvalue`), float ban test (ADR-0005).
  - Follow-up: the float ban (`internal/domain/money/nofloat_test.go`) covers only the money, calendar and sqlvalue packages. Add the commitments, matching, outlook and FX packages to `moneyPackages` as they land (or move the check repo-wide).
  - Follow-up (separate wiring task): persist the reporting zone captured on first start, per the control default in decisions.md; F2's `reporting.timezone` description already states it.

## Next (first wave)

- **F2 Configuration system**: YAML, environment and `.env` configuration, `.env.example`.
- **F6 CI validation workflow**: pinned actions, read-only permissions, Go and frontend checks, `govulncheck`.
- **F7 Frontend skeleton and embedding**: React, TypeScript, Vite, embedded into `internal/web`.

## Decisions

The operator decision record is kept outside this repository. Its status:

- **Resolved: D1-D10.** D1 guided-flow questions are covered by the supplied requirements. D2 env prefix `LEDGER_`, token prefix `ldgr_`. D3 preset snapshots supplied (see below). D4 full multi-user with administrator and read-only roles. D5 any ISO 4217 currency, decimal storage with stored scale, half-even rounding at presentation and conversion, one configured reporting timezone, per-currency totals first. D6 monthly, every-N-months and annual rules, month-end clamp, Feb 29 to Feb 28, edits apply forward and paid history is immutable. D7 derived payment status with a manual override, overdue counted from the due date with grace annotated. D8 pending transactions only suggest, reversals reopen occurrences, allocation table, no hard deletes, data kept indefinitely with an explicit purge command. D9 auto-confirm only under the listed safeguards, model output only suggests. D10 Enable Banking public documentation is the source; the mode choice follows the documentation review (R1).
- **Open: D11-D20.** Each blocks only the tasks it names.

## Open points that affect this work

- **D3 follow-up.** The preset snapshots of 2026-11-20 are current requirement-block texts, not certified exports of the original templates. F1 uses them: no per-file licence header, and a `NOTICE` file only if a dependency requires one. F6, F8, F10 and U2 still need revising against them before final acceptance.
