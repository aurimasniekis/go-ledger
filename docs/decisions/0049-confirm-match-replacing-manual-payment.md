# ADR-0049: Confirm a bank match as replacing a manual payment

Status: Accepted (task X5, 2026-11-21). D45 is the operator's decision. What happens to replaced records on unmatch and
reversal (points 4 and 5) is not covered by D45: those two are this ADR's proposed defaults, which control accepted for
now and has put to the operator; a different operator choice would amend them.

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
  records.
- Manual payment records do not store who recorded them (ADR-0031), so any person with `commitments:write` may replace
  any record of the occurrence, as they may delete it.
- The web UI for this action is a separate task (X5b).
