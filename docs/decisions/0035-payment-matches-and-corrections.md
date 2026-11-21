# ADR-0035: Payment matches, manual matching, corrections and reversal

Status: Points 1 to 4, 6 and 7 Accepted as proposed (operator, 2026-11-21, decision D28), and the bank-text rule of
point 9 decided by the operator (D28 5b). The other "proposed" marks (points 5, 9 apart from bank text, 11) stay until
the operator decides them.

## Context

Task X1 links bank transactions (bank facts, ADR-0013, imported by ADR-0020) to commitment occurrences (ADR-0027),
so that a confirmed link becomes payment evidence for the derived payment state of ADR-0031. Operator decisions
(work/decisions.md):

- D8 (a) throughout: an **allocation table** lets one transaction pay several occurrences and an occurrence be paid
  in parts; automatic matching is 1:1 only. **Pending transactions only produce suggestions** and are never
  confirmed. A **reversal reverses the link and reopens the occurrence**, with an audit entry. Transactions the
  provider stops returning are marked "no longer reported" and never deleted.
- D9: rules and the optional model may create suggestions; only the D9 safeguards (or a rule the user marks as
  confirming) confirm automatically, and model output never does. That engine is task X2, not this one.
- D5: exact decimals in their own currency, never combined across currencies without a conversion policy.
- ADR-0031 Consequences ask X1 for the allocation table, its entries in `EvidenceSources` and `DefaultEvidence`, a
  new evidence kind and an occurrence version bump on every link change. ADR-0025 asks for audited corrections.

## Decision

1. **Entity.** `payment_matches` (migration `0017_payment_matches`) holds one row per transaction, occurrence and
   decision: the allocated amount and currency, the `origin` (`auto_rule`, `model_suggestion`, `user`), the
   `state` (`suggested`, `confirmed`, `rejected`, `reversed`), who created it, who made the latest decision and
   when, the latest reason, and a version. `payment_match_events` is its append-only history (action, state and
   amount after the change, actor, reason, time). Matches are user data or machine suggestions, never bank facts:
   the import never writes them, and they refer to `bank_transactions` and `commitment_occurrences` with
   `ON DELETE RESTRICT`. Domain rules are in `internal/domain/matches`, persistence in `internal/store/matches`.
2. **No double assignment, enforced twice.**
   - Database: a partial unique index allows one open (suggested or confirmed) match per transaction and
     occurrence; triggers require the match's currency to equal the transaction's and the occurrence's, an
     outgoing (negative) transaction, a booked, still reported, not superseded transaction for any confirmation,
     only the allowed state changes, no change of what a match links, its currency or origin, no deletion of a
     confirmed or reversed match, and an append-only history.
   - Write transaction: allocation sums are exact `money.Decimal` arithmetic in Go (amounts are never summed in
     SQL, ADR-0005). Confirmed allocations of one transaction never exceed its absolute amount
     (`transaction_over_allocated`), and a confirmation never takes the occurrence's same-currency evidence
     (manual payments included) past its amount (`occurrence_overpaid`) unless the request sets
     `allow_overpayment`, in which case ADR-0031's `overpaid_amount` shows the excess. The single writer
     connection (ADR-0003) serialises these checks, so concurrent writers cannot both pass them.
3. **Evidence.** A confirmed match whose transaction is countable (booked, still reported, not superseded:
   ADR-0020 point 7) is payment evidence of kind `bank_transaction` through `EvidenceSources`; its date is the
   transaction's booking (else value, else transaction) date. Suggested, rejected and reversed matches never are.
   Evidence in another currency cannot arise (point 2), so nothing is converted.
4. **Accepted (operator, 2026-11-21): what keeps an occurrence.** `DefaultEvidence` treats an occurrence with a **confirmed or reversed**
   match as paid for C2: it is kept, and its commitment cannot be deleted (409 `in_use`), even while the
   transaction is not countable. A reversed match is payment history worth keeping. Suggestions and rejections
   do not pin an occurrence: a trigger deletes them (with their history) when C2 regenerates or deletes the
   occurrence, and the audit log keeps the decisions. `TestEvidenceSourcesMatchDefaultEvidence` still checks the
   two lists name the same tables.
