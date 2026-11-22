# ADR-0039: Automatic payment matching engine

Status: Proposed (2026-11-21). The safeguards of point 4 follow operator decisions D9, D8 and D5 and are not open. The
candidate window, amount band, hint score, drop rule, run triggers and the guard of point 9 are implementation choices no
operator decision covers; they are listed for the operator under Consequences.

> **Review amendment (PR #74).** The store reads the tolerance itself: `Create` (for an automation actor or origin
> `auto_rule`) and `ConfirmAuto` read `match_tolerance` from the commitment, through the occurrence, inside the write
> transaction, so a stale or widened value in a plan cannot loosen the guard. The caller no longer passes one. The store
> also refuses an automatic confirmation dated more than 3 days from the due date (`ErrAutoDate`). A refused
> confirmation's reason carries a stable code (`store_refused: overpaid`), never the store's error text.

> **Amendment (X6, operator decision D-DF item 7, 2026-11-22): the planned-date window.** For an occurrence with an
> **active** deferral plan (ADR-0050), the date safeguard of point 4 also passes when the booked transaction is dated
> within **3 days of the plan's planned date**, in addition to the 3 days around the due date. The candidate window of
> point 3 widens the same way (7 days around the planned date as well), so such a pair is a candidate and ambiguity is
> counted over it. Every other D9 safeguard is unchanged: same currency, exact amount or the commitment's tolerance
> (short only), booked, exactly one candidate transaction and one candidate occurrence, nothing already assigned.
> An **undone** plan no longer widens either window, and a plan revision moves the window with its planned date.
> The store reads the active plan's planned date inside the write transaction, like the tolerance, so
> `matches.CheckAutoPlanned` holds whatever the plan decided. Its refusal is `ErrAutoDate`, whose message names the
> due-date window only in general terms and does not give the distances. The engine's reason text is where the distances
> appear: a confirmation made through the planned window says "N days from the planned date of its deferral plan", and a
> failed date check reads "date_outside_window (N days from the due date, M from the planned date)". Two transactions, one near the due date and one near the planned date, make the
> occurrence ambiguous and are only suggested.

## Context

Task X2 needs an automatic matching engine on top of payment matches (ADR-0035, task X1). The engine pairs outgoing bank
transactions (bank facts, ADR-0013, imported by ADR-0020) with open commitment occurrences (ADR-0027). It confirms a
pair only when every safeguard passes and otherwise leaves a suggestion for a person to review. Operator decisions
(work/decisions.md):

