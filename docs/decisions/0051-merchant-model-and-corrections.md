# ADR-0051: Merchant model and corrections

Status: Accepted, first increment (task E1, 2026-11-22). Decision D15 (merchant enrichment) was taken on 2026-11-20: a
local curated mapping plus the person's confirmations, offline; confirmed mappings are reused as suggestions; a remote
adapter exists but is disabled; an identity is shown only for a confirmed or high-confidence association. This increment
builds the model and the person's corrections; the curated mapping, the reuse of confirmations and the disabled remote
adapter follow (task E2). See "What remains".

## Context

A bank transaction's raw description and counterparty are often unreadable ("CARD PURCHASE 4411 EXAMPLE*SHOP"). Ledger
wants to show who was paid, but only when it is reliable, never at the expense of the bank's own record, and never in a
way that touches an amount or a payment status. The lookup source is chosen by D15 (a local curated mapping, offline; a remote adapter present but disabled) and arrives
with task E2.
This task builds the model and the person's corrections so that any source, or none, plugs in afterwards.

## Decision

1. **Three things stay apart.** The bank fact (raw description, counterparty, provider identifiers, amount) is never
   read or written by merchant code: a test snapshots `bank_transactions` around every merchant operation. The
   *enrichment* is what a lookup source said. The *review* is what the person decided. Resolution combines them on read
   (`merchants.Resolve`, a pure function); nothing stores the combined result.
2. **Enrichment record** (`merchant_enrichments`, one row per transaction): status (`identified`, `ambiguous`, `none`,
   `unavailable`), name, logo reference (opaque, nothing fetches it here), source (a lowercase token), confidence as an
   exact integer in permille (0 to 1000, no floats), candidate names for an ambiguous answer, `retrieved_at` and
   `last_failed_at`. CHECK constraints tie the fields to the status (an identified answer has a name and a confidence; an
   ambiguous one has two to five candidates and no name; and so on). `RecordAnswer` is the entry point for the adapter of
   task E2.
3. **Outage never erases knowledge.** An `unavailable` answer (the source was down or refused) only stamps
   `last_failed_at` on an existing row, or creates an explicit `unavailable` row if nothing is known. A later successful
   answer replaces the row and clears the failure.
4. **Review record** (`merchant_reviews`, one row per transaction): `confirmed` (the enrichment's identified name is right;
   the confirmed name is stored), `corrected` (the person's name) or `rejected` (no merchant). It records who, how it came
   (session, token, mcp, system, as match decisions do, D42 3a) and when, and a version counting the decisions. It is
   user data: it refers to the transaction with `ON DELETE RESTRICT`, so a connection purge is blocked by it
   (`ErrPurgeBlocked`), like a payment match. Enrichment is re-fetchable, so the purge deletes it first instead.
5. **Corrections survive re-import and booking.** The bank transaction row is updated in place on re-import (ADR-0013,
   ADR-0020), and both merchant tables are keyed by its identifier and never touched by the import path. Tests re-import
   the same data and book a pending item in place and check the decisions stand. A booking can also arrive under a new
   key: the refresh then marks the pending row `superseded_by` the booked one, and a decision made about the pending row
   must not be lost. When the booked transaction has no decision of its own, resolution inherits the most recent decision
   found along the `superseded_by` chain (bounded depth; a tie goes to the lower transaction ID) and marks it
   `review_inherited`; a decision on the booking replaces it (and clearing that decision makes the inherited one apply
   again). The inherited decision is cleared on the pending transaction only: clearing it on the booking is refused
   (409 `review_inherited`, which names the pending transaction). Nothing is copied or rewritten, so
   nothing can go stale. An inherited confirmation is checked against the booking's own enrichment like any other
   (decision 7). Corrections and rejections carry over unchanged.
6. **What is shown.** `Resolve` returns a name and logo only when the person confirmed or corrected it, or an unreviewed
   identification has confidence at or above the threshold (`reliable`). Everything else is an explicit state:
   `uncertain` (identified below the threshold; not shown), `ambiguous`, `none`, `unavailable`, `not_looked_up`,
   `rejected`. With no name the API says `fallback: true` and the client uses a neutral presentation; the API does not
   put bank text in its place (that is a client decision, and bank text needs `banking:read`). A correction carries no
   logo: the source's logo belongs to the source's own name. A rejection hides even a certain identity.
7. **Stale confirmations.** A confirmation names the enrichment name it confirmed. If the source later reports another
   name (or no identification), the confirmation stops applying and the merchant resolves from the enrichment as if
   unreviewed, with `confirmation_outdated` and `needs_review` set, so a changed answer is never silently trusted
   because of an older yes. Corrections and rejections are the person's own words and always stand.
8. **Never financial.** No merchant table or package refers to amounts, occurrences, matches or payment state; a test
   pins that the packages import none of them. Merchant identity is presentation only.
9. **API** (grants `merchants:read` and `merchants:write`, administrator-only like `banking:read`, because names derive
   from bank text, decision D13): `GET /api/v1/transactions/{id}/merchant`, `PUT .../merchant/review` with
   `{decision, name?}` and `DELETE .../merchant/review` (take the decision back). Decisions are last-write-wins, with no
   If-Match: one person, one decision per transaction, and the audit log keeps the history. Audit actions
   `merchant.confirmed`, `merchant.corrected`, `merchant.rejected`, `merchant.review_cleared` carry the decision they replace (none, inherited, confirmed, corrected or rejected)
   and the channel, never a name (free text).

## What remains (D15 and follow-ups)

- **Reliability threshold.** 800 permille (`merchants.DefaultThreshold`) is a documented named default, not a decision
  of D15. The store takes it as an option (`WithThreshold`); there is no configuration key until one is needed.
- **Reuse of confirmed mappings** (D15: confirmed mappings are reused as suggestions). Not built here: what counts as
  similar is task E2's design. The model does not presuppose it: a reused mapping is written as an enrichment answer
  with its own source (for instance `local_mapping`) and a confidence, so it is shown or held back by the same
  threshold and never outranks a person's decision on that transaction.
- **Local curated mapping, the disabled remote adapter, field allowlist, logo caching and licence, attribution** (task E2;
  the remote adapter is off unless the operator enables it, and what it would send is documented before it is on).
- **UI** (task E3), and a list or batch read for the transaction list; the Go client method, CLI command and MCP tool
  (P-MCP2 for this slice): the routes are usable through the client's raw call until then.
- **Optional concurrency check** (If-Match on the review) if D17 chooses it for user decisions.
