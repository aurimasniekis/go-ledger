# ADR-0043: Matching and correction UI, and the MCP match tools

Status: Proposed (task X4). The points marked "proposed for the operator" are not decided.

## Context

Task X4 gives the user a screen to review suggested payment matches, confirm or reject them and correct what is
confirmed, and gives MCP clients the same operations. It builds on the matches API (ADR-0035), the engine (ADR-0039),
the app shell and `useOptimisticSave` (ADR-0037) and the MCP core (ADR-0038). Decisions in force: D9 and D33 (a score is
not proof, and automation never undoes a person's decision), D36/D37 (an undo notice stays until dismissed), D39 and D41
(tolerance). The derivation of paid status is not touched here.

## Decision

1. **Screen.** `/matches` ("Payment matches", in the primary navigation) lists matches by state: "Needs review"
   (`suggested`) and "Confirmed". Each card keeps apart the bank fact (date, amount, booked or pending, bank text when the
   caller may see it), the origin ("suggested by a matching rule or model" or "matched by you") and the decision. A page
   note and every rule suggestion say a similarity score is a hint, never proof. A page link leads to the monthly view,
   where a payment is recorded manually (task C3); the manual-payment form is not duplicated here.
2. **Actions.** Suggested: confirm, reject, correct amount. Confirmed: correct amount, unmatch (it was wrong), reverse (the
   payment bounced). Rejected and reversed are final and have no actions. Confirm is one step; the others open an inline
   form with an optional reason. A pending transaction cannot be confirmed: the button is disabled and says why. The
   server's guards decide everything else.
3. **No optimistic display of payment changes.** The actions pass no `apply` to `useOptimisticSave`, so the cache and the
   card show the match as it was until the server answers. While the request is out the card says "Not saved yet" and
   that the match is still what it was. This is a deliberate reading of "optimistic updates with visible pending state":
   the update is the visible pending state, never a change presented as saved (the plan's SE/UX tradeoff).
4. **Failure and conflict.** A failed save says it was not saved and that the match is unchanged, and keeps what was typed;
   "Try again" resends. A `version_conflict` (409) loads the latest version (the hook refetches), says the change was not
   sent as it was, and offers "Save again on the latest version" only while the latest state still allows that action;
   otherwise it says nothing more can be done. Nothing is resent without the person's choice. A card with a failed save
   stays on the page even when the refetch no longer lists the match (for example it was rejected elsewhere), marked "No
   longer in this list", until dismissed. An `occurrence_overpaid` refusal offers an explicit "Allow overpayment and try
   again"; overpayment is never sent by default.
5. **Undo.** After a save a notice names the change and stays until dismissed (D37). A confirmation is undone by
   unmatching, a correction by correcting back; both are saves of their own, with their own pending and failed states.
   Rejection, unmatch and reversal are final in the API, so they offer no undo, and the notice says so.
6. **Keyboard and focus.** Everything is a native button, input or form. Opening a form focuses its first field; cancelling
   returns focus to the button that opened it; after a save, focus moves to the notice. Status is announced politely and
   failures assertively through the shared announcer.
7. **MCP tools.** Added to the registry: `list_payment_matches`, `get_payment_match` (read); `create_payment_match`,
   `confirm_payment_match`, `correct_payment_match`, `run_payment_matching`, `record_manual_payment` (write);
   `reject_payment_match`, `unmatch_payment_match`, `reverse_payment_match`, `delete_manual_payment` (destructive: final,
   or removing a record). Each maps to one API route, so grants, versions, validation and audit are the route's, and the
   grant-parity test covers them. `create_commitment` gains `match_tolerance`. Descriptions tell a client that a score is a
   hint, that decisions need the person's instruction, and that a stale version must be re-read.
8. **Too-large answers (P-MCP1 follow-up).** `ledgerclient.Client.Raw` no longer fails when an answer exceeds its size cap:
   it returns the real status and Location with `Truncated` set, as the in-process caller already did. A stdio write
   whose answer is large is therefore reported as done, naming the resource, not as "unavailable", so a model does not
   repeat a create. The create tools say so in their descriptions.
9. **`requested` (P-MCP1 follow-up).** The docs now say that `requested` means the client asked for the tool, not that it
   succeeded, and point to the operation's own audit entries.

## Why

- Showing a confirmation as saved before the server commits it would present an uncommitted payment change as fact; a
  visible pending state costs one round trip of waiting and cannot mislead.
- Resending after a conflict only on request keeps a decision made on stale data from being applied silently; checking
  that the latest state still allows the action avoids a pointless second refusal.
- Reusing the routes for MCP keeps one set of rules; the tool kinds follow what cannot be undone.
- Rejected: optimistic removal of a card from the queue (loses the place to show a failure); an undo for reject, unmatch
  and reverse by creating a new match (a different origin and a different decision, not an undo); a bulk "confirm all".

## Consequences

- **Proposed for the operator:** the two-view screen (suggested, confirmed) and no list of rejected or reversed matches in
  the UI; that confirm is one step without a confirmation dialog while unmatch, reverse and reject ask for an optional
  reason in a form; marking reject, unmatch and reverse destructive for MCP.
- **Open:**
  - Each card reads its occurrence and commitment name separately (two small reads per card): fine for one person's
    review queue, a join in the matches response would remove them.
  - No bulk actions, no filtering by account or date, and no tool or screen for status overrides ("mark paid without a
    payment"); only the manual payment record exists in MCP.
  - The tolerance-paid display and the not-recorded status in the monthly view belong to the derivation task, not this one.
  - The UI cannot show a provider reversal warning (no such signal exists yet, ADR-0035 Open).
- **Checks that keep this true:** `web/src/features/matches/matches-page.test.tsx` (pending is not saved, failed save,
  409 reconciliation, overpayment, keyboard-only correction, focus, redaction, pending transactions);
  `cmd/ledger/mcp_matches_test.go` (the journey over MCP, annotations, redaction, audit); the grant-parity test; the
  `Raw` and Remote truncation tests; `TestDocsListEveryTool`.
