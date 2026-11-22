# ADR-0050: Deferral plans

Status: Accepted and decided (task DF1, extended by DF1c, 2026-11-22). D7 (a)/(a), D14 (a),(a),(a) and D-DF (all nine
recommendations, operator, 2026-11-22) are the operator's decisions; nothing in this ADR is provisional any more. The
missed-plan state (D-DF 1) and the fee paid marker (D-DF 2) were added by task DF1c (migration 0029). D-DF 7 (automatic
matching also accepts the planned date, plus or minus 3 days) and D-DF 8 (the outlook follows plans, showing deferred
amounts separately as "deferred in") are decided but are separate tasks: they are not built here, and the Consequences
below still describe the current behaviour of both.

## Context

The person must be able to defer an unpaid occurrence to a later planned date, within its month or into a later one,
inspect the effect on the current and later months, and revise or undo the plan with an optional reason. A deferral is
not payment, forgiveness or a creditor agreement, and it must never erase overdue history. The original obligation,
original due date, outstanding amount and revised plan stay distinct. A deferred occurrence stays separate from the next
normal occurrence: two outstanding occurrences of 50 EUR give a planned total of 100 EUR, before any explicitly recorded
fee, without counting anything twice.

D7: payment status is derived (ADR-0031); overdue counts from the original due date and a documented grace only
annotates it ("within documented grace until X"). D14: partial catch-up goes through allocations (D8 a, ADR-0035), leeway
is user-entered only, the priority scale has three levels (already in ADR-0024), fees and interest are explicit
adjustment entries, and a creditor-approved change is a distinct plan type.

## Decision

1. **A plan is its own entity** (migration 0025, `deferral_plans`), referring to one occurrence. It holds the plan type
   (`personal`, `creditor_agreed`, `documented_grace`), the planned date, an optional reason (1 to 500 characters),
   and, as history fixed when the plan is made, the occurrence's due date and amount and what it still owed then
   (`original_due_date`, `original_amount`, `outstanding_when_planned`). Triggers keep those and the occurrence
   reference immutable, keep the plan in the occurrence's currency and refuse deleting a plan.
2. **A plan is never payment.** No plan write touches an occurrence, a manual payment or a match, and the plan tables are
   neither an evidence source (`payments.EvidenceSources`) nor in `occurrences.DefaultEvidence`. The occurrence's
   status, outstanding amount and overdue status keep being derived from evidence and dates (D7): a deferred occurrence
   past its due date is overdue, and the plan's JSON says so (`overdue_since` = the original due date).
3. **Where an amount is planned.** Every occurrence that still owes something is planned in exactly one month: the month
   of its active plan's planned date, otherwise its own month (`deferrals.PlannedMonth`). The monthly view
   (`GET /occurrences/summary`, MCP `get_month_summary`) keeps its obligation figures unchanged (`due`, `paid`,
   `outstanding`, `overdue`) and adds per currency `planned` = `outstanding` − `deferred_out` + `carried_in` +
   `adjustments` (the fees still planned, point 6). Each row carries its active plan as `deferral`, and occurrences of earlier months planned into the
   month are listed apart as `carried_in` with their own month and due date, never merged with the month's own
   occurrence. A carried-in currency gets its own totals entry; currencies are never combined.
4. **One active plan per occurrence** (partial unique index). Revising (`PATCH`, JSON Merge Patch of `plan_type`,
   `planned_date`, `reason`) keeps the plan's identity and appends to its history; undoing (`POST …/undo`, optional
   reason) ends it, after which it never changes (trigger) and the occurrence can be deferred anew with a new plan.
5. **History** (`deferral_plan_events`, append-only): every change with the plan's type and planned date after it, the
   plan version it produced, who made it and how (session, token, mcp; D42 3a) and the reason recorded with it.
