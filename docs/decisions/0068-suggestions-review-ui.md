# ADR-0068: Suggestions and alerts review screen

Status: Accepted (task L7, review UI only, 2026-11-22). Builds on [ADR-0065](0065-decision-tasks.md), [ADR-0067](0067-decision-tasks-api.md)
and [ADR-0064](0064-primary-navigation.md). The provider, model and consent settings screens are a separate task (L7b), waiting on the settings API.

## Decision

1. **One page, `/suggestions`**, "Suggestions" in the primary navigation, shown only with `decisions:read` (an administrator grant). Four views
   behind a button group like Payment matches: Needs review (state `pending`), Decided (accepted, rejected, superseded, reverted), Open alerts, Closed
   alerts (dismissed, resolved). The page is a review queue, not a settings page.
2. **Provenance is always on the card**: the server's own `provenance` text ("suggested by provider/model") is shown as written, before and after a
   decision; an alert's model wording is set apart, labelled "worded by provider/model" and marked as extra to Ledger's own message, which is always shown.
3. **A suggestion is a proposal.** Accept, Correct (the person's own category or labels, recorded by the server as a rejection of the suggestion), Reject
   and Revert (an auto-applied category) call the ADR-0067 routes. Nothing is shown as saved until the server answers; a refusal (409 codes of ADR-0067)
   is explained in words, changes nothing, and reloads the list. There is no optimistic update. The explanation is raised to the page (one focused
   `role="alert"` notice, `role="status"` for a save), not kept in the card: the reload that follows a refusal such as `suggestion_not_pending` or
   `alert_not_open` removes the card from the Needs review or Open alerts view, and an explanation that lived in the card would vanish with it and leave focus on the page body.
   Decisions are **final by design** (operator decision): Accept, Correct, Reject and Dismiss cannot be undone from here, and the helper text says so before
   the click; only an auto-applied acceptance has Revert, worded "automatic assignment" because labels are revertible too. Cancel on the Correct form returns
   focus to Correct…, and opening it moves focus into the form. Cards use h2 under the page h1; a commitment links to its own page and a merchant shows its name.
4. **Grants in the UI** mirror the server: Accept, Correct and Revert need `organisation:write` (and `merchants:write` for a merchant); Reject and
   Dismiss need `decisions:write`. A missing grant hides the button and says what is missing; the server still enforces.
5. **Visible states**: loading, failed, disconnected and stale come from the shared query state (last-updated time, stale notice); an action in flight
   shows "Not saved yet"; superseded suggestions use the Stale badge; every state is an icon plus words. Offline turns every action off.
6. **Names**: choices come as identifiers (ADR-0067 limitation); the page joins them with the category and label lists (`organisation:read`). Without that
   grant, or if the read fails, identifiers are shown and correcting is off, and the page says so. An id that is not in the loaded list is "an archived or
   unknown category" rather than guessed.
7. **Navigation width.** The eighth primary link wraps to a second row where the row is full (the list already wraps); the header e2e check still holds at 320 to 1280 px.

## Limits

- Suggestion objects other than commitments and merchants show as type plus identifier (no transaction or account detail route is read here).
- The e2e (`web/e2e/suggestions.spec.ts`) stubs the API in the browser against the embedded UI, like the merchants spec: nothing yet raises suggestions or
  alerts without a live model provider, which tests must not use. It needs no extra server, so no new port was taken. The stub can answer 409 and flip the
  state, which covers the explanation and focus in a real browser. A server-backed 409 test is not included: the binary has no way to seed a suggestion
  without a model, and a test-only seed hook would be product code; the 409 codes themselves are covered by the store and API tests of ADR-0067.
- Pagination loads on request ("Load more"); a decided list can be long.
