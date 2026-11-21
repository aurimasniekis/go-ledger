# ADR-0031: Payment state, manual payments and status overrides

Status: Proposed (2026-11-21). Points 3, 4 (the meaning of "due"), 6, 7, 8, 9 and 10 are implementation choices that
no operator decision covers. They are marked "proposed" and listed for the operator under Consequences.

## Context

Task C3 adds payment state to the commitment occurrences of C2 (ADR-0027): what is paid, partially paid, upcoming,
due and overdue, the manual payments a user records, and a manual status change that can be undone. These operator
decisions apply (work/decisions.md):

- D7 (a)/(a): status is **derived** (paid / partially paid / upcoming / due / overdue) from links, manual records and
  dates, plus a **stored manual override with a reason**. Overdue counts from the **original due date** and is
  annotated "within documented grace until X" when a grace period is documented. It never hides an obligation.
- D8 (a) throughout: an **allocation table** lets one transaction pay several occurrences and an occurrence be paid in
  parts. Auto-matching is 1:1 only. A reversal reverses the link and reopens the occurrence with an audit entry.
- D5: amounts are exact decimals in their own currency; "today" is the date in the configured reporting zone.
- ADR-0013: bank facts are kept apart from user data. ADR-0025: finance-correcting changes are audited.

The grace length belongs to task DF2 (leeway, blocked on D14), and bank-transaction links to task X1. C3 leaves seams
for both and builds neither.

## Decision

1. **Status is never stored.** `payments.Derive` computes it on every read from the occurrence, its *facts* (the
   payment evidence and the active override) and today's date in the reporting zone. Only evidence in the occurrence's
   currency counts towards its amount: **paid** when the sum covers the amount, **partially_paid** when it is above
   zero and below the amount, otherwise the date decides. Evidence in another currency is listed in
   `unconverted_currencies` and is never converted or summed (no conversion policy applies here, D5).
2. **Evidence** is a manual payment record (built here) or, from X1, a confirmed allocation of a bank transaction to
   the occurrence. Each piece is one row per occurrence with its own allocated amount (`payments.Evidence`), which is
   the D8 (a) shape: a transaction split over three occurrences is three pieces, a part payment is a piece below the
   amount. `internal/store/payments.EvidenceSources` lists one query per evidence table and
   `internal/store/occurrences.DefaultEvidence` the matching "is paid" expression that C2's regeneration and the
   commitment delete use. X1 adds one entry to each; `TestEvidenceSourcesMatchDefaultEvidence` fails when they drift.
   Any evidence makes an occurrence immutable (partially paid ones included, as ADR-0027 point 8 requires) and blocks
   deleting its commitment (`commitments.ErrInUse`, 409), because the evidence tables refer to occurrences with
   `ON DELETE RESTRICT`.
3. **Proposed: manual payment records** (`manual_payments`, migration 0014) hold an exact positive amount with its
   currency, the date paid and an optional note. They are user data, separate from bank facts and from match links.
   The amount must be in the occurrence's currency (refused with 400 otherwise, never converted), and `paid_on` must
   not be after today in the reporting zone (a planned payment is not a payment; DF1 deferrals cover plans).
   Records are not edited: a mistake is deleted (with the record's `If-Match`) and recorded again, so the history
   in the audit log stays simple. Recording needs no version, like every create (ADR-0011).
4. **Timing.** Overdue counts from the original due date: an occurrence is overdue from the day after `due_date`,
   whatever grace exists. **Proposed:** upcoming means due after today and **due means due today** (in the reporting
   zone). A wider "due soon" window would need a length nobody has decided. A partially paid occurrence also carries
   the timing of its unpaid remainder (`timing`), so a part payment never hides that the rest is overdue.
5. **Grace is an annotation hook.** `payments.GraceFunc` returns the end of a documented grace period for an
   occurrence; `httpapi.Deps.Grace` passes it in, and it is nil until DF2 records leeway. When it returns a date, an
   overdue occurrence gets `grace_until` and `within_grace` (true while today is not after it). The status stays
   overdue. There is no default grace length.
6. **Proposed: the override says `paid` or `unpaid`, with a required reason** (1 to 500 characters).
   - `paid` settles an occurrence the counted evidence does not cover: a payment in another currency, or a creditor
     accepting less. It is refused (409 `payment_evidence_required`) when the occurrence has no evidence at all, and
     it has no effect (`override.applied` false) if its evidence is later deleted, so **no occurrence is ever paid
     without evidence**.
   - `unpaid` says the evidence does not settle the occurrence (a transfer bounced): the status follows the dates,
     and `outstanding_amount` is the **full occurrence amount** (not amount less evidence), so it is never zero
     while the status is not paid. `paid_amount` still shows the evidence.
   - Other values (waived, skipped) were not added: D7 lists the statuses, and a status that removes an obligation
     needs an operator decision.
7. **Proposed: undo restores the previous state exactly.** `occurrence_status_overrides` is a history: setting an
   override supersedes the active one and remembers it in `previous_id`; undoing (`DELETE .../status-override`)
   marks the active one undone and makes the previous one active again, with its own reason. Repeating the undo walks
   further back until no override is left and the derived status applies. A partial unique index allows one active
   override per occurrence. Overrides refer to occurrences with `ON DELETE CASCADE`: an override is not evidence,
   so it does not stop C2 from rewriting or removing an unpaid occurrence.
8. **Proposed: the occurrence's version covers its payment state.** Every payment-state change (record or delete a
   payment, set or undo an override) increases the occurrence's version in the same transaction, so its ETag changes
   when its status can. The override endpoints take the occurrence's version (`If-Match` or the body's `version`,
   ADR-0011/0015); a stale one is 409 with the current occurrence as reconciliation. The override table therefore
   has no version of its own and is listed in `unversionedTables`.