6. **Fees and interest are explicit** (`deferral_plan_adjustments`): kind `fee`, `interest` or `other`, a positive
   amount in the plan's currency, an optional note. Removing one keeps it as history. A plan never adds one by itself.
   **A recorded fee stays planned until it is itself marked paid or removed** (D-DF 2). It counts in `adjustments` and
   `planned` of the plan's planned month, under its own currency, whether or not the occurrence's principal is paid
   (even when the occurrence was cancelled), and it is never evidence for the occurrence: marking it paid does not
   touch the occurrence, and paying the occurrence does not touch the fee. The paid marker is `paid_at` plus who and how
   (`paid_by`, D42 3a): `POST …/adjustments/{adjustment_id}/paid` sets it, `POST …/adjustments/{adjustment_id}/unpaid`
   clears it again, so marking paid is reversible; both need the plan's version (a plan write: they bump it), the history
   gains `adjustment_paid` and `adjustment_unpaid`, and the audit log `deferral_plan.adjustment_paid` and
   `.adjustment_unpaid` (adjustment id and kind, never an amount or note). A removed adjustment cannot change; a paid one
   can still be removed. `adjustment_total` on a plan stays the sum of the adjustments not removed and
   `unpaid_adjustment_total` is the part still planned. **Undoing a plan is refused while a fee is still planned** (409
   `deferral_adjustments_unpaid`): the person marks it paid or removes it first, so a recorded fee can never silently drop
   out of the planned totals. (D-DF 6 says undo is always allowed; this is the one narrowing, flagged for the operator: the
   alternative, undoing and keeping orphaned fees on a plan that can no longer change, has no place to mark them paid.)
   **A missed plan is visible** (D-DF 1). `missed` is true on the plan (and so on a month row's `deferral`, in
   `carried_in` rows, in the list and in the MCP tools) while the plan is active, its planned date is before today in the
   reporting zone, the occurrence is overdue (its own due date has passed, so a due day moved later keeps it from being missed early) and it still owes something ("plan date passed, still owed"; `deferrals.Plan.Missed`). It is
   derived on every read from the plan and the occurrence's evidence, never stored and not payment state: it clears when
   the occurrence is paid, when the plan is revised to a later date and when the plan is undone, and a plan dated today is
   not missed yet. A plan never marks the occurrence paid or changes its overdue status, and a fee that is still planned
   does not make a plan missed (only the principal does).
