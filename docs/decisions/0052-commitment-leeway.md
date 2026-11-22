# ADR-0052: Commitment leeway and priority

Status: Accepted (task DF2, 2026-11-22). D14 (a) for leeway and (a) for the priority scale is the operator's decision:
leeway is **user-entered only**, the priority scale has **three levels** (essential, important, flexible). The points under
"Proposed defaults" are this ADR's own choices within that; they are not operator decisions and are listed again under
"Open questions".

## Context

A person often does not know how much slack a creditor allows after a due date, and sometimes does know ("5 days, if I
tell them", "none"). Ledger must keep what they said, say so when they said they do not know, and never turn a missing
answer into a number. Overdue stays counted from the original due date (D7, ADR-0031): a grace only annotates it.

## Decision

1. **Priority** is the three-level scale already on a commitment (ADR-0024, `priority`: essential, important, flexible,
   or none). DF2 changes nothing there; it is recorded here as the confirmed D14 scale.
2. **Leeway is a member of the commitment definition** (migration 0027, `leeway_*` columns; API member `leeway`),
   added the way the match tolerance was (ADR-0039), so it has the commitment's version, merge-patch, If-Match and audit
   (`commitment.updated` names `leeway` as a changed field) for free. Changing it never regenerates occurrences.
3. **Four distinct states.** `leeway: null` means the user recorded nothing (all existing commitments). Kind
   `unknown` is the user's explicit statement that they do not know; `none` that the creditor allows no grace;
   `period` is `days` (1 to 365) after each occurrence's due date; `deadline` is a fixed `until` date that covers the
   occurrences due on or before it. `unknown` and `none` carry no days or date; zero days is refused (that is `none`).
   The schema enforces the pairing with CHECKs and triggers, so no write path can store "unknown as 0".
4. **Free text, all optional, never summed:** `conditions` (what the grace depends on), `consequences` (known fees or other
   consequences, as words) and `source` (where the user learned it). A numeric fee is still recorded as an explicit
   deferral plan adjustment (ADR-0050, D14); leeway does not carry money.
5. **User-entered only.** Nothing writes leeway from payment history, bank data, merchant data or model output; there is no
   default length and no suggestion path (D14 option b was not chosen). The MCP tool says so to the model: use `unknown`
   when the person says they do not know, omit it when they said nothing.
6. **Effect on state.** `payments.GraceFunc` now returns a `payments.Grace` (was a date and a flag): whether leeway was
   recorded, its kind and the grace date if it is a period or a covering deadline. For an overdue occurrence the payment state
   gains `leeway` (`not_recorded`, `unknown`, `none`, `period`, `deadline`; null when not overdue) beside the existing
   `grace_until` and `within_grace`, which are set only for a period or deadline. Status, outstanding and overdue are
   unchanged. `cmd/ledger` wires the store-backed hook (`store/commitments.Store.Grace`).
7. **Surfaces.** Create, get, list and patch on `/api/v1/commitments` (OpenAPI schema `Leeway`), the Go client
   (`Commitment.Leeway`, `NewCommitment.Leeway`), CLI flags (`--leeway KIND`, `--leeway-days`, `--leeway-until`,
   `--leeway-conditions`, `--leeway-consequences`, `--leeway-source`, `--clear-leeway`) and the MCP `create_commitment`
   tool. The month board says plainly that an overdue occurrence's grace is unknown or none when the user said so.

## Proposed defaults (not operator decisions)

- Leeway belongs to the commitment, not to one occurrence. A one-off arrangement for a single occurrence is a
  `creditor_agreed` or `documented_grace` deferral plan (ADR-0050), which already exists.
- A `deadline` does not cover occurrences due after it (they get no grace date).
- The grace lookup reads the commitment per occurrence. When the commitment cannot be read it reports nothing recorded: that
  can only drop the annotation, never hide the obligation or invent a date.
- Limits: days 1 to 365; conditions and consequences 500 characters; source 200.
- In a merge patch the `leeway` object merges member by member, so a client switching kind sends the other member as null
  (the CLI does this); `leeway: null` clears it.

## Consequences

- The overdue/outstanding totals, the outlook and automatic matching do not read leeway.
- The UI has no commitment editor yet (task C4 area): leeway is entered through the API, CLI and MCP; the month board
  shows what was recorded. The DF3 planning UI can use the `leeway` states as they are.
- The Terraform provider (T4) will need a `leeway` block on the commitment resource; `null` and explicit `unknown` must stay
  different there too.

## Open questions

Whether leeway should also be settable per occurrence; whether a missing grace on an overdue occurrence should be nudged
in the UI ("add what you know"); whether `consequences` should later gain a structured fee.