- **D9 (a) plus an optional per-commitment tolerance.** Auto-confirm only when all of these hold: same currency, exact
  amount (or within the user's tolerance), booked, dated within ±3 days of the due date, exactly one candidate occurrence
  and one candidate transaction, and not already assigned. expr-lang rules may suggest (task X2b, not built here). Model
  and TypeSafe output only ever suggest (task X3).
- **D8 (a).** Pending transactions only ever produce suggestions. Automatic matching is 1:1 only.
- **D5 (a).** Amounts are exact decimals in their own currency and are never converted for matching.
- **D28** (ADR-0035 points 1 to 4, 6 and 7). **D33:** automation never rejects, unmatches or reverses a match a person
  confirmed or corrected. It may only drop its own suggestions, and it never passes `AllowOverpayment`.
- ADR-0035 point 11 is the seam: `matches.Store.Create` with origin `auto_rule`, with the store's actor guards.

The engine must be deterministic and offline, and its tests must not depend on live bank or model APIs.

## Decision

1. **Layout.** The pure planner and the runner live in `internal/domain/automatch`. `Plan(Snapshot) []Action` is a pure
   function, and `Engine.Run` applies its actions through a `Store` port. `internal/store/matches` implements the port:
   `Snapshot` and `ConfirmAuto` sit next to the existing `Create` and `Reject`. Every write is made as
   `audit.System` and recorded as `payment_match.*` by the store, so the ADR-0035 actor guards apply whatever the plan
   says.
2. **Snapshot.** The engine first materialises occurrences through the month of today + 7 days (reporting zone), so a
   payment made before next month's due date finds its occurrence. It then reads:
   - the transactions it may match: outgoing (negative), booked or pending, still reported, not superseded;
   - the open occurrences: not cancelled, of a commitment that is not deleted, with no payment evidence (no manual
     payment, no countable confirmed match) and no confirmed match. A partly paid occurrence is left to a person,
     because auto-matching is 1:1 only (D8);
   - every match, with whether a person (user or token) ever created, decided or corrected it.
3. **Candidates.** A pair (transaction, open occurrence) is a candidate when all of these hold:
   - the transaction is not already assigned (it has no confirmed match);
   - no person closed the pair (point 6);
   - the currency is the same;
   - the transaction is dated (booking date, else value date, else transaction date) within **7 days** of the due date;
   - its magnitude differs from the occurrence's amount by at most **10%**, or by at most the commitment's tolerance when
     that is larger.

   The description is **only a hint**. `Score` is the share (0 to 100) of the commitment name's words of three or more
   letters that appear in the transaction's description or counterparty. It is shown in the reason. It never makes a
   candidate, never breaks a tie and never confirms anything.
4. **Safeguards (D9).** A candidate pair is confirmed automatically only when **all** of these pass:
   - `amount`: the transaction's whole magnitude equals the occurrence's amount, or falls short of it by no more than
     the commitment's `match_tolerance`. A payment **above** the amount always fails (`amount_above_expected`), because
     confirming it would overpay and only a person may allow that (D33, ADR-0035 point 2);
   - `booked` (pending fails, D8);
   - dated within **3 days** of the due date;
   - the occurrence has exactly one candidate transaction, and the transaction exactly one candidate occurrence;
   - the transaction has no other open match left after this run's drops (`transaction_has_open_match`).

   Anything else becomes a **suggestion** (state `suggested`, origin `auto_rule`). Its reason lists the failed
   safeguards as stable codes with their figures, plus the hint score, for example: `automatic match not confirmed:
   pending (only a booked transaction can be confirmed); several_transactions (2 candidate transactions for this
   occurrence); description similarity 100/100 (a hint only)`. Reasons never contain bank text (the description and
   counterparty need `banking:read`, D28 5b). The review queue is `GET /payment-matches?state=suggested`: ambiguity is
   visible there and is never confirmed automatically.
5. **The engine's own suggestions.** When an `auto_rule` suggestion the system created, and no person touched, now
   passes every safeguard (a pending transaction booked in place), the engine confirms it through `ConfirmAuto` with the
   commitment's tolerance. When it is no longer a candidate (its transaction was assigned elsewhere, superseded or is no
   longer reported, or its occurrence was paid, cancelled or changed), the engine **drops** it: a system rejection with
   a reason (D33 allows this, and nothing more). A suggestion a person touched, a model suggestion and any confirmation are
   never acted on.
6. **No loops.** A pair a person rejected or unmatched (state `rejected` with a person in its history), or that was
   reversed, is never a candidate again. A rejection keeps its pair closed even after the transaction books.
   A pair the engine dropped itself may be suggested again if it becomes a candidate again. The drop rule is the
   negation of the candidate rule over the same snapshot, so one run never drops and re-creates the same pair, and
   unchanged data gives no actions.
7. **Determinism and idempotency.** The snapshot is sorted. Actions come out in a fixed order: drops by match ID, then
   confirmations, then suggestions, each by due date, occurrence ID and transaction ID. A second run over unchanged data
   writes nothing, and the tests check this, including after a re-import. One run happens at a time per engine (a
   mutex), and the single writer connection serialises everything else (ADR-0003).
8. **When the store refuses.** The store re-checks every action (ADR-0035 point 11). The plan cannot override that:
   - a refused automatic confirmation becomes a suggestion whose reason names the refusal;
   - a refused drop or confirmation of a suggestion (a version conflict, a person acted meanwhile) is skipped and counted,
     and the next run decides again;
   - a database error ends the run with what was done so far.
9. **A store guard added here.** Automation can no longer confirm a `model_suggestion` (`matches.ErrAutoModel`). This
   applies on `Confirm` with a system or anonymous actor and on `ConfirmAuto`. D9 says model output only ever suggests,
   and ADR-0035 point 5 says only a user (or a token acting for one) confirms it. The X1 tests that confirmed a model
   suggestion as the system now use `auto_rule`, and a new case expects the refusal.