7. **Checks.** A plan needs an occurrence that owes something now (409 `nothing_outstanding` when paid, or "not
   recorded" under D38 1a; 409 `occurrence_cancelled`), no active plan (409 `deferral_plan_exists`), and a planned
   date after the occurrence's due date and within the materialisation horizon (400 on `planned_date`). Revision
   re-checks against the occurrence as it is now. Undone plans refuse changes (409 `deferral_plan_undone`); removing an
   adjustment twice is 409 `deferral_adjustment_removed`, marking a paid one paid again 409 `deferral_adjustment_paid`,
   clearing an unpaid one 409 `deferral_adjustment_not_paid`, and undoing with a planned fee 409
   `deferral_adjustments_unpaid`.
8. **Occurrence lifecycle.** A plan (active or undone) is in `occurrences.DefaultReferences`: when a terms edit or a
   commitment delete no longer schedules a deferred occurrence, it is cancelled with reason (D31) instead of deleted, so
   the plan stays readable; a cancelled occurrence owes nothing and plans nothing. A plan does not freeze the occurrence:
   a terms edit may still rewrite an unpaid deferred occurrence in place (the plan's original_* fields keep what was
   deferred).
9. **API, client, MCP.** `/api/v1/deferral-plans` (list with `occurrence_id`, `commitment_id`, `state`,
   `planned_month`; create), `/{id}` (get, PATCH), `/{id}/undo`, `/{id}/history`, `/{id}/adjustments` (POST),
   `/{id}/adjustments/{adjustment_id}` (DELETE) and `/{id}/adjustments/{adjustment_id}/paid` and `/unpaid` (POST). Reads need `commitments:read`, writes `commitments:write` (plans are
   decisions about occurrences, like payments and matches). Writes need the plan's version (If-Match or body). The Go
   client has the matching methods; the MCP server has ten tools (`undo_deferral_plan` and
   `remove_deferral_adjustment` are marked destructive; `mark_deferral_adjustment_paid` and
   `unmark_deferral_adjustment_paid` are not, since the marker is reversible). Audit: `deferral_plan.created`, `.revised` (changed field
   names), `.undone`, `.adjustment_added`, `.adjustment_removed`, `.adjustment_paid`, `.adjustment_unpaid`, with the occurrence, plan type, planned date,
   currency and provenance, never a reason, note or amount.
10. **Documented grace.** An active `documented_grace` plan reports `grace_until` = its planned date and
    `within_grace` while the occurrence is overdue and today is not after it, on the plan (and so on the month row's
    `deferral`). The payment state's own `grace_until` stays the hook for commitment-level leeway (task DF2).

## Decided defaults (D-DF, operator, 2026-11-22)

All nine recommendations were accepted. Items 7 and 8 are decided and built by later tasks.

1. The planned date must be strictly after the due date; a planned date already in the past is accepted (a plan recorded
   late). A plan whose date has passed while the occurrence still owes something is visibly flagged (point 6).
2. A recorded fee stays planned until it is marked paid or removed, with its own paid marker (point 6). This replaces the
   earlier default that a fee counted only while the occurrence still owed something.
3. Adjustments are positive only: a reduction is not modelled (a creditor accepting less stays a "paid" status override,
   ADR-0031).
4. A plan defers the whole remaining outstanding amount to one date. Paying part of it is a partial payment through the
   existing evidence (manual payment or allocation, D14 a / D8 a); the remainder stays planned on the plan's date.
   Instalments are not built in the initial build.
5. Undo ends the plan; it does not step back one revision, and the history keeps every revision.
6. A revision is refused once the occurrence owes nothing; an undo is allowed on an active plan, with a narrowing:
   undo is refused while a fee is still planned (Decision point 6 above), so the person settles it first.
7. Automatic matching also accepts the planned date, plus or minus 3 days, for auto-confirm, in addition to the D9
   due-date window. Not built by this task.
8. The outlook follows plans, showing deferred amounts separately as "deferred in". Not built by this task.
9. A commitment terms edit that would change the currency of an occurrence with an active plan is refused whole with 409
   `deferral_plan_currency` naming the occurrence and the plan; nothing changes. The plan's fees are in the occurrence's
   currency, so converting them silently is not an option; the person undoes the plan (settling its fees first), edits,
   and defers again. The monthly view still adds each adjustment under its own currency, so a fee can never vanish from
   the totals.

`overdue_since` is the occurrence's current due date (a terms edit can move it); `original_due_date` stays the due date
when the plan was made.

## Consequences

- The obligation views (status, outstanding, overdue, outlook "still due") are unchanged by plans; only the new planned
  figures move. Re-imports never touch plans (they do not read or write bank facts), and later matching pays the
  occurrence through the usual evidence while the plan stays as history (tests in `internal/store/deferrals`).
- The outlook (ADR-0044) does not use plans yet: placing planned outflows by month there is task DF3.
- Automatic matching still uses the due date for its date window (D9): a payment made on a planned date far from the due
  date becomes a suggestion for a person, never an automatic confirmation, until D-DF 7 is built.
- A fee marked paid is the person's statement, not bank evidence: this feature matches no bank transaction to it.

## What the UI needs (task DF3)

Defer, revise and undo flows on a month row (plan type with neutral wording, planned date, optional reason; If-Match
from the row's `deferral.version`); show the original due date, overdue status, planned date and "within documented grace
until X" distinctly without relying on colour; render `carried_in` apart from the month's own occurrences; show
`planned`, `deferred_out`, `carried_in` and `adjustments` beside the obligation totals; add and remove fees; show a
plan's history, the missed flag ("plan date passed, still owed", not by colour alone) and each fee's paid marker with
mark-paid and undo-paid actions; the e2e journey: defer within a month, carry to the next month, undo, re-import, match.

## Open questions

None from the operator's decisions. Details this ADR chose within them that a later decision could amend: undo being
refused while a fee is still planned (point 6), and a fee on a plan whose occurrence was cancelled still counting as
planned (the fee is its own charge).