5. **Proposed: actions and their states.**
   - *Match* (`POST /payment-matches`): the user's own match, created `confirmed` with origin `user`. The API cannot
     create suggestions; the matching engine (X2, X3) does, through `matches.Store.Create`.
   - *Confirm* a suggestion (any origin; a model suggestion only ever becomes confirmed this way, by a user or a
     token acting for one). *Reject* a suggestion. Both record the decider.
   - *Unmatch* a confirmed match: it was wrong; state `rejected`.
   - *Reverse* a confirmed match: it was right but the payment was reversed (bounced, refunded, returned); state
     `reversed`. The occurrence reopens with exactly the status it would have without the match, because status is
     derived (ADR-0031 point 1).
   - *Correct* the allocated amount of a suggested or confirmed match; a confirmed one is checked again (point 2).
     What a match links, its currency and origin never change: a different target is unmatch plus a new match.
   - `rejected` and `reversed` are final. After either, the same pair can be matched or suggested again as a new
     row, so the history of both decisions stays.
   - Every action takes an optional reason (1 to 500 characters), kept on the match and in its history.
6. **Accepted (operator, 2026-11-21): pending transactions.** A user cannot confirm a pending transaction either: the match is refused
   with 409 `transaction_not_countable` until the transaction is booked (D8 says pending only suggests). The
   database trigger enforces the same.
7. **Accepted (operator, 2026-11-21): transactions that stop counting.** A confirmed match on a transaction the provider no longer
   reports stays confirmed and listed, with `counts_as_evidence` false, `needs_review` true and
   `transaction.no_longer_reported_at` set; the occurrence's derived status no longer counts it, and counts it
   again when the provider reports the transaction again. A suggestion on a pending transaction that B2 marks
   superseded keeps pointing at the pending row and shows `transaction.superseded_by`; it is not moved to the
   booked successor automatically.
8. **Re-import and refresh.** Transaction ids are stable across re-imports (ADR-0020 point 13), so a match, its
   corrections and its decisions survive every refresh: tests re-import through the real engine and the fake
   provider with a rewritten description, a transaction that disappears and returns, and a database reload. A
   connection whose bank facts have matches cannot be purged (`banking.ErrPurgeBlocked`).
9. **API and grants** (bank text: accepted, operator 2026-11-21, D28 5b; the bank-text grant `banking:read` is administrator only, D29). `GET`/`POST /payment-matches` (filters `transaction_id`, `occurrence_id`, `state`,
   `origin`), `GET /payment-matches/{id}`, and `POST /payment-matches/{id}/confirm|reject|unmatch|correct|reverse`.
   Each action needs the match's version in `If-Match` or the body's `version` (ADR-0011/0015); a stale one is 409
   `version_conflict` with the current match. Every change also increases the occurrence's version (ADR-0031
   point 8). Like manual payments (ADR-0031 point 9), matches use `commitments:read` and `commitments:write`.
   **Bank text needs `banking:read`.** The new grant `banking:read` (resource `banking`, read only, administrative)
   covers the bank's own text. Without it the match responses carry `transaction.description` and
   `transaction.counterparty` as `null` (redacted, not omitted) and `bank_text_redacted: true`; every other field
   stays visible under `commitments:read`. Built-in roles: `administrator` holds it (all grants); `read-only` does not,
   because the grant is administrative and counterparty names are sensitive (D13; operator decision D29 makes `banking:read` administrator-only); a token needs it explicitly and is
   still limited by its owner's role. Create and action responses and 409 version-conflict bodies are redacted the
   same way. Each response now also carries `occurrence_version`.
   The response keeps four things apart: `transaction` (the bank fact, `source: "bank"`), `origin` (who proposed
   it), `state` with `decided_by`/`decided_at` (the decision) and `counts_as_evidence` (whether it pays now),
   plus `history`. New error codes: `transaction_not_countable`, `transaction_not_debit`,
   `transaction_over_allocated`, `occurrence_overpaid`, `duplicate_match`, `invalid_match_state` (all 409).
   The Go client gains the matching methods.
10. **Audit.** Six actions, registered in `audit.Definitions`, target the match: `payment_match.suggested`,
    `.confirmed`, `.rejected`, `.unmatched`, `.corrected`, `.reversed`, with `occurrence_id`, `transaction_id`,
    `origin`, `from` (the state before, `none` for a new match) and `currency`. Amounts, reasons and bank text
    stay out of the audit log (ADR-0025); they are in the match and its history.
