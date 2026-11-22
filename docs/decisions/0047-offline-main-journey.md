# ADR-0047: The offline end-to-end main journey

Status: Accepted (2026-11-21). Implements task U3. Nothing here needs an operator decision.

## Context

The earlier Playwright specs stub the API in the browser, so none of them proves that the UI, the HTTP API, the store,
the import engine, the matching engine and the outlook agree. The initial plan names one journey that must: import from
a (simulated) bank, record commitments, read the month, match, correct, reload, and read the outlook.

## Decision

1. **The real binary, no stubs.** `web/e2e/journey.spec.ts` runs `ledger serve` with its embedded UI, a throwaway
   database and a user created by the host command `ledger auth set-password --create`. A second `webServer` in
   `playwright.config.ts` starts `web/e2e/journey-server.mjs`, on its own ports so the other specs are unaffected.
2. **A simulated Enable Banking API, not the in-process fake.** The fake provider in `bankingtest` is not wired into
   the binary, and wiring a test provider into production code was not worth it. Instead `journey-server.mjs` serves the
   Enable Banking endpoints the adapter calls (shaped like `internal/providers/enablebanking/testdata`) on loopback,
   and the binary's own adapter is pointed at it with `banking.enable_banking.base_url`. So the real adapter's consent
   redirect, session, account details, balances and transactions requests run against the double, and it signs its JWTs with a key
   generated on every start. The double checks only that a `Bearer` header is present, not the JWT's signature or claims,
   and it never pages: each transactions request gets one page with no continuation key.
   The simulation also has a control surface (`POST /__control/outage|revoked`) for the degraded states.
3. **Dates relative to today.** Bank data and commitments are placed in the previous month and the next one
   (`journey-data.mjs`), so the journey gives the same figures on any day. The dates are computed in UTC, the browser runs
   in UTC and the binary's reporting zone is pinned to UTC (`LEDGER_REPORTING_TIMEZONE`). Background refresh and rates are
   switched off.
4. **What the journey asserts.** Connection and import; exact payment confirmed by the engine; two identical payments
   left as competing suggestions; confirm one, reject the other; correct a confirmed amount (800.00 to 500.00:
   partly paid, outstanding 300.00); statuses and totals unchanged after a reload; outlook balance with its date, the
   "payments after the balance date" caveat, still-due and not-recorded notes; a failed refresh (stale, balance kept,
   outlook says not fresh); the browser offline; a withdrawn consent and reconnecting.
5. **CI.** A new `e2e` job in `ci.yml` builds the binary and runs `make e2e`. The only download is the browser.

## Limits and findings

- **No overdue in a real-clock journey.** An occurrence is overdue only if it is due on or after the commitment's
  creation date and before today (ADR-0042); created today, that is impossible, and the binary has no clock override.
  The overdue drill-down from the outlook into the month stays covered by `outlook.spec.ts` (stubbed API) and the Go
  tests with injected clocks. A journey with real overdue items needs a clock hook or day-spanning data: not built.
- **Commitments have no screen**, so the journey creates them, and starts a matching run, through the API as the
  signed-in session.
- **Finding, fixed by U4: stale "Reconnect needed" after reconnecting.** After a successful reconnect the connection is
  `active` with `needs_reconnect: false`; the card used to keep showing "Reconnect needed" with no "Refresh now". It now
  says "Reconnected, not refreshed yet", and an account's reconnect error is superseded per account (its last attempt is
  before the consent was granted), so accounts stay Stale and are never called fresh. The journey asserts this in the UI.
- **Finding, not fixed: the month view says "No bank data connected yet"** even when a bank is connected and imported
  (the API reports `freshness.bank.available` false by design for now, see `internal/httpapi/occurrences.go`).
- **No paging.** The double answers every transactions request with one page and no continuation key, so the adapter's
  paging is not exercised by this journey; it is covered by the in-package fake in `internal/providers/enablebanking/fake_test.go`.
- **No JWT verification.** The double does not verify the adapter's JWT signature, so a wrong key would not fail here.
- A failing bank is retried with a backoff by the adapter, so the failed-refresh step takes several seconds.
