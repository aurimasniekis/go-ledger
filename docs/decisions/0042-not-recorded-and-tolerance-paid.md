# ADR-0042: "Not recorded" occurrences and tolerance-confirmed payments count as paid

Status: Accepted (2026-11-21; operator decision D41 confirmed points 1 and 3). Implements operator decisions D38 1a and D39 5a on top of ADR-0031 (payment state),
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
3. **Tolerance counts as paid, from what was recorded at confirmation.** When a bank match is confirmed (created
   confirmed, confirmed from a suggestion, or its amount corrected while confirmed), the store checks, inside the write
   transaction, whether the occurrence's counted evidence with this match is **confirmed bank matches alone** (no
   manual payment), in the occurrence's currency, short of its amount by no more than the commitment's positive
   `match_tolerance` (inclusive). If so it records that tolerance on the match (`tolerance_amount`,
   `tolerance_currency`, migration 0021); otherwise the columns are NULL. The derived status is `paid`,
   `outstanding_amount` zero and `shortfall_amount` the gap when the counted evidence is bank matches only, short of
   the amount, and a recorded tolerance (the smallest among the matches) covers the gap. This is the conservative reading
   of D39 5a ("confirmed within tolerance"): a **mixed** manual and bank payment stays `partially_paid`, a bank match in
   another currency is never counted (so it cannot turn a manual partial payment into paid), a larger gap changes
   nothing, and a payment above the amount is unaffected. An "unpaid" override still wins; the shortfall stays visible.
   Bank facts are not touched. Because the state reads the recorded tolerance, never the commitment's current one,
   editing or clearing a tolerance later does not change past occurrences, and a tolerance set later does not rescue a
   past short match. Existing matches have none recorded.
4. **Facts.** `payments.Facts` gains `CommitmentCreatedOn`; each bank `Evidence` carries `ConfirmedWithin` (the
   recorded tolerance). The payments store reads them with the evidence, so every reader derives the same state. The
   reporting zone for the creation date is injected into the payments and matches stores
   (`WithZone`, applied where the API is registered; UTC when unset); there is no package global.
5. **API, client, UI.** `PaymentStatus` and `PaymentState.timing` gain `not_recorded`; `PaymentState` gains
   `shortfall_amount`. The Go client gets `PaymentNotRecorded` and `ShortfallAmount`. The month row shows a muted
   "Not recorded" badge (icon and text, never the overdue look) with the sentence "Due before this commitment was
   created…", keeps "Record payment", and shows "Paid within the commitment's tolerance; X short".

## Consequences

- A commitment created today with a start date in the past shows its earlier unpaid occurrences as "Not recorded". The
  user can mark any of them paid.
- Editing a commitment does not change its creation date, and editing its tolerance does not change past occurrences.
- Migration 0021 is numbered after 0020 on the base branch; the open PR that adds a migration 0021 for bank consent
  origin means whichever merges second renumbers its file (migrations must have no gaps).
- Open: whether the shortfall should be added to the totals row; it is not (the tolerance is the user's own, per
  commitment).

### Edge cases

These follow from point 3 as written. They are stated here so that they are not mistaken for bugs.

a. **A manual payment after a tolerance-paid bank match reverts it to `partially_paid`.** The tolerance applies only to
   bank matches alone, so once a manual payment is counted the occurrence is judged on the mixed evidence. Example: a 50.00
   EUR occurrence, a bank match of 49.70 EUR with 0.50 EUR recorded, then a manual payment of 0.10 EUR gives
   `partially_paid` with 0.20 EUR outstanding. Adding money can make the occurrence look less paid. This is deliberate:
   the outstanding amount stays visible rather than being covered by the tolerance.
b. **A match confirmed alongside manual evidence records no tolerance.** Point 3 records the tolerance only when the
   counted evidence is bank matches alone at confirmation. If the manual payment is removed later, the occurrence stays
   `partially_paid`: the tolerance is not re-derived. Re-recording it needs a Correct of the confirmed match (the amount is
   checked again at that point, and the tolerance is recorded if the bank-only rule now holds).
c. **Matches confirmed before migration 0021 stay `partially_paid`, with no backfill.** Their `tolerance_amount` is NULL,
   including automatic confirmations that were short within the tolerance before this change. They are neither rewritten
   to paid nor to unpaid. Consequence: an older short auto-confirmed match shows its small outstanding amount, while a new
   one shows paid. A backfill from the automatic-confirmation reason text is possible but not recommended; it would need a
   separate operator decision.
