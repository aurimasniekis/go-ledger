# ADR-0046: Frankfurter rate adapter and daily refresh

Status: Proposed (2026-11-21). The source default (ECB) and the numeric settings below are implementation choices
inside decision D11; they are listed for the operator under Consequences.

## Context

FX1 (ADR-0022) built the rate cache and conversion service behind a `fx.Provider` port and left three things for FX2:
the Frankfurter adapter, the configuration keys and the daily refresh job. Decision D11 selects Frankfurter, including
its historical rates, behind that port: store the provider, the source key, the exact rate, the rate date and the
retrieval time; keep cached rates in outages and never invent a rate; refresh daily; a cached rate is stale after five
days. The outlook (ADR-0044) already shows `no_provider` when no rate service is wired.

Frankfurter's v1 routes answer with a `Deprecation` header pointing at v2, so the adapter uses v2.

## Decision

1. **Route.** `GET /v2/rates?base=B&quotes=Q1,Q2[&providers=KEY][&date=YYYY-MM-DD]` answers an array of
   `{date, base, quote, rate}`. The latest rates and the rates on a past date (historical access, `OnDate`) use the same
   route. A quote the provider does not publish is absent (an empty array is "not published", not an error). The
   response shape was observed on the live service once and recorded by hand as fixtures
   (`internal/providers/frankfurter/testdata`); tests use them through a local test server and never reach the
   live API.
2. **Exact rates.** The body is decoded with `json.Number` and the literal goes through `money.ParseDecimal`:
   no float64 anywhere, trailing zeros kept (`159.40` stays `159.40`), an exponent form refused. A non-positive rate,
   an unexpected base, an unasked or duplicated quote, a date later than the date asked for, a malformed body or one over
   1 MiB make the **whole answer** a permanent failure; nothing from it is cached.
3. **Source.** `rates.source` is the Frankfurter provider key sent as `providers` and stored as the rate's source;
   the default is **ECB**. `blended` sends no `providers` parameter and stores the source `blended`. The cache
   keeps sources apart (ADR-0022), so changing it never mixes series. Why ECB by default: one named publisher whose
   publication calendar (working days; a weekend or holiday answers with the previous working day, which the quote's date
   says) is documented, rather than a blend whose composition can change. The operator may choose `blended`.
4. **Failures** are classified for the service: 429 is `rate_limited` with `Retry-After` (seconds or HTTP date, capped
   at 24 h; the service pauses calls for it); network errors, timeouts, 408 and 5xx are `unavailable` (the service
   pauses for a short, bounded time); other statuses (an unsupported currency is 422) and unusable answers are
   `permanent`. The error carries the provider name, operation, kind and a short code only: never the URL, a body or a
   transport message. A caller's own cancellation stays visible as `context.Canceled`.
5. **Quota and cache terms.** Frankfurter needs no key and publishes no quota. The adapter is deliberately gentle: one
   request per base currency, no automatic retries, a request timeout (`rates.timeout`, 15 s), no redirects followed,
   a generic `User-Agent` and nothing about the user or the amounts in a request (only currency codes and a date). The
   cache is never discarded: a failed or refused call leaves it as it was.
6. **Refresh job** (`fx.RefreshJob`, started by `ledger serve` when `rates.enabled` is true **and**
   `reporting.currency` is set). It runs at startup and then every `rates.refresh_interval` (24 h): for each distinct
   currency of bank accounts, balance snapshots and commitments other than the reporting currency it calls
   `Service.Refresh(currency, [reporting])`. A permanent failure skips that currency; an unavailable or rate-limited
   failure stops the cycle (the provider is not answering) and the job retries after 5 min, doubling per consecutive
   failure, at least the provider's `Retry-After`, never beyond the interval. The job only adds to the cache, so a failed
   cycle changes nothing. It stops with the process and is waited for before the database closes. Without a reporting
   currency, rates are still fetched on demand when a request asks for a converted total (`Service.Convert`).
7. **Freshness** keeps FX1's meaning: `rates.refresh_interval` is `Freshness.RefreshAfter` (a rate retrieved less than
   this long ago is used without a call; default 24 h) and `rates.stale_after` is `StaleAfter` (default 120 h = D11's
   five days). Validation: stale_after at least 24 h; refresh_interval from 1 h to stale_after; timeout 1 s to 2 min;
   `base_url` https (http only on loopback), without credentials or query; source a provider-key-shaped token.
8. **Optional to enable.** `rates.enabled` defaults to **false**. The service is nevertheless always built on the cache:
   with the provider off, rates cached while it was on still convert (with their age and, when old, the stale status),
   and an empty cache says `no_provider`. Nothing contacts Frankfurter at startup except the job's first cycle. Turning
   it on sends currency codes to the Frankfurter service; the key's description says so.
9. **Wiring.** `httpapi.Deps.Rates` is the `*fx.Service`, so the outlook's converted secondary total now uses
   cached rates with provenance (ADR-0044 point 7 is superseded by this).

## Consequences

- No migration: `fx_rates` (migration 0007) already holds provider, source, exact rate, date and retrieval times.
- New: `internal/providers/frankfurter`, `internal/domain/fx/refresh.go`, `cmd/ledger/rates.go`, the `rates.*`
  configuration keys (`config.example.yaml` and `.env.example` regenerated). No endpoint, so no OpenAPI change.
- **Not in this change:** a metric for refresh outcomes and a rate-freshness entry in the readiness/health output (the
  outlook already shows each used rate's age and an excluded currency's reason); an API or UI to trigger a refresh or to
  list cached rates; fetching historical rates for back-filled conversions (the adapter and `Service.ConvertOn` support
  it, but no feature asks for it yet); an audit entry per refresh (rates are not user data and no user acts).
- **Open, for the operator:** the default source (ECB or blended); the default refresh timing (a restart refreshes
  immediately, so a frequently restarted instance asks once per start); whether a restart should skip the first cycle
  when the cache is fresh. Rates are mid/reference rates of a central bank or a blend, not what a bank or card charges,
  and the outlook already presents the converted total as secondary.
