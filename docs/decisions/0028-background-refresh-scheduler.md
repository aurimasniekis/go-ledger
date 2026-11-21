# ADR-0028: Background refresh scheduler

Status: Proposed (2026-11-21); the shape follows decision D17, the intervals, backoff and budget rules are **proposed, not operator decisions**

## Context

Task B6 refreshes bank connections periodically, so the monthly view and the outlook rest on data that is current or visibly stale. It builds on the import engine ([ADR-0020](0020-bank-import-engine.md)) and the Enable Banking adapter ([ADR-0023](0023-enable-banking-adapter.md)). D17 (accepted) fixes: retries only for idempotent reads, at most 3, exponential backoff with jitter honouring Retry-After; bank data stale after 24 h; background logs carry `job_id` (ULID) and the job name; never retry consent POSTs or DELETE. It does **not** fix a refresh interval, a failure backoff or a rate-limit wait, so B6 is partial: those values below are proposals, kept in options and configuration so the operator can change them without code changes.

## Decision

1. **Where.** `bankimport.Scheduler` (`internal/domain/bankimport/scheduler.go`) uses the `banking.FactStore` reads it needs (`ScheduleStore`) and a `Refresher` (the engine), so it is domain code with no store or adapter import. `ledger serve` builds one engine and one scheduler and runs the scheduler in a goroutine on the server's context. Shutdown cancels it; an in-flight refresh is recorded as `interrupted` by the engine (ADR-0020 §9) and `serve` waits for the scheduler before the database closes. `banking.refresh.enabled=false` switches it off; refreshes then happen on request only (B4).
2. **No migration; the run history is the schedule.** Last finish, outcome, consecutive failures and the daily count come from `bank_refresh_runs`, so a restart loses nothing. Only two things are in memory: an exact Retry-After longer than the derived wait, and the jitter drawn for the backoff after a run (so later passes keep the same attempt time). After a restart the conservative derived time applies.
3. **One attempt per connection per pass; no replay.** A pass lists connections and refreshes each that is due, sequentially. A failed refresh is never repeated inside the pass: its retries are the engine's per-call retries of idempotent reads (D17, ADR-0020 §10). The scheduler calls only `Refresh`, so it never issues `StartConsent`, `CompleteConsent`, `EndSession` or any other write at the provider; a test asserts that. Bank facts are written only through the engine's idempotent keyed upserts.
4. **When a connection is due (proposed values).**
   - Never refreshed: at once. After a success: `interval` (default **6 h**, four a day).
   - After a failure (failed or partial): `15 min` doubling per consecutive failure, capped by `max_backoff` (default **6 h**), with jitter in [d/2, d].
   - After a rate limit: at least the **6 h** rate-limit backoff (R1; the adapter's own 6 h for `ASPSP_RATE_LIMIT_EXCEEDED`), or the provider's Retry-After when longer, capped at 24 h so a bad header cannot park a connection for good.
   - After an interrupted run (shutdown): at once; committed windows stay and the next run resumes.
   - A connection that is not active (`reconnect_required`, disconnected) is never refreshed: only the user can fix it. One without a configured application is shown as `no_provider`. A connection another engine is refreshing (`ErrRefreshInProgress`) is skipped for a minute and is not a failure.
5. **Provider budget.** When the provider's `BackgroundFetchesPerDay` is known, a connection gets at most that many runs in any rolling 24 h; the next waits for the oldest counted run to age out (`budget_exhausted`). Every recorded run counts, user-triggered ones too, which errs on the safe side. A run is treated as one "fetch" although it makes several provider calls: Enable Banking's limit is per bank and approximate (ADR-0023 [U]), so this is an estimate, not a guarantee.
6. **Visibility without blocking.** Passes run off the request path; the UI never waits for them.
   - Logs: each pass line carries `job=bank_refresh_scheduler` and a new ULID `job_id`; each refresh it starts keeps the engine's `job=bank_refresh` and `job_id` = run id, and the pass line names the run (`refresh_run_id`). Freshness changes of a connection are logged once (warning for stale, failed, reconnect required, disconnected).
   - Metrics: `ledger_job_runs_total`, `_last_run_timestamp_seconds`, `_duration_seconds` with `job="bank_refresh_scheduler"` for passes that did work (idle passes are not counted), and `ledger_bank_accounts{freshness}`, accounts per state (`fresh`, `stale`, `failed`, `reconnect_required`, `disconnected`, `never_refreshed`), a closed set.
   - `Scheduler.Status()` returns per connection the schedule state (`waiting`, `due`, `running`, `backoff`, `rate_limited`, `budget_exhausted`, `not_active`, `no_provider`), the next attempt, consecutive failures, the last error kind and code and the worst account freshness. It is ready for the status endpoint and the connection API (B4); none exists yet.
   - Readiness stays database-only (D17); a stale or failing bank never fails `/readyz`.
7. **Configuration.** `banking.refresh.enabled` (true), `.interval` (6h), `.max_backoff` (6h); the two durations must be at least 1 minute. The 24 h staleness, the retry count and the provider budget are not configurable.
8. **First-import history (follow-up from the B3 review).** `bankimport` always sets `From` (the first refresh reaches back `InitialHistoryDays`, 90 by default, capped by the provider's `MaxHistoryDays`), so the adapter's `strategy=longest` (a zero `From`) is never used. It is **not changed here**: a "longest history" request is one unbounded window, which would break the engine's invariants (ADR-0020 §3-5: windows of at most 31 days, stored one at a time, the cursor advancing, "no longer reported" marking bounded by the window). **Proposed decision for the operator:** is 90 days enough for the first import, or should the first import go further back (up to the provider's limit, in chunks, through `InitialHistoryDays`)? Enable Banking may require an extra authentication for history beyond 90 days (ADR-0023 [U]). Until decided, history before the initial window is not imported.

## Why

- Deriving the schedule from the run history needs no table, survives restarts and crashes, and cannot drift from what actually happened; the cost is that an exact Retry-After is lost at restart, replaced by the 6 h floor.
- Sequential passes with one attempt per connection keep the bank's few daily fetches and Ledger's single SQLite writer unstressed; a single user has few connections.
- Keeping the budget check on the history makes crash loops and manual refreshes count against the same budget.

Rejected: a persisted schedule table (more state to keep consistent; the history already holds the facts); retrying a failed run inside the pass (spends the daily budget); one goroutine per connection (no gain for one person, harder shutdown).

## Consequences

- **Unresolved, to ask the operator (D17 follow-up):** the interval (6 h), failure backoff (15 min doubling to 6 h), rate-limit floor (6 h) and the cap on Retry-After (24 h) are proposals; the "a run is one fetch" budget estimate; and §8, the first-import depth.
- **Not built here:** the connection API and a status endpoint (B4) and the UI surfacing (B5); until then the freshness shows in logs, metrics and `Scheduler.Status()`. B4's manual refresh must use the same engine instance as the scheduler so the in-process lock covers both.
- **Limitations.** The budget counts runs, not provider calls. A failure backoff is counted from the history, so a user-triggered success resets it, and a user-triggered run also resets the interval. A refresh running longer than the engine's run lease can be taken over (ADR-0020). Times are the process clock; a large clock jump can run a refresh early or late (the budget check still limits it).
