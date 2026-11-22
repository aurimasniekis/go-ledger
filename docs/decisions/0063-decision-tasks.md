# ADR-0063: Decision tasks: categorisation, label suggestions and alerts

Status: Accepted (task L6, 2026-11-22). Decisions D12 (category, label and bucket semantics) and D13 (LLM policy) were
taken on 2026-11-20. This increment is the domain, store and audit; HTTP routes, the review UI and the settings screens
are task L7. The decision layer is [ADR-0060](0060-decision-provider-layer.md), the organisation model
[ADR-0053](0053-organisation-model.md).

## Context

ADR-0060 stores a validated model answer as a pending suggestion and leaves applying it to this task. D13 says: review by
default, opt-in auto-apply for categorisation only and always reversible, output kept as a suggestion with its origin,
alerts in-app only unless a channel is selected (consent expiring, commitment amount changed, unmatched overdue). The
acceptance criteria are that suggestions are correctable and that auto-apply, where allowed, is reversible and audited.

## Decision

1. **Package `internal/domain/decisiontasks`** runs the tasks between the decision service and the organisation store.
   `SuggestCategory` and `SuggestLabels` ask for one object; `Accept`, `Correct`, `Reject` and `Revert` handle what
   becomes of the answer. Facts have no field for an IBAN, account number, counterparty name or balance, so they cannot be
   passed; the decision layer still applies the provider allowlist. The merchant name is not sent when the merchant is
   classified as a transfer (ADR-0060 review note N4).
2. **Review is the default.** A valid answer is a `pending` suggestion and assigns nothing. The only automatic
   application is a categorisation whose task has `auto_apply` on: it is written through the organisation store's
   `ApplyAutomatic` as a **model-origin** assignment with source `provider:model`, so a rule or a person always outranks
   it. If the store keeps the proposal (a person or rule decided meanwhile, the category was archived) nothing is written
   and the suggestion stays pending for review. Label suggestions are never auto-applied (configuration refuses it).
3. **Nothing is asked needlessly.** Categorisation is skipped, with no request counted, when a person or a rule has
   categorised the object (including a person's explicit "no category"), when there is no active category, when there
   are more candidates than the task offers, or when a person already turned a suggestion down for that task and object
   (rejected, corrected or reverted). Labels the object has, or the person removed, are not offered. A model answer of
   "none" is recorded and leaves the pending queue as `superseded` (audited as dismissed).
4. **Accept** writes the suggestion as the person's own (**user-origin**) assignment through `SetCategory`/`AddLabel`
   (the person's word, which no automatic origin replaces) and marks it accepted; the suggestion row keeps the provider,
   model and response id that said it, so provenance stays. **Correct** writes the person's own answer instead (a
   different category, "none", other labels) and marks the suggestion rejected. **Reject** assigns nothing. A state moves
   once (`ErrNotPending`). The organisation store re-checks the object and the definitions at that moment, so a vanished
   object or an archived definition refuses the accept and the suggestion stays pending (ADR-0060 note N3). The
   suggestion does not record which version of the object it answered; the person sees it next to the object.
5. **Revert** takes back an auto-applied category: it is allowed only while the object's category is still exactly the
   model-origin one that suggestion wrote (same category, origin and source). It removes that assignment (the person's
   reset, audited by the organisation store) and records the reversal in `decision_suggestion_reversals` (migration
   0032), because a suggestion's state moves once and its history, what the model said and what the person did, must both
   stay. A later person or rule decision is never undone by a revert (`ErrNotReversible`). A revert also stops the model
   from being asked about that object again.
6. **Audit.** `decision_suggestion.accepted|rejected|corrected|auto_applied|reverted|dismissed` (task, provider, via; never
   the model, the answer or object text), next to the organisation store's own `assignment_changed` and
   `automatic_applied` events. `ApplyAutomatic` then `Decide` are two transactions (two stores): a failure between them
   leaves the assignment written and the suggestion pending, and the audit log of the assignment stands.
7. **Alerts** (`internal/domain/alerts`, store `internal/store/alerts`, migration 0032): three kinds from D13. Conditions
   are built by Ledger code (`ConsentExpiring`, `AmountChanged`, `UnmatchedOverdue`) with exact decimal facts and their
   currencies (a currency change is shown as itself, never converted) and a deterministic message. Raising is idempotent
   by a key that includes what makes the situation new (expiry date, new amount, due date); an alert is only ever closed
   (dismissed by a person, resolved when the condition stops holding), never edited or deleted, and audited.
8. **Alerts are in-app only.** A `Channel` (name and notify function) receives a newly raised alert only when the operator
   selected it; without one nothing leaves the process, and a channel failure never loses the alert.
9. **Model wording is optional and labelled.** With the `alerts` task enabled (a `generate` task, with consent) a model may
   add up to 280 characters of wording. It is sent only Ledger's own alert message (no bank text), is stored apart from the
   message with its provider and model, never replaces the message and is never needed to raise an alert. The suggestion
   row it came from is marked `superseded` since the text is attached to the alert (review-by-default is satisfied
   because the text is informational and the deterministic message is always shown).

## Limitations and what is not built (kept visible)

- **No HTTP routes, CLI commands or UI** for suggestions or alerts, and so no OpenAPI change: they belong to L7 (review UI
  and settings) and the MCP and Terraform tasks. Grants for them are decided there.
- **No runner calls these services yet.** Nothing in `serve` triggers `SuggestCategory`/`SuggestLabels` for new
  transactions, nothing reads transaction facts and the merchant's transfer classification for them (the caller builds
  `Facts`), and no detector scans bank connections, commitments or occurrences to feed `alerts.Service.Raise`. Wiring
  these to a trigger (after import) and to the real data is a follow-up that needs L7's settings and the decision adapters
  (L2 to L5, none exist in this build, so an enabled decision task still stops startup as ADR-0060 says).
- **No configuration key selects an alert channel.** The channel list is a constructor argument; the config key and the
  Pulse event for alerts are not defined.
- Retention of suggestions and alerts is not decided (ADR-0060 lists it as open).
- Migration number 0032 may need renumbering if another branch merges a 0032 first.

## Why

- Accept as user origin keeps the rule "a person's word outranks everything" simple; the suggestion row, not the
  assignment, is where the model's provenance lives.
- A separate reversal record, instead of a new suggestion state, avoids rebuilding a table with CHECK constraints and keeps
  the suggestion's own history intact.
- Skipping objects a person already decided saves the monthly request cap and never lets a model second-guess a person.
