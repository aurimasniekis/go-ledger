# ADR-0044: The outlook against recorded commitments

Status: Proposed (2026-11-21). Points 3, 4, 6 and 8 are implementation choices no operator decision covers; they are
listed for the operator under Consequences.

## Context

Task FX3 compares bank balances with the commitments still due, per currency and, where policy allows, converted with
provenance; the freshness of each balance and rate is shown, and the copy says "outlook against recorded commitments",
never safe-to-spend. Decisions in force: D5 (per-currency totals first; a converted aggregate in one reporting currency
only as secondary, with rate provenance, excluding and flagging items without an acceptable rate; half-even rounding only
at presentation and conversion), D7 (derived payment status; overdue from the original due date, grace annotated, never
hiding an obligation), D11 (Frankfurter, daily refresh, a rate is stale after 5 days), D38/D41 (occurrences due before a
commitment was created are "not recorded" and excluded from what is owed), D39 (tolerance-paid counts as paid) and D40
(bank data stale after 24 h). The rate adapter (FX2) and the bank connection API (B4, PR #77) are not on `work` yet,
and payment derivation is changing in parallel (PR #79).

## Decision

1. **Transport-free service, `internal/domain/outlook`.** `Service.Build` reads, through ports, the monthly view
   (`monthview.Service`), commitment start dates, stored bank facts (`banking.FactStore`'s read methods) and an optional
   converter (`*fx.Service`). `GET /api/v1/outlook` (grant `commitments:read`) is a thin handler over it; a CLI or MCP
   view can reuse it later.
2. **What is still due comes from the monthly view, month by month.** For every month from the earliest commitment start
   through `through` (default: the current month in the reporting zone, at most the 60-month horizon) the outlook adds up
   the monthly view's per-currency `outstanding` and `overdue`. Payment state is therefore derived in exactly one place
   (`payments.Derive` via `monthview`); PR #79's "not recorded" and tolerance-paid rules reach the outlook without a
   change here. `upcoming` is `still_due - overdue`. Cancelled and paid occurrences owe nothing; evidence in another
   currency is never converted and is only counted (`unconverted_evidence_count`).
3. **Look-back bound (proposed).** At most 240 months before `through` are read, so one request cannot read without
   limit; `window.truncated` says when older commitments exist.
4. **Balances (proposed).** Per account and currency, the balance of the newest fetch is counted, choosing booked, then
   expected, then available (available can include an overdraft facility). "Other" balances, accounts that report no
   balance and accounts no longer reported are listed with the reason and not counted. Balances are append-only snapshots,
   so a re-import adds snapshots but never a second counted balance. Each account carries
   `banking.AccountFreshness` (fresh, stale, failed, reconnect required, disconnected, never refreshed; D40's 24 h
   default) and each currency line counts its stale balances and gives the oldest `obtained_at`. A stale balance is still
   counted, but always shown as stale. `balance_less_still_due` is computed only in one currency and only when that
   currency has a counted balance.
5. **Who sees balances.** Balances, accounts and their freshness need `connections:read` (the administrator grant B4
   introduces for bank connections, accounts, balances and freshness) in addition to `commitments:read`. Without it the
   outlook compares commitments only and says why. The resource is added here with the same name, text and admin-only
   flag as in PR #77, so whichever lands second merges without a semantic conflict.
6. **Converted aggregate (D5).** Only when a reporting currency is given (`?currency=`, else `reporting.currency`, a new
   optional configuration key). Each currency line is converted as a whole at the latest rate (`fx.Service.Convert`,
   rounding half-even to the target's minor units), so there is one rounding per figure and currency. A currency whose
   rate is stale or missing, or whose conversion fails, is excluded entirely (its balance and its commitments together,
   never half) and listed in `excluded` with the status, reason and, for a stale rate, the rate found; `complete` is false
   then. Included currencies list the rate used with provider, source, pair, value, rate date, retrieval time and age. The
   outlook is computed on every read and stores nothing, so the rates it reports are those it used.
7. **No rate service is wired yet.** Until FX2 provides the Frankfurter adapter, `ledger serve` passes no converter: only
   amounts already in the reporting currency convert (identity) and every other currency is excluded with
   `no_provider`. This is visible in the response and the UI rather than hidden. FX2 sets `httpapi.Deps.Rates` to its
   `*fx.Service`.
8. **Wording and UI (proposed).** Every response carries `notice`: "Outlook against recorded commitments: bank balances
   compared with the commitments Ledger has recorded as still due. It covers recorded commitments only, is not a forecast
   and does not tell you how much money is safe to spend." The `/outlook` page shows the notice first, the per-currency
   cards next (bank balance, commitments still due, of which overdue and upcoming, balance minus commitments still due),
   the converted aggregate below them labelled "secondary" with an "Incomplete" badge and the excluded currencies, then
   the accounts with their freshness. The through-month moves with Earlier/Later. Client-side staleness and a failed
   refetch reuse the month view's behaviour (ADR-0034 point 4). Planned outflows (task S8) are not part of Ledger yet:
   `planned_outflows.included` is false and the page says so.

9. **Follow-ups from the product review (FX3c).** The bank balance is shown exactly as the bank reported it and is never
   adjusted. Each currency line carries `balance_as_of` (the oldest counted balance's reference date, or the date, in the
   reporting time zone, on which it was obtained when the bank gave none) and `payments_after_balance` (count and amount
   of payment evidence in that currency dated strictly after `balance_as_of`: a payment dated on that day is not counted;
   a caveat that such payments are off "still due" but may not be in the balance yet, never netted in). `overdue_months` lists the months the overdue part comes from, for links to the monthly view;
   `window.not_recorded_count` counts occurrences left out because they fell due before their commitment was recorded;
   `balances.stale_after_seconds` is the effective `banking.stale_after`. A not-fresh count names its states ("1 needs
   reconnect", "1 disconnected") and says "not refreshed in 24 h" only when every such account is stale.
   When nothing besides the reporting currency converts, the page shows only the explanation, not repeated figures.
   `ledger serve` now shares its bank-facts store with the outlook and passes `banking.stale_after` as
   `BankStaleAfter`.

## Consequences

- No migration. New: `internal/domain/outlook`, `internal/httpapi/outlook.go`, the OpenAPI path and schemas
  (`Outlook`, `OutlookCurrency`, `OutlookAccount`, `OutlookConverted`, `ExchangeRate`, `SignedMoney`, since balances
  and differences may be negative), `reporting.currency`, the `connections` grant resource, the `/outlook` page and nav
  entry. The Go client lists `getOutlook` as not implemented until a CLI or MCP view needs it.
- No audit entries: the outlook only reads.
- Open for the operator: the 240-month bound (3); the balance-type preference and counting stale balances (4); whether the
  converted aggregate should also exist without balances (6); the wording and layout (8, product-designer review
  pending). D40's configurable `banking.stale_after` arrives with B4; until then the 24 h default applies
  (`httpapi.Deps.BankStaleAfter` is the seam).
- Not done: planned outflows (S8/DF3); a converted figure is not computed at a due date's rate (D5 chose latest);
  Playwright journey (U3); manual screen-reader and real-device checks.
