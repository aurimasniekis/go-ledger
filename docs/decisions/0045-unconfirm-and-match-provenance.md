# ADR-0045: Unconfirm a payment match, and the provenance of match events

Status: Accepted (task X4c, 2026-11-21). Decisions D33, D37, D41 3a and D42 1a/3a are the operator's; the window rules
in point 2 are this ADR's reading of them, accepted as written with no time limit by the operator's decision D44.

## Context

D42 1a asks for a real undo of a confirmation: confirmed back to suggested, only by the person who confirmed it, only
while the Undo notice shows. The notice is dismiss-only (D37): it stays until the person dismisses it or their next
payment change replaces it, and the server never learns about a dismissal. D42 3a says API-token and MCP actions count as
the person (as D33 already treats them) and that every match event must record whether it came through MCP or a token.
ADR-0043 point 5 undid a confirmation by unmatching, which is final; ADR-0035 has no transition back to suggested. D41
3a snapshots the tolerance on the match at confirmation (migration 0021).

## Decision

1. **The transition.** `POST /api/v1/payment-matches/{id}/unconfirm` (grant `commitments:write`, If-Match or body
   `version`, optional `reason`) moves a confirmed match back to `suggested` and returns the match with its new version
   and `occurrence_version`. The store method `Unconfirm` runs in one write transaction and puts the match back exactly as
   the suggestion was: `decided_by`, `decided_at` and the new `decided_by_via` are cleared; the tolerance snapshot of
   the confirmation is cleared (D41 3a); `reason` is the suggestion's reason again (the reason of the history entry
   before the confirmation); the amount is unchanged (a correction closes the window, point 2). The occurrence's payment
   state is derived again without the match, so it is what it was before the confirmation; its version increases. The
   history gains an `unconfirmed` entry carrying the reason given, and the audit log a `payment_match.unconfirmed` entry.
   Migration 0023 widens the transition trigger (confirmed may become suggested; rejected and reversed stay final) and
   adds a trigger that refuses a suggested match carrying a decision or a tolerance.
2. **Who and when (the server-side undo window).** All of these are checked inside the write transaction; any failure
   leaves the match untouched:
   - The caller is the decider: the user who confirmed it, whether they came by session, API token or MCP (a token acts
     as its owner, D33 and D42 3a). Anyone else gets 403 `match_undo_not_decider`; automation is refused outright and a
     confirmation automation made has no person to undo it (D33). The `commitments:write` grant is still required.
   - The match was a suggestion: a match a person created confirmed (origin `user`) was never one, so it gets 409
     `invalid_match_state` and is undone with unmatch.
   - The confirmation is still the match's latest change (no correction since).
   - It is still the person's latest match decision: no history entry by that user on any match was written after it.
     This is the server's view of "replaced by the next payment" (D37).
   - Nothing else changed the occurrence since: the history entry of the confirmation records the occurrence's version
     right after it (new column `occurrence_version`), and the occurrence must still have that version. Another person's
     match, a manual payment or its deletion, a status override, a terms change, regeneration and cancellation all bump
     it. A confirmation recorded before migration 0023 has no recorded version, so its window is closed.
   Outside the window: 409 `match_undo_window_closed`; unmatch remains. A stale version is 409 `version_conflict` with
   the current match, as for every other action. There is no time limit (D37 has none).
3. **Provenance on every match event (D42 3a).** `audit.Actor` gains `Via`: `session`, `token`, `mcp` or `system`
   (`Actor.Channel` defaults it from the actor kind). The match handlers set it per request: a session principal is
   `session`; a token principal is `mcp` when the in-process MCP server made the call (a request-context marker,
   `httpapi.WithMCPCall`, that only code in the process can set) or when `ledger mcp stdio` named its tool in
   `X-Ledger-MCP-Tool`, and `token` otherwise; the system and the matching engine are `system`. Migration 0023 stores it
   on every history entry (`actor_via`) and on the match for its creation and latest decision (`created_by_via`,
   `decided_by_via`, the former immutable), with a CHECK that the provenance fits the actor kind. The API shows it as
   `via` on `created_by`, `decided_by` and each history entry's `by` (null for entries written before migration 0023),
   and the `payment_match.*` audit entries carry it as the `via` detail.
4. **Client, MCP and docs.** `ledgerclient.Client.UnconfirmPaymentMatch`, `MatchActor.Via`; the MCP tool
   `unconfirm_payment_match` maps to the route, so its grant, version handling, audit and the per-token MCP rate limit
   are those of every other tool. It is marked a write, not destructive: it is reversible (the suggestion can be confirmed
   again). An MCP tool answer for a 403 that carries its own code (such as `match_undo_not_decider`) now names that code
   instead of suggesting a missing grant.
5. **History order.** The history is read ordered by time and then by insertion (`rowid`), so entries written in the same
   instant keep the order they were written in; migration 0023 copies the old rows in their history order. The window
   checks use insertion order too.

## Why

- Checking the window on the server keeps an old browser tab, a second device or a model from taking back a confirmation
  after later changes were made on top of it; the conditions are what the person can see in the notice (their latest
  change, on an occurrence nothing else touched).
- Recording the occurrence's version on the confirmation entry catches every kind of later change, including ones
  outside the matches tables, without a list of tables to watch.
- Treating a token and MCP as the person follows D33 and D42 3a; recording `via` keeps the difference visible.
- Rejected: a time limit (D37 has none); trusting a client flag that the notice is still shown (a client cannot be the
  guard); restoring by creating a new suggestion (a different match and history, not an undo); making the MCP marker a
  header for the in-process server (a direct API caller could set it).

## Consequences

- **Accepted by the operator (D44):** the window conditions of point 2 (latest match change, the person's latest match
  decision, occurrence untouched); refusing unconfirm for matches created confirmed; marking the MCP tool a write.
- **Limitations:**
  - The server cannot see a dismissal, so the window can stay open after the notice was dismissed; the UI decides whether
    to offer undo. Manual payments carry no user in their table, so the person's manual payment on another occurrence
    does not close the window (one on the same occurrence does, through its version).
  - `via = mcp` from `ledger mcp stdio` rests on a header the client asserts, like the tool name in the audit log; it is
    the same person either way. Rows written before migration 0023 have no provenance (`via` null).
  - The "person's latest decision" check compares history entries by time, then insertion order. Entries of a
    suggested or rejected match can be deleted when its occurrence is regenerated; a later decision lost that way no
    longer closes the window (the occurrence-version check still covers the same occurrence).
  - After an unconfirm the match is a suggestion again, so the history_kept trigger no longer protects its confirmed
    and unconfirmed entries (the same open decision as for rejected matches).
  - If an occurrence with an unconfirmed suggestion is later regenerated away, the suggestion and its history go with it
    (as for every suggestion, ADR-0035); the audit log keeps the decisions.
  - The UI (task X4d) still undoes a confirmation by unmatching until it switches to this route.
- **Checks that keep this true:** `internal/store/matches/unconfirm_test.go` (restoration of match and occurrence state
  and tolerance snapshot, decider matrix, each window condition, concurrent unconfirm and correct);
  `cmd/ledger/matches_unconfirm_test.go` and `cmd/ledger/mcp_unconfirm_test.go` (403/409/428, provenance over session,
  token, in-process MCP and the stdio header, audit); the permission matrix; the MCP grant-parity test; the migration
  0023 fixture check (`internal/store/match_unconfirm_provenance_test.go`).
