# ADR-0042: "Not recorded" occurrences and tolerance-confirmed payments count as paid

Status: Proposed (2026-11-21). Implements operator decisions D38 1a and D39 5a on top of ADR-0031 (payment state),
ADR-0034 (monthly view) and ADR-0039 (match tolerance). Point 1 chooses between the two cut-offs D38 allows.

## Context

D38 1a: an unpaid occurrence due before the commitment's creation date (or before the first recorded payment) is shown
as a muted "Not recorded", not Overdue; it stays listed, can be marked paid and is left out of the overdue total.
D39 5a: an occurrence confirmed within the commitment's `match_tolerance` counts as paid; the shortfall is a detail.
The status is still derived on read (D7), never stored.

## Decision

1. **Cut-off: the commitment's creation date**, not the first recorded payment. The creation date is the date of the
   commitment's `created_at` in the reporting zone. An unpaid occurrence whose original due date is **before** it, and
   which would otherwise be overdue, has status and timing `not_recorded`. An occurrence due on the creation date or
   later is untouched, and upcoming or due occurrences are never affected. Why not the first payment: it would turn a
   genuinely late occurrence into "not recorded" as soon as a later one is paid, hiding a real obligation. A payment, a
   "paid" override or a partial payment still apply as before (a partly paid one has timing `not_recorded` for its
   remainder); an "unpaid" override keeps it `not_recorded`.
2. **Totals.** A `not_recorded` occurrence stays in the month list and in `due`/`occurrence_count`, but adds nothing to
   `outstanding`, `overdue` or `overdue_count`. Its own `outstanding_amount` still shows the full amount. No grace
   period is looked up for it.
3. **Tolerance counts as paid.** When the commitment has a positive `match_tolerance` in the occurrence's currency, the
   counted evidence includes at least one confirmed bank match, and the evidence total is below the amount by no more than
   the tolerance (inclusive), the derived status is `paid`, `outstanding_amount` is zero and the new
   `shortfall_amount` holds the gap (zero otherwise). The tolerance is short-only: a larger gap changes nothing, and a
   payment above the amount is not affected (it is "paid" with `overpaid_amount` as before). Manual partial payments
   without a bank match keep their behaviour (`partially_paid`). An "unpaid" override still wins; the shortfall stays
   visible. Bank facts and the match are not touched.
4. **Facts.** `payments.Facts` gains `CommitmentCreatedOn` and `MatchTolerance`, read with the evidence by the
   payments store (one join on the commitment), so every reader derives the same state. The reporting zone for the
   creation date is set once at start-up (`SetReportingZone`; UTC until then).
5. **API, client, UI.** `PaymentStatus` and `PaymentState.timing` gain `not_recorded`; `PaymentState` gains
   `shortfall_amount`. The Go client gets `PaymentNotRecorded` and `ShortfallAmount`. The month row shows a muted
   "Not recorded" badge (icon and text, never the overdue look) with the sentence "Due before this commitment was
   created…", keeps "Record payment", and shows "Paid within the commitment's tolerance; X short".

## Consequences

- A commitment created today with a start date in the past shows its earlier unpaid occurrences as "Not recorded". The
  user can mark any of them paid.
- Editing a commitment does not change its creation date.
- Open: whether the shortfall should be added to the totals row; it is not (the tolerance is the user's own, per
  commitment).