9. **Proposed: API and grants.** `GET /occurrences` and `GET /occurrences/{id}` carry a `payment` object (status,
   derived_status, timing, paid_amount, outstanding_amount, unconverted_currencies, evidence_count, grace_until,
   within_grace, override). New routes: `GET`/`POST /occurrences/{id}/manual-payments`,
   `GET`/`DELETE /occurrences/{id}/manual-payments/{payment_id}`, `PUT`/`DELETE /occurrences/{id}/status-override`.
   They use `commitments:read` and `commitments:write`: payments are decisions about commitment occurrences, and no
   new grant was added to the ADR-0021 vocabulary. A separate `payments` grant can be split off later without
   changing the routes.
10. **Audit.** Four actions, registered in `audit.Definitions`, each targeting the occurrence:
    `manual_payment.recorded` and `manual_payment.deleted` (payment_id, currency),
    `occurrence.status_overridden` (status, previous: none/paid/unpaid) and
    `occurrence.status_override_undone` (undone, restored). **Proposed:** amounts, notes and reasons are not copied into
    the audit log (ADR-0025 keeps details to short fixed values); they stay in their own tables. The existing
    `correction.recorded` / `correction.reversed` actions are left for X1's match corrections.

## Why

- Deriving status from evidence (D7 a) means there is no stored status to drift from the facts: removing evidence
  reopens the occurrence by itself, which is also what X1's reversal needs (D8 a).
- Keeping only same-currency evidence in the sum, and listing the rest, follows "never combine currencies without an
  explicit conversion policy". The `paid` override is the explicit, reasoned, audited way for the user to settle a
  foreign-currency payment.
- A history table makes the undo exact for any sequence of overrides, keeps every reason, and costs little.
- Bumping the occurrence's version gives one concurrency token for everything that can change what a client shows
  for the occurrence.

## Consequences

- **For the operator (proposed, not decided):** "due" means the due day itself (point 4); the override values
  `paid`/`unpaid` and the evidence requirement for `paid` (point 6); undo as a walk back through the override
  history (point 7); payment-state changes increasing the occurrence version (point 8); manual payments in the
  occurrence's currency only, not after today, never edited (point 3); the commitments grants (point 9); no amounts,
  notes or reasons in the audit log (point 10). Overpayments are visible as `overpaid_amount` (paid less the amount when
  positive, else zero; informational, it does not change the status). Duplicate manual payments are not detected:
  two identical records both count and both are listed. How duplicate payments are handled is still an open question for the operator.
- **For X1:** add the allocation table (one row per transaction and occurrence, allocated amount and currency,
  `ON DELETE RESTRICT` to the occurrence), its confirmed-allocation query to `EvidenceSources` and its expression to
  `DefaultEvidence`, a new `payments.EvidenceKind`, and the occurrence version bump on every link change. A bank
  transaction in another currency than the occurrence then shows up in `unconverted_currencies` without further
  work.
- **For DF2:** fill `httpapi.Deps.Grace` from the recorded leeway. **For FX3/C4:** read `payment.status`,
  `timing` and `outstanding_amount`; totals must stay per currency.
- Partial payments are compared exactly (`money.Decimal`), scale included in the output: 20.00 + 30.005 is 50.005.
- **Migration number:** written as `0012_payment_state`, renumbered to `0014_payment_state` at merge because A4
  (`0012_api_tokens`) and A3 (`0013_totp`) landed first. `0013_totp` is the fixture of the previous version (added at
  merge).
- No UI (task C4) and no MCP tool here.
