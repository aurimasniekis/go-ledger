# ADR-0039: Bank connection API and UI

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
   removes both from the address and history. The state works once (a replay is `consent_not_found`). Without the setting,
   starting a connection answers `bank_connect_unavailable`; refreshing and disconnecting still work.
4. **Reconnect and retry are different on the wire and on screen.** A refresh answers 200 with the connection even when it failed, and
   `last_refresh_run.error.kind` says what to do: `reconnect_required` (consent ended or withdrawn: retrying cannot help), `retryable` or
   `rate_limited` (try later; the consent is fine), `permanent` (neither helps). Errors raised before a refresh (institution lookup, consent
   start) use distinct codes: `bank_reconnect_required` 409, `bank_unavailable` 503, `bank_rate_limited` 429 with Retry-After,
   `bank_request_rejected` 422. The UI shows a solid "Reconnect needed" badge with a "Reconnect bank" action and no retry for the first, a
   tinted "Temporary problem" badge with "Try again" and no reconnect for the second, and "Refresh failed" with neither for the third.
5. **Visible freshness.** Every account shows its last successful refresh, last attempt, the latest balance of each type with when Ledger
   obtained it, and an up-to-date, stale (over 24 hours), failed, reconnect, never-fetched or disconnected badge. A failed refresh never
   changes stored data; the figures stay on screen, labelled as from the last success. An account with no reported balance shows none,
   never zero. A connection's own state summarises the worst account.
6. **Non-idempotent calls are not retried.** Consent start, session end and consent completion are single calls. A retryable failure to
   complete a consent leaves the state valid, so the callback page's "Try again" re-sends the same redirect; a refused consent marks the
   connection `failed` and keeps it visible.
7. **Disconnect** ends the provider session first and changes nothing if the provider cannot be reached (no false "disconnected"); a session
   the provider no longer knows counts as ended. Stored accounts, balances and transactions stay as history.

## Why

- A refresh that failed is information, not an HTTP failure: the UI needs the run and the preserved data in one answer. Pre-refresh calls
  have no data to return, so they fail with codes.
- `connections` is administrator-only like `banking:read` (D29) because it manages access to bank data; whether a single-person
  deployment needs finer roles is an open decision (see below).
- The callback is a UI page because the bank redirects a browser: a GET API endpoint that changes state from a redirect would run without
  the session's CSRF protection.

## Consequences

- **Limitations, in the open:** (a) the Go client and CLI have no bank methods yet (the new operations are listed in the client's
  not-implemented table); the MCP server does not expose bank tools. (b) A refresh runs inside the HTTP request; a very slow bank can hit
  the server's write timeout, in which case the engine records the run as interrupted and the UI shows the failure. (c) Audit entries for these
  actions are recorded after the change, not in its transaction (the provider call cannot sit inside one); if the audit write fails the
  response says so. (d) Only the latest 64 refresh runs feed the scheduler; the UI shows just the latest run. (e) Purging a connection's
  facts has no endpoint. (f) Balances are read from all snapshots of an account to find the latest of each type; a retention policy
  (open decision) should bound that. (g) The e2e spec (Playwright) drives the real UI against a stateful stub of the API that mirrors the fake
  provider; the real handlers and store run the same journey in the Go test `TestBankConnectionJourney`. No live Enable Banking check was made.
- **Open decisions this touches:** who may manage connections beyond administrators (D1 audience), freshness thresholds (24 hours is a
  proposal, not an operator decision), and whether sandbox and production applications may be connected side by side (the UI shows each
  connection's application, mode and environment).
