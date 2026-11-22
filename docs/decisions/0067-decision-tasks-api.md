# ADR-0067: Decision tasks API: suggestions and alerts

Status: Accepted (task L6c, 2026-11-22). Builds on [ADR-0065](0065-decision-tasks.md), which left routes, OpenAPI, the
client, the CLI and MCP open. The review UI is task L7.

## Decision

1. **Routes** under `/api/v1`: `GET /suggestions` (filters `state`, `task`, `object_type`, `object_id`, each repeatable where
   it is an enum; cursor pages in ID order, oldest first), `GET /suggestions/{id}`, `POST /suggestions/{id}/accept|correct|reject|revert`,
   `GET /alerts` (filters `state`, `kind`, `object_type`, `object_id`), `GET /alerts/{id}`, `POST /alerts/{id}/dismiss`. Actions are
   `POST` verbs like the archive and match actions; a state moves once, so there is no `If-Match` (a second action is a structured
   409). Only categorisation and label suggestions are listed: the alerts task's answer is attached to the alert as `model_text`.
2. **Grants.** A new resource `decisions` (read, write), administrator only like the other model- and bank-facing grants. Accept,
   correct and revert write an assignment, so they also require `organisation:write` (and `merchants:write` for a merchant object),
   checked in the handler against the suggestion's object. A `decisions:write` token alone can reject suggestions and dismiss alerts
   but cannot reach an assignment. MCP tools carry the route's `decisions` grant; the server names the second grant when it is missing.
3. **Provenance.** Every suggestion carries `suggested_by {provider, model, label}` and `provenance: "suggested by provider/model"`,
   and keeps them after it is accepted. An alert carries `model_text {text, provider, model, label, provenance}` or null; `message`
   is always Ledger's own text.
4. **State.** The API `state` is the stored state, or `reverted` when an accepted auto-applied suggestion has a reversal row (the
   stored state stays `accepted`; `reversal` says who and when). The filter `state=accepted` excludes reverted ones. A correction is
   a rejection with the person's own answer and reads `rejected` (the audit action `decision_suggestion.corrected` tells them apart).
   Alerts show `open`, `dismissed` (a person) or `resolved` (the condition stopped holding) with `closed_at`.
5. **Errors** (all 409, codes added to the Error enum): `suggestion_not_pending` (`ErrNotPending`), `not_reversible`
   (`ErrNotReversible`: not auto-applied, already reverted, or the category changed since), `assignment_changed`
   (`organisation.ErrAssignmentChanged`), `nothing_to_apply` (the model chose none), `wrong_task`, `alert_not_open`. An archived
   category or label is `definition_archived` and the suggestion stays pending.
6. **Audit** is the stores' own (`decision_suggestion.*`, `alert.closed`, and the organisation assignment events); the routes add none.
7. **Wiring.** `ledger serve` builds the task service without a decision runner: reviewing never calls a model. The stores gained
   list queries (`ListSuggestions`, `SuggestionView`, `Find`); suggestions join the reversal row for the derived state.

## Limitations

- Choice identifiers are returned as identifiers; names are read from the categories and labels routes (the review UI joins them).
- No `If-Match` on suggestion actions (a state moves once) and no list of reversals beyond the suggestion's own `reversal`.
- Nothing yet raises alerts or requests suggestions (triggers and detectors are separate tasks), so the lists stay empty until they land.
- Terraform has no resource for these (actions, not configuration).