11. **Proposed: the X2 seam.** X2 calls `matches.Store.Create` with origin `auto_rule` or `model_suggestion` and
    state `suggested`, or `auto_rule` and `confirmed` once its D9 safeguards pass; the store refuses a model
    confirmation, a pending or uncountable confirmation, a duplicate pair, an over-allocation and (without
    `AllowOverpayment`) an overpayment whatever the caller decided. Its audit actor is `audit.System`. A rejected
    match is the record X2 should consult so it does not suggest the same pair again.
    The store also guards by actor, not only by origin: an actor that is neither a user nor a token (system,
    anonymous) is held to the D8 automatic rules whatever origin it claims. Creating or confirming is 1:1 only,
    the whole transaction and its only open match, with the occurrence's amount (Create applies AutoTolerance); it cannot claim origin `user`
    (`ErrActorOrigin`), cannot correct an allocation (`ErrActorCorrect`) and can never pass `AllowOverpayment`
    on create or confirm (`ErrActorOverpayment`), because deciding a payment really happened twice is a user's
    judgement and duplicate payments are an unresolved decision. Decision D33: automation can only reject its
    own suggestions; it never unmatches or reverses (`ErrActorUndo`), its own confirmations included. It cannot
    reject a suggestion with a person in its history: any event (suggestion, confirmation, correction) from a user
    or token. A provider reversal signal must become a visible warning for the
    user, never an automatic reverse; no such signal exists yet, so surfacing it is X2's job.

## Why

- One row per (transaction, occurrence) with its own amount is the D8 (a) shape ADR-0031 already reads as
  evidence; split and partial payments need no other structure.
- Constraints in the database catch any writer that skips the store; the exact-decimal checks that SQL cannot do
  run in the same write transaction, which the single writer makes race-free.
- Never deleting decisions, and keeping rejected and reversed rows, makes every correction traceable and lets X2
  respect earlier rejections.
- Keeping a confirmed match visible but not counting while its transaction is unreported follows ADR-0020's
  "countable" rule without losing the user's decision.

Rejected: a 1:1 link (D8 b, no partial payments); deleting a match to unmatch it (loses the decision); counting
uncountable transactions as evidence; converting an allocation into the occurrence's currency (no policy, D5);
moving suggestions to a superseding transaction automatically (a guess the user should see first).

## Consequences

- **For the operator (proposed, not decided):** confirmed and reversed matches keep their occurrence, while
  suggestions and rejections are dropped with it (point 4); the action set, final states and what *correct* may
  change (only the amount) (point 5); users cannot confirm pending transactions (point 6); unreported
  transactions keep their match but stop counting, and superseded pending rows are not followed automatically
  (point 7); the commitments grants and the API being user-only for creation (point 9); only outgoing
  transactions can pay; reasons optional on every action.
- **Open:** a bank reversal arrives as a separate booked transaction; linking it to the match it reverses is not
  modelled (reverse records only the user's decision and reason). Duplicate payments (the same bill paid twice
  from two transactions) are refused by the overpayment check unless allowed, and are not otherwise detected.
  C3's manual payment recording still does not check overpayment against confirmed matches. Retention follows D8
  (kept indefinitely); the explicit purge refuses facts with matches.
- **Not built here:** the matching engine and its review queue (X2), the optional TypeSafe matcher (X3), the UI
  and MCP tools (X4), and CLI commands for matches (only the Go client methods exist).
- **Migration number:** `0017_payment_matches` with fixture `0016_device_cookies.sql`. It was written as 0015 and renumbered (0016, then 0017) when
  tasks A6 (OIDC, `0015_oidc`) and A2c (device cookies, `0016_device_cookies`) merged first.

## Amendment: the X2 seam in use (task X2, ADR-0037)

The matching engine of ADR-0037 uses point 11 as written: it writes as `audit.System` with origin `auto_rule`, and
`Store.ConfirmAuto` confirms the engine's own suggestions with the commitment's `match_tolerance`. One guard is added:
automation (a system or anonymous actor) cannot confirm a `model_suggestion` (`matches.ErrAutoModel`), as point 5 and
decision D9 require. A rejected match is what the engine consults: a pair a person rejected, unmatched or reversed is never
suggested again.
