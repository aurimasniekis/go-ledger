# ADR-0027: Commitment occurrences

Status: Proposed (2026-11-21). Points 2 (the interval anchor and the late-start first occurrence), 5, 6, 7, 9, 10, 11 and 12
and the start_date lower bound are implementation choices that no operator decision covers.
They are marked "proposed" and listed for the operator under Consequences.

## Context

Task C2 turns recurring commitment definitions (ADR-0024) into dated occurrences for each reporting month. The month
view (C4), payment state (C3), matching (X1/X2) and the outlook (FX3) all read these occurrences. These operator
decisions apply (work/decisions.md):

- D5: one configured IANA reporting time zone, captured at setup and stored (ADR-0010). Amounts are exact decimals
  in their own currency.
- D6: rules are monthly on day N, every N months, and annual on a date. A day past the end of a month clamps to the
  month's last day. An annual February 29 falls on February 28 in common years. Edits (option a) apply from an
  effective date forward and regenerate only unpaid future occurrences. Paid history is immutable.
- D7: payment status is derived from evidence (links, manual records) and dates. It is never stored on the
  occurrence.

ADR-0024 left C2 three jobs: reading schedules through `commitments.Schedule`, deciding the first occurrence when
`start_date`'s day comes after the scheduled day, and implementing D6 edits.

## Decision

1. **Occurrences are stored rows (migration 0010).** `commitment_occurrences` holds `commitment_id`, `month`
   (YYYY-MM), `due_date`, the exact `amount` and `currency`, timestamps and a `version`. It has no status
   column and no bank columns. `(commitment_id, month)` is unique, because every version 1 rule schedules at most
   one date a month. That month is the *slot* an edit rewrites in place. `due_date` always lies in `month`. The
   foreign key to `commitments` is `ON DELETE RESTRICT`.
2. **Calendar rules** (`occurrences.Terms.DueIn`, pure domain code):
   - **Proposed:** Monthly every N months: the months counted are those of `start_date`'s month, not of the first
     real occurrence.
   - Annual: once a year, in the schedule's month.
   - Day past month end: the month's last day (`calendar.ClampedDate`). So Feb 29 is Feb 28 in common years, and
     day 31 falls on Apr 30 and on Feb 28 or 29.
   - **Proposed: first occurrence:** a scheduled date before `start_date` is not an occurrence. With a start of January 20 and
     day 15, the first monthly occurrence is February 15. With every 3 months it is April 15, because the counted
     months still start in January. An annual schedule whose date has passed in the start year starts the next year.
     Example: start Jan 20, day 15, interval 3 gives Apr 15 as the first occurrence, nearly three months after the
     start. Anchoring on the first real occurrence would give Feb 15, May 15 and so on. D6 does not decide this.
   - `end_date` is inclusive.
   - Due dates are calendar dates in the reporting zone. They are not instants.
3. **Materialisation horizon.** `commitment_occurrence_horizons.generated_through` records, per commitment, the last
   month materialised. Every month up to it is stored, including months that hold nothing. Later months follow the
   definition's current terms when they are generated. Materialising is idempotent: months already materialised are
   never regenerated. The insert also has `ON CONFLICT DO NOTHING` as a guard.
4. **D6 edits** (`occurrences.PlanEdit`, run in the commitment's update transaction by
   `store/occurrences.ApplyTx`). An edit runs only when the amount, schedule, start or end date change. Name, kind,
   priority and notes don't affect occurrences. The steps:
   1. Materialise the **old** terms through the effective date's month, so every month before the effective date
      keeps the occurrences the terms in force then gave it. A definition edited before it was ever read keeps its
      history this way.
   2. For each month from the effective month through the horizon:
      - a **paid** occurrence is never changed;
      - an unpaid occurrence **due before the effective date** stays (it is history);
      - an unpaid occurrence due on or after it is **rewritten in place** (same id, version + 1) to the new date and
        amount, or **removed** when the new terms schedule nothing that month;
      - an empty month gets the new terms' occurrence when it falls due on or after the effective date.
   3. The change is audited as `occurrences.regenerated` with `effective_from` and the counts `created`,
      `updated`, `removed` and `kept_paid`. Amounts and names are never in the details.
