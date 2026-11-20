# ADR-0022: Currency-rate cache and conversion service

Status: Accepted (2026-11-20)

## Context

FX1 needs a rate-provider port, a SQLite cache of rates with provenance, and a conversion service that reports value, provenance and age, makes missing and stale rates explicit, never invents a rate, and keeps serving the cache when the provider is down or its quota is exhausted.

Decisions in force: D5 (any ISO 4217 code; exact decimals with their own scale; half-even rounding only at presentation and conversion; per-currency totals first, a converted aggregate second, items without an acceptable rate excluded and flagged) and D11 (Frankfurter with historical access, behind an adapter; store provider, the provider/source key, the exact rate, the rate date and the retrieval time; keep cached rates during outages; never invent a rate; refresh daily; a cached rate is stale after 5 days).

Research on Frankfurter's responses (a knowledge-base proposal, not yet accepted) observed that its blended rates, its per-source routes (for example ECB) and its v1 route give different values for the same pair and date, that per-source routes answer a weekend date with the previous working day while the blended route answers with the requested date, and that rates come as JSON numbers.

## Decision

1. **Ports in `internal/domain/fx`.** `Provider` (`Name`, `Source`, `Latest`, `OnDate`) returns `Quote`s: source key, pair, exact `money.Decimal` rate, rate date. Adapters parse rates from the response text (for JSON numbers, `json.Number`), never through `float64`, and classify failures as `*ProviderError` (`rate_limited` with `RetryAfter`, `unavailable`, `permanent`); an unclassified error counts as `unavailable`. Error messages carry provider, operation, kind and code only. `Cache` (`Save`, `Find`) is implemented by `internal/store/fxrates`.
2. **Cache row** (`fx_rates`, STRICT, WITHOUT ROWID): provider, source, base and quote currency, rate as exact TEXT (positive, `sqlvalue.AmountCheck`), rate date, first and last retrieval time (UTC). Key: (provider, source, base, quote, rate date). The source is part of the key because different sources give different values for the same pair and date. A re-retrieval moves `retrieved_at` forward and replaces the value if the provider revised it; an older retrieval changes nothing. Rates are provider facts, unversioned, never edited through the API.
3. **One source per service.** The service converts with the adapter's configured source only; rates of other sources are cached but not used.
4. **No invented rates.** The service never inverts a pair, triangulates through a third currency, interpolates between dates or defaults to 1. A provider answer with a non-positive rate, a different base or a date after the one asked (one day of tolerance for the latest rate, since the provider's calendar may run ahead of UTC) is a permanent provider error and nothing from it is cached.
5. **Freshness** (`fx.Freshness`, defaults from D11): `RefreshAfter` 24 h, `StaleAfter` 5 days.
   - `Convert` (latest): a cached rate retrieved less than `RefreshAfter` ago is used without calling the provider. Otherwise the provider is asked; on failure the cache is served. The result is **stale** when the rate was last retrieved `StaleAfter` ago or more (`not_retrieved_recently`) or its date is more than `StaleAfter` in whole days before today, UTC (`rate_date_too_old`). Staleness is measured from retrieval, so a Friday rate confirmed on Monday is current, and ECB weekends and holidays don't trip it.
   - `ConvertOn(date)` (historical): the newest rate dated on or before the date. A cached rate is final when it is dated that day or was retrieved after that day ended (UTC), so it is used without a provider call. Otherwise the provider is asked. If the provider can't be reached, an earlier unconfirmed rate is reported as stale (`unconfirmed`). A gap of more than `StaleAfter` days is stale. A future date is an error, and today uses `Convert`. Historical rates don't go stale by retrieval age.
6. **Result.** `Conversion` has a status (`ok`, `stale`, `missing`) and a reason. For `ok` it carries `Value` (rounded half-even to the target's ISO 4217 minor units, or exact for a currency without them) and `Exact` (the unrounded product). It also carries `Rate` (full provenance), `Age` (since last retrieval), `FromCache` and `RefreshError`. Stale and missing results carry no value, so they can't be summed by mistake. A stale result still carries the rate, so it can be shown. Only `ok` is `Acceptable()` for converted totals (D5).
7. **Quota exhaustion.** A `rate_limited` failure with `RetryAfter` pauses provider calls until then (in memory, per service). Conversions keep coming from the cache and report the pause in `RefreshError`.
8. **Provider calls are made outside write transactions**; `Save` writes a whole answer in one transaction after validating every rate.

## Why

- Measuring staleness from the last retrieval, not only from the rate date, keeps "the provider has been unreachable for 5 days" distinct from "the provider publishes nothing newer" (weekends, holidays). The rate-date check still catches a provider that keeps answering with an old date.
- A finality rule for dated rates avoids calling the provider every time a weekend or holiday date is converted, without assuming a holiday calendar.
- Keeping the source in the key preserves provenance when the configured route changes, and avoids mixing blended and single-source values in one series.
- Rejected: inverting a pair (1/rate needs rounding, so it would be a derived rate the provider never published); storing rates as REAL (ADR-0005); hard-coding Frankfurter's shapes in the domain (ADR-0001 boundary; FX2 owns them); a stale result with a computed value (too easy to sum by mistake).

## Consequences

- **Not in this change:**
  - The Frankfurter adapter (FX2), with its route choice (blended or a provider key such as ECB) and JSON-number parsing.
  - Configuration keys for the freshness policy and the source, which are wired with FX2.
  - The daily refresh job: `Service.Refresh` exists, but no scheduler calls it yet.
  - HTTP endpoints, OpenAPI and MCP tools. The route-access pattern (A1, PR #25) is not on `work` yet, and FX3 (outlook) is the first consumer.
  - Audit entries: no user action exists yet.
- The rate-limit pause is in memory and is lost on restart. The first call after a restart may hit the provider once more.
- A provider's revision of a past rate replaces the cached value; earlier values are not kept. Any figure that must stay reproducible (for example a reported converted total) has to copy the rate it used.
- **Migration numbering:** this branch adds `0005_fx_rates` (with a `0004` fixture copied from B2) so it is green on `work` alone. It is expected to become `0007` at merge, after A1 (`0005_users`) and B2 (`0006`), with the fixture renumbered to match.
- **Open:** the precision Frankfurter publishes is undocumented (about 4 to 5 decimals were observed), and rates are stored exactly as returned. Which Frankfurter route to use is an FX2 question for the operator if R7's ECB caveat matters.