10. **Triggers.**
    - `bankimport.Options.AfterRefresh` runs the engine after every refresh whose outcome is `succeeded` or `partial`,
      whatever started it (the scheduler or a user), once the connection's lock is released. `ledger serve` wires it in.
      A failed run is logged and retried after the next refresh.
    - `POST /api/v1/payment-matching/runs` (`commitments:write`, like the matches it writes) runs it on request and
      returns the counts (`candidates`, `confirmed`, `suggested`, `dropped`, `skipped`).
    - The management CLI's `ledger matching run` calls that endpoint through the Go client
      (`Client.RunPaymentMatching`).
    - A requested run is audited as `payment_matching.run` for the caller, with the counts. The matches themselves are
      audited one by one with the system as actor.
11. **Per-commitment tolerance.** Commitments gain the optional `match_tolerance` (`Fields.MatchTolerance`): zero or
    more, in the commitment's currency, `null` for none. Migration `0020_commitment_match_tolerance` adds
    `match_tolerance_amount` and `match_tolerance_currency`, with a CHECK for a non-negative exact decimal and triggers
    that keep the pair together and in the commitment's currency. It comes with fixture
    `0019_manual_payment_soft_delete`. The tolerance is in the commitment API (create, read, merge patch, OpenAPI), the
    Go client types, `ledger commitments create|update --match-tolerance` (`--clear-match-tolerance`) and
    `commitment.updated` (field name only).

## Why

- A pure planner over a snapshot makes the safeguards testable in isolation: every acceptance case is a table entry,
  and determinism is checked by shuffling the input. The store keeps the final say, so a planner bug cannot confirm
  what the X1 guards refuse.
- Counting ambiguity over the wider candidate set (7 days, 10%), not only over pairs that would pass, is the
  conservative reading of D9's "exactly one candidate". A near-amount or pending look-alike blocks an automatic
  confirmation and is shown beside it for review.
- Refusing payments above the expected amount, even within a tolerance, follows from D33 and ADR-0035 point 2:
  automation never allows an overpayment.
- Keeping person-closed pairs closed, and letting only the engine's own drops reopen, follows D33 and stops a rejection
  from being re-suggested in a loop. It still lets the engine recover from its own clean-ups.
- Rejected:
  - confirming on description similarity, or using it to break ties (D9: a score is not proof);
  - converting currencies to find candidates (D5 has no matching policy);
  - matching partly paid occurrences or splitting transactions automatically (D8: 1:1 only);
  - moving a pending suggestion onto its booked successor in place (ADR-0035 point 7: the engine drops its own and
    proposes the successor instead, visibly).

## Consequences

- **For the operator (proposed, not decided):**
  - the 7-day candidate window and the 10% amount band;
  - the hint score's definition;
  - that ambiguity counts every candidate, not only those that would pass;
  - that payments above the amount never auto-confirm, even within a tolerance;
  - that the engine drops its own stale suggestions, and that a pair it dropped may come back;
  - running after `partial` refreshes too;
  - the `commitments:write` grant for a requested run;
  - the `ErrAutoModel` guard (point 9).
- **Open:**
  - expr-lang rules (X2b) and the TypeSafe matcher (X3) plug in as further suggestion sources. They must use origin
    `auto_rule`/`model_suggestion` through the same store.
  - The review queue's UI and MCP tools are X4.
  - Duplicate payments (one bill paid twice) are not detected beyond the ambiguity and overpayment checks.
  - A bank reversal arriving as a separate transaction is not linked. D33 says a provider reversal signal becomes a
    warning for the user, and no such signal exists in the bank-fact model yet, so nothing is surfaced.
  - A suggestion's reason is fixed when it is created. If the data changes and it stays a candidate, its reason is not
    refreshed.
  - The engine reads every eligible transaction and open occurrence on each run, which is fine for one person's data. A
    larger history would need a date bound.
- **Checks that keep this true:**
  - `internal/domain/automatch` tests: every safeguard, ambiguity on both sides, duplicates, pending, score never
    confirms, closed pairs, D33, determinism;
  - `internal/store/matches/automatch_test.go`: imported fake data, the import hook, idempotency after a re-import, a
    rejection never re-suggested, drops, tolerance, the model guard;
  - `cmd/ledger/matching_api_test.go`: API, audit, tolerance through the API;
  - the permission matrix, the OpenAPI contract and client tests, and the float ban (`automatch` is in
    `moneyPackages`).