5. **Proposed: the slot rule for a due day that moves across the effective date.** Example: the due day moves from
   the 20th to the 5th, effective on the 10th. That month's unpaid occurrence (old date the 20th, after the
   effective date) moves to the 5th instead of disappearing. When the day moves later (from the 5th to the 20th,
   effective on the 10th), the month keeps its 5th occurrence as history and gets no second occurrence. A pure
   due-date cutoff was rejected: in the first case it drops the month entirely (an obligation silently vanishes,
   against D7's "never hide an obligation"), and in the second it gives the month two occurrences.
6. **Proposed: the default effective date is today in the reporting zone.** The PATCH body may carry an
   `effective_from` control member, which is not stored on the commitment. Without it the change applies from today,
   so overdue unpaid occurrences keep their terms, as D6 option (a) requires. An earlier `effective_from` lets the
   user deliberately reach back over overdue unpaid occurrences. Paid ones are still never touched.
7. **Proposed: occurrences are materialised when a month is read.** `GET /api/v1/occurrences?month=YYYY-MM` first
   materialises every commitment through that month, in one write transaction. This is derived data, so it is not
   audited. Months and effective dates more than **60 months** after the current reporting month are refused, so a
   request cannot make the server store decades of rows (`occurrences.MaxMonthsAhead`).
8. **Payment evidence is a seam for C3.** `store/occurrences.DefaultEvidence` lists SQL conditions (over alias
   `o`) that make an occurrence paid. The list is empty until C3 adds manual payment records and confirmed
   allocations. C3's evidence tables must refer to `commitment_occurrences(id)` with `ON DELETE RESTRICT`, so the
   database also refuses to drop a paid occurrence. The tests stand in a `test_payments` table. A schema test
   (`TestOccurrenceReferencesAreEvidenceOrCascade`) fails when a foreign key to `commitment_occurrences(id)` is neither
   `CASCADE` nor `RESTRICT` and named in a `DefaultEvidence` expression, because a forgotten entry would let an edit
   rewrite a paid occurrence in place. C3 must treat a *partially paid* occurrence (D7) as evidence, so it is immutable too.
9. **Proposed: deleting a commitment removes its unpaid occurrences.** They are derived data. Paid occurrences
   (evidence) keep the foreign key, so the delete becomes 409 `conflict` (`commitments.ErrInUse`) and payment history
   is never lost. Setting `end_date` remains the way to stop a commitment.
10. **Proposed: API and grants.** `GET /api/v1/occurrences` (`month` is required and given once; `commitment_id`
    can be repeated; ordered by due date, then id; the cursor names the last id) and
    `GET /api/v1/occurrences/{id}` (with an ETag). Both need `commitments:read`: occurrences are derived from
    commitments, so no new grant was added to the ADR-0021 vocabulary. Occurrences have no write endpoint. Payment
    actions are C3.

11. **Proposed: a future-dated terms edit is protected.** A definition holds only the latest terms, so a terms edit with
    `effective_from` after today leaves the older terms of the months in between only in the stored rows. The edit
    records `commitment_occurrence_horizons.pending_effective_from`. While that date is after today, a terms edit
    whose effective date is earlier is refused with 409 `pending_terms_change` (it would rewrite those months with
    the future terms, or with an amount that was never in force then). This also covers a deliberate reach-back over
    them. An edit effective on or after the pending date supersedes it; once the date has passed nothing is pending.
    The alternative, refusing any `effective_from` after today, was rejected: a price change announced in advance is
    a normal case. A terms-revisions table (allowed by ADR-0024) would lift the restriction and is not built.
12. **Proposed: `start_date` is not before 1900-01-01** (C1 validation, `commitments.MinStartDate`), so the first read of a
    month cannot materialise tens of thousands of rows in one write transaction.

Audit: `occurrences.regenerated` also carries `kept_history` (unpaid occurrences before the effective date, left
unchanged), and `commitment.deleted` carries `unpaid_occurrences_removed`. A month read that needs no materialising
reads only; it takes the write lock only when some commitment is behind.

## Why

- Stored rows (rather than computing occurrences on every read) give occurrences stable identifiers that payment
  evidence and match links (C3, X1) can refer to. They also keep each past month as it was generated, which D6's
  immutable paid history needs.
- The horizon marker plus "materialise the old terms before applying an edit" make a separate terms-revision table
  unnecessary. The stored rows *are* the history of earlier terms. ADR-0024 allowed adding a revisions table. It was
  not needed.
- Rewriting an unpaid occurrence in place, rather than deleting and inserting, keeps its id stable for suggestions and
  links that refer to it.
- Regeneration runs in the commitment's write transaction, so a definition and its occurrences can never disagree
  after a crash.

## Consequences

- **For the operator (proposed, not decided):** the every-N-months anchor and the late-start first occurrence
  (point 2: start Jan 20, day 15, interval 3 gives Apr 15), the pending future-dated edit guard (point 11), the
  1900-01-01 lower bound on `start_date` (point 12), the slot rule (point 5), today as the default effective date
  (point 6), materialising on read with a 60-month horizon (point 7), deleting unpaid occurrences with the commitment
  (point 9), and reading occurrences under `commitments:read` (point 10).
- C3 must add its evidence conditions to `DefaultEvidence`, with `RESTRICT` foreign keys. Any other table that
  refers to occurrences (for example match suggestions) must either count as evidence or use `ON DELETE CASCADE`.
  Otherwise an edit that removes an unpaid occurrence fails on the foreign key.
- Changing the reporting zone (ADR-0010) does not move stored due dates. They are calendar dates. The zone decides
  only "today" (the default effective date and the horizon).
- When `start_date` moves earlier, months before the effective date are not filled in. That follows D6, because the
  edit applies from its effective date. Send an earlier `effective_from` to fill them.
- A page cursor whose occurrence an edit removed is refused with 400 `cursor`, and the client starts again.
- A commitment with a very old `start_date` materialises one row per scheduled month from that date on first read.
  The cost is bounded (at most 12 rows a year), and nothing is regenerated afterwards.
- Left for later tasks: payment status and manual payments (C3), the month view UI and MCP `list_occurrences` (C4),
  matching (X1/X2), the Terraform provider's `effective_from` handling, outbox events for occurrence changes, and
  the outlook (FX3).
- Migration number: written as 0010 because migrations must be contiguous for `make check`. If A2's migration lands
  first, renumber it at merge to 0011. That also needs a fixture for A2's version. `0009_commitments` is the fixture
  of the previous version added here.
