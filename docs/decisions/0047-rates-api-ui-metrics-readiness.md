# ADR-0047: Exchange rates: list and refresh API, client, CLI, MCP, UI, metrics and readiness

Status: Proposed (2026-11-21). The `rates` grant (point 1), the one-a-minute limit on manual refreshes (point 3) and the
readiness stance (point 6) are implementation choices no operator decision covers; they are listed for the operator
under Consequences.

## Context

FX2 (ADR-0046) shipped the Frankfurter adapter, the cache and the daily refresh job with no way to see the cached rates,
to ask for a refresh, or to watch the refresh. Decision D11 asks for the provider, the source key, the exact rate, the
rate date and the retrieval time to be stored, and for a rate to be stale after five days; the outlook already shows the
rate it used. This change makes the cache and the refresh visible everywhere Ledger offers a capability (API, client,
CLI, MCP read tool, UI) without making the optional rate provider a dependency.

## Decision

1. **Grant.** A new resource `rates` with `rates:read` and `rates:write`. Reading is not administrator-only: the rates
   are public reference data and the read-only role holds `rates:read` (so the UI's rates page and the MCP read tools
   work for them). Writing, which asks the provider for fresh rates, is held by administrators only (the read-only role
   never holds a write; a token holds it only if its owner does). Reusing `commitments:write` was rejected: refreshing
   rates has nothing to do with editing commitments, and `connections:write` is for bank access.
2. **Read API.** `GET /api/v1/rates` lists the cache in the Management API's conventions (ADR-0011): `limit`, an
   opaque `cursor`, and equality filters `provider`, `source`, `base`, `quote` plus `date[gte]` and `date[lte]`
   (rate date); unknown parameters and repeated single-value filters are 400. Order: provider, source, base, quote, then
   rate date newest first. Rates have no identifier of their own, so the cursor carries the rate's key (provider,
   source, pair, date), validated on decode, and resumes with a keyset query. An item is the outlook's `ExchangeRate`
   fields (`provider`, `source`, `base`, `quote`, `value` as an exact decimal string, `date`, `retrieved_at`,
   `confirmed_through`) plus `first_retrieved_at`, `stale`, `stale_reason` and `previous` (the value a provider
   revised away). `stale` is D11 evaluated at request time with the same test conversion uses
   (`fx.Service.StaleReason`): last retrieved the stale threshold (5 days) ago or longer, or dated more than that many
   days before today. Listing reads the cache only and never calls the provider.
3. **Manual refresh.** `POST /api/v1/rates/refreshes` (`rates:write`) runs one refresh cycle of the daily job: every
   currency in use to the reporting currency. It only adds to the cache. It is **bounded** (a timeout below
   `http.write_timeout`, the same bound as other on-request refreshes; the adapter's own `rates.timeout` still applies
   per request), **never overlaps** another run, scheduled or manual (409 `refresh_in_progress`), and is **limited to one
   a minute** (429 `rate_limited` with `Retry-After`), so a button or a script cannot turn Ledger into a load generator
   against a free, unauthenticated service. A failed run is still 200: its `outcome` (succeeded, partial, failed) and
   `error` (the provider's kind and short code, never a URL or message) say how it ended, like the bank refresh. 409
   `rates_refresh_unavailable` means no job exists (`rates.enabled` false or no `reporting.currency`). **Audit:** every
   accepted request is recorded as `rates.refresh_requested` (actor, outcome, currencies refreshed, error kind and code);
   a refused one (403, 409, 429) changes nothing and is not recorded. A refresh through an API token is also covered by
   the generic `token.write_used` entry.
4. **Status.** `GET /api/v1/rates/status` (`rates:read`) reports whether a provider is configured, provider and source,
   the reporting currency, whether a refresh can run, the freshness policy and the last run (since the process started:
   trigger, times, outcome, count, error kind and code) with the last fully successful refresh. It is the place the
   optional provider is **reported**.
5. **Client, CLI and MCP.** `pkg/ledgerclient` gets `ListRates`, `AllRates`, `GetRatesStatus` and `RefreshRates`;
   the CLI gets `ledger rates list|status|refresh`; MCP gets two read tools, `list_exchange_rates` and
   `get_exchange_rate_status`, with grant `rates:read`. There is deliberately **no MCP refresh tool**: it calls an
   external service and is an administrator action, available through the CLI and API. Tests tie the client to the OpenAPI
   document, the MCP tools to their routes' grants and the CLI to the real handlers.
6. **Readiness.** The rate provider is optional (`rates.enabled` defaults to false), and Ledger works without it, so it is
   **not a readiness dependency**: `/readyz` gates on the database only (D17) and never mentions rates, whatever the
   provider's state. Its state is reported by the status endpoint and by metrics instead, like Pulse. This is also why
   no health check was added: a provider outage must not take Ledger out of rotation.
7. **Metrics.** `ledger_rates_refresh_runs_total{trigger,outcome}` (trigger scheduled or manual; outcome success when
   every currency was refreshed, else failure), `ledger_rates_last_success_timestamp_seconds`,
   `ledger_rates_last_run_timestamp_seconds` (absent until a run has finished since the process started) and
   `ledger_rates_provider_enabled`. A run cut short by shutdown or by the caller going away is not counted: it says
   nothing about the provider. Labels are a closed set.
8. **UI.** A read-only page `/rates` ("Exchange rates", in a "Reference" group of the Account menu with `rates:read`, and linked from the outlook's converted section; the primary navigation stays at four links so it fits one row on a phone): the status
   (provider and source, whether it is off, the stale rule, the last refresh and, after a failure, the last full
   success) and a table of the cached rates with exact values, ISO rate dates (D38), retrieval time in UTC, provider and
   source, and a stale badge with its reason. Loading, empty (with the provider-off explanation), failed and
   load-more states are shown. It has no refresh button: refreshing is an administrator action through the CLI or API,
   and the page is for seeing what the outlook converts with.
9. **PR #92 follow-ups.** (N1) When the outlook's conversion deadline passes, a currency whose rate is already cached
   still converts: the outlook asks `fx.Service.ConvertCached` (cache only, same freshness rules) before it excludes the
   currency as `conversion_timed_out`. Only a currency that needs the provider and has no usable cached rate is
   excluded. A provider call cut short by the deadline records no failure pause (the caller's deadline says nothing about
   the provider), so a `rates.timeout` above the bound costs one slow call per request, not a pause; the
   cache-only fallback keeps the outlook useful meanwhile. (N2) `NormalizeSource` is Frankfurter-specific and moved from
   `internal/domain/fx` into the adapter package; the domain service no longer normalises, and configuration and wiring
   call the adapter's function.

## Consequences

- No migration. New: `internal/domain/fx/list.go` and `refresh_status.go`, `internal/httpapi/rates.go`,
  `fxrates.Store.List`, `cmd/ledger/mgmt_rates.go`, `web/src/features/rates`, the `rates.refresh_requested` audit action
  and the `rates_refresh_unavailable` error code. `docs/auth.md` and `docs/mcp.md` list the grant and the tools.
- The status of the last run is in memory: it is empty after a restart (the metrics counters too). The stored rates and
  their retrieval times are what persists; a durable run history is not built.
- The rate list has no filter on `stale`: staleness is computed per request and a filter would need post-filtering that
  breaks keyset paging. The flag is on every item.
- **Open, for the operator:** whether `rates:read` should be administrator-only like `connections:read`; the
  one-a-minute limit on manual refreshes; whether a refresh button belongs in the UI for administrators; whether the
  status should become durable.
