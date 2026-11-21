# Ledger plan (living)

Short, in-repo view of the initial build. Updated when a task changes state. The full plan, with dependencies and open decisions, is kept outside this repository.

Last updated: 2026-11-20.

## Done

- **F1 Repository skeleton and conventions**: module, `cmd/ledger`, internal package layout (ADR-0001), Apache-2.0 licence, `.gitignore`, golangci-lint v2 config, Makefile, CONTRIBUTING, ADR log (ADR-0002 conventions).

## In review

- **M1 API conventions and OpenAPI** (ADR-0011): `/api/v1`, ULID IDs, cursor pagination and filters, mandatory optimistic concurrency (version/ETag, If-Match or body version, 409 with reconciliation, 428 without a version), JSON 404/405 for API routes, hand-written OpenAPI served as JSON and YAML, route/document contract test, authentication middleware hook, and an internal example resource (`internal/apiexample`, not served).

- **F5 SQLite store and migrations**: `modernc.org/sqlite` (ADR-0003), one writer and query-only readers, WAL, foreign keys, busy timeout, embedded forward-only migrations with checksums, UTC timestamp convention, migration fixture harness. Not yet wired into `cmd/ledger` (needs the configuration task's database path).
- **F9 Money and time primitives**: exact `money.Decimal`/`Money`/`Totals` (per-currency, mixed currencies rejected), half-even rounding at presentation only, `calendar.Date`/`YearMonth`/`Zone`, exact TEXT encoding for SQLite (`store/sqlvalue`), float ban test (ADR-0005).
  - Follow-up: the float ban (`internal/domain/money/nofloat_test.go`) covers only the money, calendar and sqlvalue packages. Add the commitments, matching, outlook and FX packages to `moneyPackages` as they land (or move the check repo-wide).
  - Follow-up (separate wiring task): persist the reporting zone captured on first start, per the control default in decisions.md; F2's `reporting.timezone` description already states it.

- **B1 Banking provider boundary and bank facts** (ADR-0013): `internal/domain/banking` provider port, capabilities with the D10 access mode per connection, error taxonomy, sensitive-value type, idempotency keys; migration `0004_bank_facts` (connections, accounts, balance snapshots, transactions; RESTRICT-only references; explicit purge); `internal/store/bankfacts`; deterministic fake provider `bankingtest`; transitive "no provider import" test; float ban extended. No HTTP endpoints yet.
  - Follow-up (B2): whole-window keying, staleness and attempt timestamps, no-longer-reported marking, `superseded_by` links, retry policy by error kind.
  - Merge note: the outbox migration is 0003, so this one is 0004.

- **A1 Users, roles and grants model** (ADR-0021, first increment; D4 chosen): grant vocabulary `resource:action` in `internal/domain/authz` shared by HTTP, tokens and MCP; administrator and read-only roles; every API route declares its access (`HandleAPI`, documented as `x-ledger-access`); pluggable authenticators (none yet, so protected routes answer 401); migration `0006_users`; `/api/v1/users` list/create/get/merge-patch (disable, role change), `/me`, `/grants`; last enabled administrator protected; audit events through `store.AuditWriter` (the A5 audit table, ADR-0025, now backs it); permission matrix test over every route.
  - Merge note: the bank refresh migration is 0005, so users is 0006.
  - Remaining: A5 audit table implements `store.AuditWriter` and `serve` uses it; A2/A4 install authenticators (and the first-administrator bootstrap); recent re-authentication for user management (A2); OIDC provisioning policy (A6, D16); A7 admin screens; Terraform coverage of users (D18).
- **B2 Import engine** (ADR-0020): `internal/domain/bankimport`; whole-day windows stored atomically with a per-account resume cursor and overlap; every page fetched; no-longer-reported marking; `superseded_by` on a shared entry reference; D17 retries; migration `0005_bank_refresh` (refresh runs, per-account last attempt/success/error); computed freshness (24 h); unknown balances dropped, never zero; `StoredTransaction.Countable`.
  - Follow-up: refresh and freshness endpoints with the connection API (B4, auth tasks); scheduler (B6); serve wiring after B3; reversal, split and supersession handling in matching (X1/X2); retention of refresh runs.

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
