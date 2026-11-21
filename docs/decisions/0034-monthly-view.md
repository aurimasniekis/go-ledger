# ADR-0034: The monthly view

Status: Proposed (2026-11-21). Points 3, 5, 6 and 8 are implementation choices that no operator decision covers; they are
listed for the operator under Consequences.

## Context

Task C4 gives the user the primary outcome of Ledger: a trustworthy monthly view of paid, upcoming and overdue
commitments, with totals and visible data freshness. It builds on occurrences (ADR-0027) and derived payment state
(ADR-0031, decision D7). These constraints apply: amounts are exact decimals in their own currency and are never summed
across currencies or computed in TypeScript (ADR-0005, D5); the view must never present totals as money that is safe to
spend; stale, failed and pending-save states are visible; a payment change is never shown as saved before the server has
confirmed it.

## Decision

1. **Server-side summary.** `GET /api/v1/occurrences/summary?month=YYYY-MM` (grant `commitments:read`, like the
   occurrence list) returns every occurrence of the month with its payment state and commitment name, `totals` per
   currency and `freshness`. It is not paged: a month holds one row per commitment due in it. The read logic is the
   transport-free `internal/domain/monthview` service, which the MCP `list_occurrences` tool (P-MCP2) is meant to call;
   no MCP server is built here (P-MCP1 does not exist yet).
2. **Totals** per currency, computed in Go with `money.Totals`: `due` (sum of amounts), `paid` (sum of evidence in that
   currency), `outstanding` (sum of each occurrence's outstanding amount from `payments.Derive`), `overdue` (the
   outstanding amount of occurrences past their original due date, grace period or not: D7 says overdue counts from the
   original due date and never hides an obligation), `overpaid` (informational). There is no cross-currency figure.
   Evidence in another currency is never converted and is counted nowhere; the response counts the occurrences that have
   it so the UI can say so. `paid` can differ from `due - outstanding` when a manual "paid" override settles an
   occurrence the evidence does not cover; the override is visible on the row.
3. **Freshness (proposed).** `computed_at` (statuses are derived on every read), `as_of` (today in the reporting zone),
   `occurrences_changed_at` (newest `updated_at` among the month's occurrences) and `bank`. **Bank data age is not
   available:** the refresh and freshness facts of B2 (`banking.AccountFreshness`) are not exposed over HTTP yet, so
   `bank.available` is always `false` with a note, and the UI says "Bank data age: not shown" rather than implying data is
   fresh. When a bank-connection endpoint exists, `bank` carries the worst account freshness state and oldest last-success.
4. **Client-side staleness.** The UI labels data it fetched more than 5 minutes ago as "Stale, fetched N minutes ago", and
   a failed refetch keeps the last data on screen under a "Refresh failed" alert that gives the time of the data. The
   threshold is a placeholder: freshness thresholds are an unresolved operator decision.
5. **Primary action (proposed).** The screen's one primary action is "Record a payment" (manual payment, C3 API). The
   form suggests the outstanding amount as the server computed it. The request is not optimistic: while it is out the
   form shows "Not saved yet"; a failure says "Payment not saved", keeps what was typed and changes nothing; success shows
   "Saved" with an undo that deletes the manual payment record (`If-Match` with its version) and is itself confirmed by
   the server. Rows are refetched after either.
6. **Month and zone (proposed).** The month is in the address (`/month?month=YYYY-MM`). Without one, the UI uses the
   current month in the browser's time zone, which can differ from the reporting zone near a month boundary; the page
   shows "as of" in the reporting zone. Reading a month beyond 60 months ahead is refused by the server as for the list.
7. **Status display.** Every status is an icon plus text (WCAG 1.4.1). `due` ("Due today") and `partially_paid`
   ("Partly paid") were added to the commitment statuses and `pending` ("Not saved yet") to the data states, reusing
   existing hues (the design-system contrast and distance tests are unchanged); the icon and the label tell them apart.
   "Overdue" with a documented grace period reads "within documented grace until X" and stays overdue.
8. **Wording.** The totals section says the totals cover recorded commitments and recorded payments only and are not a
   forecast or a measure of money that is safe to spend. The outlook against bank balances is a later task (FX3, DF3).
9. **CSRF.** Writes from the browser send `X-CSRF-Token` from a small in-memory holder (`web/src/lib/csrf.ts`) that the
   sign-in flow (A7) must fill with the token of the session response. Until it does, a session-cookie write is refused
   by the server (bearer-token requests are exempt).

## Consequences

- No migration. The OpenAPI document and the permission matrix gain the summary route; the Go client lists it in
  `notImplemented` until P-MCP2 / a CLI view needs a typed method.
- Open for the operator: the freshness thresholds (4), whether record-payment is the right primary action (5), the month
  default and zone (6), the hue reuse for due / partly paid (7).
- Not done: a Playwright journey (U3 owns the end-to-end main journey); screen-reader and real-device manual checks.
