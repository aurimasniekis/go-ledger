# ADR-0049: Confirm a bank match as replacing a manual payment

Status: Accepted (task X5, 2026-11-21). D45 is the operator's decision. What happens to replaced records on unmatch and
reversal (points 4 and 5) is not covered by D45: those two are this ADR's proposed defaults, which control accepted for
now and has put to the operator; a different operator choice would amend them. The amendment "Replacement follow-ups"
(task X5c) adds a further proposed default (a replacement on hold while the match does not count) and three fixes.

## Context

An occurrence the person already marked paid with a manual payment record often gets a matching bank transaction later.
Confirming the engine's suggestion then counts the same payment twice: it is refused as `occurrence_overpaid`, or, with
`allow_overpayment`, it shows an overpayment that never happened. Until now the only ways out were to delete the manual
record first (which loses the person's own entry) or to reject the suggestion. D45 (operator, "1a") adds "This is the
payment I recorded": confirm the bank match and mark the person's manual record as replaced by it, kept in history and
not deleted, no longer counting; the occurrence is then paid by bank evidence, and the change is undoable like a
confirmation (D44 window). D41 2a says the tolerance applies only to bank evidence alone; D42 3a says every match change
records how it came; D33 keeps automation away from a person's decisions.

## Decision

1. **The action.** `POST /api/v1/payment-matches/{id}/confirm-replacing` (grant `commitments:write`, as confirm;
   If-Match or body `version`) with `manual_payment_ids` (1 to 20 distinct identifiers), optional
   `allow_overpayment` and `reason`. It is a separate action, not a parameter of `confirm`, so a client that only knows
   `confirm` never replaces anything by accident and the permission matrix and MCP annotations stay per action. The Go
   client has `ConfirmPaymentMatchReplacing`; the MCP server has the tool `confirm_payment_match_replacing` (write, not
   destructive: it is undoable), under the same per-token rate limit as every MCP call.
2. **One write transaction.** The store method `ConfirmReplacing` checks the match version (409 `version_conflict`) and
   that the match is a suggestion (409 `invalid_match_state`), then for each record: it exists (400
   `validation_failed` on `manual_payment_ids`), belongs to the match's occurrence (409
   `manual_payment_other_occurrence`), is not deleted and not already replaced by another match (409
   `manual_payment_not_replaceable`), and is in the match's currency (400; amounts are never converted). It then marks
   the records replaced and runs the ordinary confirmation checks **against the evidence that remains**: the
   over-allocation check, the overpayment check (refused unless `allow_overpayment`) and the tolerance snapshot (D41 3a),
   which is taken only when bank matches alone remain (D41 2a). A record that stays (one the person did not list) keeps
   counting, so a mixed occurrence stays mixed. Any refusal rolls everything back. Automation is refused outright
   (`ErrActorReplace`, D33).
3. **Storage (migration 0024).** A new table `manual_payment_replacements` holds one row per record and replacement:
   the record, the match, the history entry of the confirmation that replaced it and when, and, once restored, the
   history entry that restored it and when. A row is active while `restored_at` is null; a partial unique index allows
   one active row per record. Triggers refuse a replacement by a match of another occurrence or currency, of a deleted
   record, or by a closed match; refuse any change but a single restoration; and refuse deletes. Nothing is deleted: the
   manual payment row is untouched, so its own history (`manual_payments`, `deleted_at`) is unchanged. The evidence
   queries (`payments.EvidenceSources` and `occurrences.DefaultEvidence`) leave out records with an active replacement;
   the confirmed match is the evidence instead, so an occurrence stays immutable and paid.
4. **Undo restores exactly; unmatch restores too (proposed default).** `unconfirm` inside the ADR-0045 window (D44)
   stamps every active replacement of the match with the unconfirm's history entry: the records count again and, with
   the match a suggestion again, the occurrence's evidence is exactly what it was before (the window guarantees nothing
   else changed it). `unmatch` (final: "this transaction did not pay the occurrence") restores them the same way: the
   claim that the bank payment was the recorded one falls with the match, and the person's own record stands again.
5. **Reversal keeps them replaced (proposed default).** `reverse` says the payment was reversed (bounced, refunded).
   The replaced records described that very payment, so they stay replaced and the occurrence reopens; the person can
   record a new payment.
