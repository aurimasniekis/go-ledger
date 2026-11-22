# ADR-0056: Deferral planning UI and the outlook's planned outflows

Status: Accepted (task DF3, 2026-11-22). Builds on ADR-0050 and the operator decisions D-DF and D-DF-b. The wording and layout
choices below are this task's own, within them; none is an operator decision.

## Decisions

1. **Neutral wording.** The screens say "Plan a later date", "Change plan", "Undo plan", "Planned catch-up date" and "Plan date
   passed, still owed". They never say late, failed or behind, and every form says that a plan records when the person plans to
   pay, marks nothing paid and leaves the original due date and overdue status as they are. The plan types read "My own plan",
   "Agreed with the creditor" and "Documented grace period", with a line saying Ledger records what the person says and checks
   none of it.
2. **Where it lives.** A plan is made, changed, undone and managed in place under the occurrence's row on the monthly view (the
   same pattern as recording a payment), for the month's own rows and for the "carried in" rows. A row shows its plan beside the
   payment status, never instead of it: original due date, the overdue sentence, the planned catch-up date and a documented
   grace period are separate labelled lines, each with its own icon, so none depends on colour. The new data state "Plan date
   passed" (calendar X, default tier) carries the missed flag; the reason (`principal` or `fee`) is in its words.
3. **Current and future month effects are said before saving.** The form names both months ("In November it stays owed ... In
   December it is planned, and listed under carried in from earlier months"). The totals show "Planned this month" beside
   outstanding, and, only when a plan is involved, deferred to a later month, carried in and fees still planned, with the
   formula in words. Nothing in the UI adds money: every figure is the server's.
4. **Writes wait for the server.** Plan writes use the shared save primitive without an optimistic change: "Not saved yet"
   while pending, a failure says nothing was changed and keeps what was typed. The version sent is read from the freshly
   loaded row, never kept from an earlier write, and a version conflict is explained. A plan write refreshes every month view
   and the outlook, because it moves an amount between months.
5. **Undo, and the fee rule (D-DF-b 1a).** A saved or changed plan gets a notice with "Undo this plan" (it reads the plan's
   current version first). While a fee is still planned "Undo plan" is disabled with the reason visible and linked to the
   button: mark the fee paid or remove it first. If the server still refuses (409 `deferral_adjustments_unpaid`), the same
   sentence is shown. Marking a fee paid is the person's own record: the screen says it is matched to no bank transaction and
   does not pay the occurrence.
6. **Carried-in rows are listed apart**, with their own month and original due date, and are never merged into the month's own
   list. They offer plan management; recording a payment stays on the occurrence's own month (a link says so), so a payment
   is never saved against a month other than the one it belongs to.
7. **The outlook follows plans (D-DF 8).** `GET /outlook` adds per currency `planned_outflows` = `{total, deferred_in,
   deferred_beyond, adjustments}` with total = still due − deferred_beyond + adjustments (ADR-0050 "Outlook"). Still due,
   overdue and upcoming and the balance comparison are unchanged by plans; the page shows the planned total beside them and
   names the deferred-in, deferred-beyond and fee amounts apart. The converted aggregate adds `planned_total`: each
   currency line's planned total converted with the same single rate and one half-even rounding as the line's still due and
   overdue (a currency without an acceptable rate is excluded from every converted figure together). The converted
   deferred-in, deferred-beyond and fee parts are not built. Each line also carries `missed_count` and `missed_outstanding`
   (plans whose date passed with something planned still unpaid, and their outstanding principal); a cancelled occurrence
   whose plan has an unpaid fee counts, with no principal. A currency whose only planned amount is a fee on a paid occurrence
   still gets a line.

## Limits

- The monthly view's status summary line is unchanged; it does not count plans.
- `deferred_beyond` can be understated for a truncated window (ADR-0050 / `window.truncated`).
- The outlook shows only the count and outstanding principal of missed plans; which plans, and whether the reason is
  principal or fee, are on the monthly rows.
- The end-to-end deferral journey uses next month's occurrence: with the real clock a past occurrence is "not recorded"
  (ADR-0047), so a plan on an overdue one is covered by the component and Go tests, not by the browser journey. The simulated
  bank serves one booked payment dated on the planned date, which is in the future: the journey relies on the import and the
  engine accepting that.
- Screenshots of the new panels were taken by the journey (`DEFERRAL_SHOTS_DIR`), but only the no-sideways-scrolling
  assertion at 390 px is automated; layout and contrast were not reviewed by eye by this task.
