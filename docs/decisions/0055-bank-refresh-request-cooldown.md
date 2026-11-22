# ADR-0055: Cooldown for bank refresh on request

Status: Accepted (operator decision D-B6c, 2026-11-22: cooldown of 5 minutes, per connection, configurable). The design points
below (what counts as a finished refresh, the error code, the zero value) are this ADR's choices within that decision.

## Context

A refresh on request calls the bank, which limits how often it may be asked and may answer with a rate limit that parks the
connection for hours ([ADR-0028](0028-background-refresh-scheduler.md)). Every entry point that can request one (the HTTP API,
the web UI, the CLI, and MCP including `ledger mcp stdio`) ends in one route, `POST /api/v1/bank-connections/{id}/refresh`, and in
`bankconnect.Service.Refresh`, so one check covers them all.

## Decision

1. **Per connection, from the end of the last refresh.** After a refresh of a connection has finished, another refresh *on request*
   for that connection is refused until `banking.refresh.request_cooldown` (default **5 minutes**) has passed. The clock starts
   when the last run *finished*, read from the run history (`bank_refresh_runs`), so it survives a restart and needs no new table
   or migration.
2. **Any finished run counts**, whichever way it was started and however it ended (succeeded, partial, failed, interrupted).
   A failed refresh also costs the bank a call, and a person who hits an outage is better served by the stated wait than by
   hammering the bank. A run that is still going does not start a cooldown: the engine already refuses a second one
   (409 `refresh_in_progress`).
3. **The scheduled background refresh is unaffected.** The scheduler calls the engine directly and never the service, so it is
   neither refused by the cooldown nor needs to know about it. Its runs do start the cooldown for requests, because they are
   runs in the history (see 2). This errs on the safe side for the bank's few daily fetches.
4. **The refusal.** HTTP **429** with the stable code `refresh_cooldown` and a `Retry-After` header in whole seconds (rounded up).
   Nothing is started, recorded or audited for a refused request. It is distinct from `bank_rate_limited` (the bank asked for a
   pause) and `rate_limited` (Ledger's own request limits).
5. **Visible state.** The connection carries `refresh_available_at` (UTC, null when a refresh is allowed now). The UI disables
   the Refresh button and shows "Refreshed just now, try again in N min" (N rounded up, at least 1), counted from the server's
   `as_of` so the device clock does not matter. The CLI prints the error and `retry after N seconds`; MCP tool errors carry
   the code, the 429 status and "(retry after N seconds)", through the in-process server and `ledger mcp stdio` alike (the client
   library reads `Retry-After` into `APIError.RetryAfter`).
6. **Configuration.** `banking.refresh.request_cooldown` (env `LEDGER_BANKING_REFRESH_REQUEST_COOLDOWN`, YAML, `.env`), a duration
   from `0s` to `24h`, default `5m`. **`0s` switches the cooldown off.** Other values are rejected at startup with the key named.
   It is independent of `banking.refresh.enabled`.
7. **Reconnecting resets it.** A run that finished before the connection's current consent was granted does not count: after a
   failed run (for example the consent ended) and a reconnect, a refresh is allowed at once, and the connection's
   `refresh_available_at` is empty. The same rule applies in the refusal and in the view (C-B6c-reconnect).

## Consequences

- A person who wants fresh data right after a refresh must wait; the stored data stays visible with its own freshness labels.
- The bank's own limits are unchanged and not enforced here: this is a guard against accidental repeats, not a quota.
- The check reads the run history before starting the run; two requests racing in the first instant are still arbitrated by the
  engine's per-connection lock (the second gets `refresh_in_progress`), after which the cooldown applies.
- Not covered by the browser journey (e2e), which sets the cooldown to `0s` because it refreshes one connection repeatedly;
  the cooldown is covered by offline Go tests with a fake clock (service, API, CLI, MCP in process and stdio, configuration)
  and by UI tests.
