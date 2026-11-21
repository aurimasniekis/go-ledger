# ADR-0040: Bank connection API and UI

Status: Accepted (2026-11-21)

## Context

Tasks B1, B2, B3 and B6 built the provider boundary, the import engine, the Enable Banking adapter and the background scheduler, and
left every user-facing step for B4: ADRs 0020, 0023 and 0028 each say "the connection API (B4)" and none existed. B4 therefore
delivers both the HTTP API and the UI. It never needs live banking access: every test runs against the in-memory fake provider.

## Decision

1. **Service.** `internal/domain/bankconnect` holds the logic (applications, institutions, start, reconnect, complete, refresh,
   disconnect, and the per-connection view with per-account freshness). It uses only the `banking` ports, the shared import engine
   (the same instance as the scheduler, as ADR-0028 requires) and the audit recorder. It never returns a session reference, an
   authorisation code, an account handle or a full account number: accounts show a four-character hint.
2. **API** (`/api/v1`, in OpenAPI): `GET /bank-applications`, `GET /bank-applications/{application}/institutions`,
   `GET|POST /bank-connections`, `POST /bank-connections/complete`, `GET /bank-connections/{id}`,
   `POST /bank-connections/{id}/reconnect|refresh|disconnect`. A new administrator-only grant `connections:read|write` guards them;
   it is separate from `banking:read` (bank text). Disconnect needs the connection's version (If-Match). Consent start, reconnect,
   complete, refresh and disconnect are audited (`bank_connection.*`) with provider and application names only.
3. **Consent redirect.** `banking.redirect_url` (new, optional, https or loopback, path `/connections/callback`) is where the bank
   sends the user. That is a UI page: it posts the state and code to `POST /bank-connections/complete` with the session, once, and
   removes both from the address and history. The state works once, has a lifetime and an owner (point 8). Without the setting,
   starting a connection answers `bank_connect_unavailable`; refreshing and disconnecting still work.
4. **Reconnect and retry are different on the wire and on screen.** A refresh answers 200 with the connection even when it failed, and
   `last_refresh_run.error.kind` says what to do: `reconnect_required` (consent ended or withdrawn: retrying cannot help), `retryable` or
   `rate_limited` (try later; the consent is fine), `permanent` (neither helps). Errors raised before a refresh (institution lookup, consent
   start) use distinct codes: `bank_reconnect_required` 409, `bank_unavailable` 503, `bank_rate_limited` 429 with Retry-After,
   `bank_request_rejected` 422. The UI shows a solid "Reconnect needed" badge with a "Reconnect bank" action and no retry for the first, a
   tinted "Temporary problem" badge with "Try again" and no reconnect for the second, and "Refresh failed" with neither for the third.
5. **Visible freshness.** Every account shows its last successful refresh, last attempt, the latest balance of each type with when Ledger
   obtained it, and an up-to-date, stale (older than `banking.stale_after`, default 24 hours, D40.2), failed, reconnect, never-fetched or disconnected badge. A failed refresh never
   changes stored data; the figures stay on screen, labelled as from the last success. An account with no reported balance shows none,
   never zero. A connection's own state summarises the worst account.
6. **Non-idempotent calls are not retried.** Consent start, session end and consent completion are single calls. Completing a consent
   *claims* its state atomically (compare-and-swap in one write transaction) before the provider is called, so of two completions of
   one state (two tabs, a retrying client) only one reaches the provider; the other is `consent_not_found` and cannot fail or replace
   the first one's connection. The state is put back only after a failure a retry can fix (`retryable`, `rate_limited`, cancelled), so
   the callback page's "Try again" re-sends the same redirect; a refused consent marks the connection `failed` and keeps it visible. If
   Ledger cannot record a session the provider granted, it ends that session and marks the connection `failed` (`activation_failed`).
7. **Disconnect** ends the provider session first and changes nothing if the provider cannot be reached (no false "disconnected"); a session
   the provider no longer knows counts as ended. Stored accounts, balances and transactions stay as history. A successful **reconnect**
   of an existing connection (renewing early is allowed) ends the provider session it replaced, best effort: a failure does not undo the
   reconnect, is audited as `bank_connection.session_end_failed` (error kind only) and is shown as `previous_session_end_failed` in the
   completion answer, because that session may still be open at the bank.
