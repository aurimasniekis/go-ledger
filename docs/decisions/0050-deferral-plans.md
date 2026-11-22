# ADR-0050: Deferral plans

Status: Accepted (task DF1, 2026-11-22). D7 (a)/(a) and D14 (a),(a),(a) are the operator's decisions. The points marked
**proposed default** are this ADR's own choices within them; they are not operator decisions and are listed again under
"Open questions" so a different choice can amend them.

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
   `adjustments`. Each row carries its active plan as `deferral`, and occurrences of earlier months planned into the
   month are listed apart as `carried_in` with their own month and due date, never merged with the month's own
   occurrence. A carried-in currency gets its own totals entry; currencies are never combined.
4. **One active plan per occurrence** (partial unique index). Revising (`PATCH`, JSON Merge Patch of `plan_type`,
   `planned_date`, `reason`) keeps the plan's identity and appends to its history; undoing (`POST …/undo`, optional
   reason) ends it, after which it never changes (trigger) and the occurrence can be deferred anew with a new plan.
5. **History** (`deferral_plan_events`, append-only): every change with the plan's type and planned date after it, the
   plan version it produced, who made it and how (session, token, mcp; D42 3a) and the reason recorded with it.
6. **Fees and interest are explicit** (`deferral_plan_adjustments`): kind `fee`, `interest` or `other`, a positive
   amount in the plan's currency, an optional note. Removing one keeps it as history. A plan never adds one by itself.
7. **Checks.** A plan needs an occurrence that owes something now (409 `nothing_outstanding` when paid, or "not
   recorded" under D38 1a; 409 `occurrence_cancelled`), no active plan (409 `deferral_plan_exists`), and a planned
   date after the occurrence's due date and within the materialisation horizon (400 on `planned_date`). Revision
   re-checks against the occurrence as it is now. Undone plans refuse changes (409 `deferral_plan_undone`); removing an
   adjustment twice is 409 `deferral_adjustment_removed`.
8. **Occurrence lifecycle.** A plan (active or undone) is in `occurrences.DefaultReferences`: when a terms edit or a
   commitment delete no longer schedules a deferred occurrence, it is cancelled with reason (D31) instead of deleted, so
   the plan stays readable; a cancelled occurrence owes nothing and plans nothing. A plan does not freeze the occurrence:
   a terms edit may still rewrite an unpaid deferred occurrence in place (the plan's original_* fields keep what was
   deferred).
9. **API, client, MCP.** `/api/v1/deferral-plans` (list with `occurrence_id`, `commitment_id`, `state`,
   `planned_month`; create), `/{id}` (get, PATCH), `/{id}/undo`, `/{id}/history`, `/{id}/adjustments` (POST) and
   `/{id}/adjustments/{adjustment_id}` (DELETE). Reads need `commitments:read`, writes `commitments:write` (plans are
   decisions about occurrences, like payments and matches). Writes need the plan's version (If-Match or body). The Go
   client has the matching methods; the MCP server has eight tools (`undo_deferral_plan` and
   `remove_deferral_adjustment` are marked destructive). Audit: `deferral_plan.created`, `.revised` (changed field
   names), `.undone`, `.adjustment_added`, `.adjustment_removed`, with the occurrence, plan type, planned date,
   currency and provenance, never a reason, note or amount.
10. **Documented grace.** An active `documented_grace` plan reports `grace_until` = its planned date and
    `within_grace` while the occurrence is overdue and today is not after it, on the plan (and so on the month row's
    `deferral`). The payment state's own `grace_until` stays the hook for commitment-level leeway (task DF2).

## Proposed defaults (not operator decisions)

- The planned date must be strictly after the due date; a planned date already in the past is accepted (a plan recorded
  late), and nothing yet marks a plan whose date passed while the occurrence still owes something.
- Adjustments count in the planned total only while the occurrence still owes something; a fully paid deferred
  occurrence plans nothing, fees included. Adjustments are positive only: a reduction is not modelled (a creditor
  accepting less stays a "paid" status override, ADR-0031).
- A plan defers the whole remaining outstanding amount. Paying part of it is a partial payment through the existing
  evidence (manual payment or allocation, D14 a / D8 a); the remainder stays planned on the plan's date. Splitting one
  occurrence into several dated instalments is not built.
- Undo ends the plan; it does not step back one revision.
- A revision is refused once the occurrence owes nothing; an undo is always allowed on an active plan.

## Consequences

- The obligation views (status, outstanding, overdue, outlook "still due") are unchanged by plans; only the new planned
  figures move. Re-imports never touch plans (they do not read or write bank facts), and later matching pays the
  occurrence through the usual evidence while the plan stays as history (tests in `internal/store/deferrals`).
- The outlook (ADR-0044) does not use plans yet: placing planned outflows by month there is task DF3.
- Automatic matching still uses the due date for its date window (D9): a payment made on a planned date far from the due
  date becomes a suggestion for a person, never an automatic confirmation.

## What the UI needs (task DF3)

Defer, revise and undo flows on a month row (plan type with neutral wording, planned date, optional reason; If-Match
from the row's `deferral.version`); show the original due date, overdue status, planned date and "within documented grace
until X" distinctly without relying on colour; render `carried_in` apart from the month's own occurrences; show
`planned`, `deferred_out`, `carried_in` and `adjustments` beside the obligation totals; add and remove fees; show a
plan's history; the e2e journey: defer within a month, carry to the next month, undo, re-import, match.

## Open questions

The proposed defaults above; whether a plan should mark itself missed when its date passes; whether automatic matching
should also consider the planned date; whether the outlook's planned outflows should follow plans (DF3).