6. **Visibility.** Replaced records stay in the manual payment list and reads with `replaced_by_match_id` set (null
   otherwise). In a match's history the confirmation entry lists `replaced_manual_payment_ids` and an unconfirm or
   unmatch entry `restored_manual_payment_ids` (both omitted when empty). The audit log records
   `payment_match.confirmed` as usual plus, per record, `manual_payment.replaced` and later `manual_payment.restored`
   (target the occurrence; details `payment_id`, `match_id`, `amount`, `currency`, `via` and, for a restoration,
   `by` = `unconfirmed` or `unmatched`). Like `manual_payment.deleted` they carry the amount, since the record stops
   counting. Never a reason or bank text.

## Consequences

- One bank payment can stand for several manual records (a payment the person recorded in parts), up to 20.
- A record can be deleted while replaced; it then stays out of the evidence even after a restoration (deleted wins). A
  delete bumps the occurrence's version, so it also closes the undo window.
- Concurrency is the match's version and the occurrence's version, both bumped by every change, and the single writer
  connection; concurrent replacements of one match version leave exactly one winner and at most one active replacement.

## Limitations and open points

- Points 4 and 5 are proposed defaults, not operator decisions; the ADR and the code keep them in one place each
  (`Store.decide` for unmatch; `Reverse` does nothing) so they are easy to change.
- A confirmed match whose transaction the provider stops reporting no longer counts (ADR-0035), while its replaced
  records stay replaced: the occurrence shows unpaid with the match flagged `needs_review`. Unmatching restores the
  records. (No longer so: see the amendment, point A.)
- Manual payment records do not store who recorded them (ADR-0031), so any person with `commitments:write` may replace
  any record of the occurrence, as they may delete it.
- The web UI for this action is a separate task (X5b).

## Amendment: replacement follow-ups (task X5c, from the staff review of the X5 change)

Point A is a **proposed default**, not an operator decision; control puts it to the operator. Points B to D are fixes
or documented behaviour.

A. **A replacement is on hold while its match does not count (proposed default).** A confirmed match whose transaction
   the provider no longer reports, or which was superseded, stops counting (ADR-0035) and is flagged `needs_review`.
   Until now the records it replaced stayed replaced, so the occurrence showed unpaid although the person had recorded
   the payment. Now a record counts again while the match replacing it is confirmed and its transaction does not count;
   as soon as the transaction counts again (reported again), the match counts and the record does not. Nothing is
   written when this happens: it is derived when evidence is read (`payments.EvidenceSources`, the same countability
   condition as the match's own evidence), so exactly one of the two counts at every moment and no step can be missed.
   The replacement row stays active, `replaced_by_match_id` stays set and the match history is unchanged; unmatching
   (restore) or reversing (stays replaced) is still the person's decision once they have looked at the match. A
   reversed match keeps its records replaced whatever its transaction does (point 5). The overpayment check, which
   already counts every confirmed match whether its transaction counts or not (ADR-0035, so a reported-again
   transaction cannot double-count), leaves out every record with an active replacement, for the same reason.
   `occurrences.DefaultEvidence` is unchanged: the confirmed or reversed match keeps the occurrence either way.
   Alternative considered: keep the records replaced and only flag the occurrence for review. That leaves the month
   view showing an unpaid (or overdue) commitment the person paid and recorded, because of the bank's reporting.
   Because the hold is derived, the record's version does not change when the bank's reporting changes (point C).
B. **Unmatch restores without an overpayment check (documented).** Restoring can take an occurrence past its amount
   when the person recorded another payment after the replacement (record 13.49 replaced by a 12.99 bank match, then a
   0.50 record, then unmatch: 13.99 paid). These are the person's own records, each counted once; manual payments are
   never checked for overpayment when recorded (ADR-0031); and the excess shows as `overpaid_amount`. Refusing the
   unmatch would keep a match the person says is wrong counting as evidence. Unconfirm cannot overpay: its window
   guarantees the occurrence is unchanged since the confirmation.
C. **A record's version changes with its replacement state.** Replacing and restoring a manual payment increase its
   `version` (and so its ETag) and set `updated_at`, because `replaced_by_match_id` is part of what a read returns;
   a delete based on the version from before is refused with `version_conflict`. The record's own fields still never
   change. The derived hold of point A does not change the version: it follows the bank's data, not a write to the
   record.
D. **No restoration entry for a deleted record.** A record deleted while replaced stays deleted on unconfirm or
   unmatch: its replacement row is still closed (so the match's history lists it under `restored_manual_payment_ids`
   and nothing is left active), but no `manual_payment.restored` audit entry is written and its version is left alone.

Tests: `internal/store/matches/replace_test.go` (`TestReplacementOnHoldWhileTheMatchDoesNotCount`,
`TestUnmatchRestoreCanOverpayVisibly`, `TestReplacementChangesTheRecordsVersion`, `TestRestoreSkipsADeletedRecord`).