8. **Consent lifetime and owner.** Each started consent stores when it started and the starting user (a token counts as its owner).
   `banking.consent_ttl` (default 1 hour, 1 minute to 24 hours; operator decision D40.3; the 1 hour value still has to be checked against Enable Banking's
   documented authorisation-link validity once that documentation is supplied) bounds it: later completion is refused as `consent_not_found`, a
   connection that never connected becomes `failed` with `consent_expired` (a reconnect that expired leaves the connection as it was),
   and expired consents are also ended whenever connections are listed or read (a cheap read finds whether any is due, so the write lock is taken
   only when one is; a failing sweep is logged and does not fail the read). Another user cannot complete it; their attempt leaves
   the state valid for the starter. Migration 0022 adds `consent_started_at` and `consent_initiator`.
9. **Refresh on request** (D40.4) waits for the run up to a deadline 5 seconds below `http.write_timeout` (at least 1 second), so the
   answer is not dropped. At the deadline the run is **not** cancelled and not recorded as failed: it goes on in the background (detached from the
   request), the answer is 200 with `refresh_running: true`, and the UI polls the connection list every 3 seconds until no connection is
   running. The refresh audit entry is written when the run ends. A run that outlives the process is closed by the engine's lease like any other
   interrupted run. Reconnecting a disconnected connection is allowed: the consent claim remembers the status it saw, and only a disconnect after
   the claim is refused at completion. The
   answer and the audit entry use the run the engine returned (`last_refresh_run` of that call); an error before any run started is not a
   200: `connection_not_active` 409 when the connection stopped being active, otherwise the error itself.
10. **Audit in the transaction.** Start, reconnect start, completion and disconnect write their audit entry in the same transaction as the
   change, so a failed audit write undoes it (fail closed, as elsewhere in Ledger). Only the refresh entry is written afterwards (the
   provider call is the change, and it cannot be rolled back); if that write fails the response says so.

## Why

- A refresh that failed is information, not an HTTP failure: the UI needs the run and the preserved data in one answer. Pre-refresh calls
  have no data to return, so they fail with codes.
- `connections` is administrator-only like `banking:read` (D29) because it manages access to bank data; whether a single-person
  deployment needs finer roles is an open decision (see below).
- The callback is a UI page because the bank redirects a browser: a GET API endpoint that changes state from a redirect would run without
  the session's CSRF protection.

## Consequences

- **Limitations, in the open:** (a) the Go client and CLI have no bank methods yet (the new operations are listed in the client's
  not-implemented table); the MCP server does not expose bank tools. (b) A refresh is waited for inside the HTTP request, bounded below the write timeout (point 9). When
  the bound is hit the run continues and the answer says it is running; a 202 job model is the alternative if banks are routinely slow. (c) The refresh audit entry is written after the run (point 10); the entries of the other actions are not.
  (c2) The reporting of a failed end of a replaced session is a boolean on the completion answer; the UI does not yet show it. (d) Only the latest 64 refresh runs feed the scheduler; the UI shows just the latest run. (e) Purging a connection's
  facts has no endpoint. (f) Balances are read from all snapshots of an account to find the latest of each type; a retention policy
  (open decision) should bound that. (g) The e2e spec (Playwright) drives the real UI against a stateful stub of the API that mirrors the fake
  provider; the real handlers and store run the same journey in the Go test `TestBankConnectionJourney`. No live Enable Banking check was made.
- **Open decisions this touches:** who may manage connections beyond administrators (D1 audience), freshness threshold and consent lifetime (decided in D40.2 and D40.3: 24 hours and 1 hour by default, both configurable; the 1 hour
  value still needs the Enable Banking check) and whether a 202 job model is wanted for refresh. Whether applications of different modes
  coexist is settled by D10: per connection, each recording its application, mode and environment (the sandbox environment is for fixtures
  and tests). The API journey test covers an app-mode and a developer-mode application side by side.
