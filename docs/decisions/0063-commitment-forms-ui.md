# ADR-0063: Commitment forms: create, edit, end and delete in the UI

Status: Accepted (2026-11-22). The choices marked "conservative" are implementation choices where no operator decision exists; they are listed
under Consequences.

## Context

The screenshot tour showed no way to create or edit a commitment in the UI: only the Management API could. Recurring commitments are an
essential journey, so `/commitments` (until now "Organise commitments": names and Organise) gets forms. The server rules are in ADR-0024 (the
definition, PATCH as a merge patch with a version, per-field errors), ADR-0027 (how an edit reaches occurrences, the optional
`effective_from`, D31 cancellation on delete), ADR-0039 (match tolerance) and ADR-0052 (leeway, priority). The API has **no restore and no
archive endpoint**: stopping a commitment is `end_date`, and DELETE removes it (or archives it server-side when cancelled history remains).

## Decision

1. **Pages.** `/commitments` lists every commitment with kind, exact amount, a plain-language schedule and its dates, an Edit link (View
   without `commitments:write`) and the existing Organise control; "New commitment" sits above the list and in its empty state.
   `/commitments/new` is the create form; `/commitments/:id` is the edit form with End and Delete. The monthly view's empty state links to
   "New commitment" for a role holding `commitments:write`; the account menu gets a Commitments group (`commitments:read`).
2. **TanStack Form** (`@tanstack/react-form`, added with this change: no earlier form used it) holds every field as text, so an amount stays the
   decimal string typed (scale kept, ADR-0005) and is sent as that string, never through a number. Client checks only save a round trip (name,
   positive decimal, three-letter currency, day and interval ranges, ISO dates, end after start); the server stays authoritative and its
   structured `errors[]` (`amount.amount`, `schedule.day`, `leeway.days` …) are mapped onto the fields. An error with no field is shown as the
   form message. Typing in a field clears that field's server message.
3. **Nothing is lost, nothing is claimed early.** The save button reads "Saving…" with the "Not saved yet" badge while the request is open;
   a refusal or an unreachable server leaves every entry in place with a "Not saved" badge and says nothing was saved. The page changes only
   after the server confirmed (the created commitment's page, or the cached commitment replaced by the server's answer).
4. **Edit sends a merge patch of what changed**, with the loaded `version` in the body. A changed nested member is sent whole, with explicit
   nulls for members that go away (switching monthly to annual sends `interval: null`; clearing leeway sends `leeway: null`). A form with no
   change sends nothing. A 409 `version_conflict` keeps the entries, never resends the stale version, and offers "Load the latest version"
   (which replaces the entries with the server's current ones). There is no merge of concurrent edits (the concurrent-edit policy is an open
   operator decision).
5. **How an edit affects occurrences is stated from the server's rules, not invented** (ADR-0027 point 4 and 6): name, kind, priority, notes,
   match tolerance and leeway never touch occurrences; a changed amount, schedule, start or end date applies from the effective date forward
   (default today in the reporting zone); paid occurrences never change; unpaid ones due before it keep their terms, overdue ones included;
   unpaid ones on or after it are rewritten or removed; empty months get the new occurrence. The optional "Apply changed terms from" field sends
   `effective_from` only together with such a change.
6. **End = set the end date, with Undo.** "End commitment" opens a dialog with an end date and PATCHes `end_date` (no `effective_from`: the
   server's default keeps past and overdue unpaid occurrences). The notice that follows offers **Undo**, a second PATCH restoring the earlier end
   date (or none); like every save it is shown only once the server confirms. Because the PATCH is also applied from today, the unpaid
   occurrences the end removed come back from the current terms. The notice goes when the page is left; later the end date can still be edited.
7. **Delete has a confirmation and no Undo**, because the API has no restore. The dialog says what the server does (removes the commitment and
   its unpaid occurrences, refuses with 409 when any occurrence has payment evidence) and points to End as the safe way to stop. A refusal keeps
   the dialog open with the server's message.
8. **Grants.** Without `commitments:write` the New and Delete/End controls are hidden and the form is read-only with a note naming the missing
   grant; the server still decides every request.

## Why

- A patch of changed members keeps a concurrent change to another field from being overwritten silently, and keeps the audit's "fields changed"
  list truthful (ADR-0024 point 8).
- Making End an end-date edit (not a delete) follows ADR-0024 point 9 and ADR-0027 point 9: ending is the supported way to stop, delete is for
  mistakes, and only an end date can be undone with the existing API.
- Showing server messages per field, rather than a generic failure, is what ADR-0024 point 7 built the `errors[]` envelope for.

## Consequences

- **Conservative choices, not operator decisions:** the End dialog never reaches back over overdue unpaid occurrences (use the edit form's
  "Apply changed terms from" for that); a currency change is allowed in the form and left to the server (it refuses it while a deferral plan is
  active); the End dialog's default date is the browser's today, not the reporting zone's (the dialog states the date it will send).
- Open product decisions that stay open: concurrent-edit policy (a conflict discards the entries), whether delete should become an archive that
  can be restored, and whether Undo should survive leaving the page.
- Not covered: a day that does not exist in an annual schedule's month (30 February) is checked by the server and shown on the field, not by the
  form. The commitments list rows (amount and schedule) are built here because the earlier row change had not landed on `work`; a merge may
  need a trivial conflict resolution in `commitments-page.tsx`.
