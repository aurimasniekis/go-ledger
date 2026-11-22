# ADR-0057: Organisation UI and MCP tools

Status: Accepted (task O2, 2026-11-22). Builds on ADR-0053 (the model and the Management API).

## Context

ADR-0053 delivered categories, labels, buckets and their assignments as a Management API. A person needs to manage the
definitions, assign them where the item is shown, see whether a category was their own choice or automatic, and an MCP
client needs the same operations with the same validation.

## Decision

1. **MCP tools are typed calls of the API routes** (ADR-0038), generated for the three kinds (`list_|get_|create_|update_|archive_|unarchive_|delete_` +
   `category|label|bucket`; `list_categories`, `list_labels`, `list_buckets`) plus `list_bucket_members`, `add_bucket_member`, `remove_bucket_member`,
   `get_object_organisation`, `set_object_category`, `reset_object_category`, `add_object_label`, `remove_object_label`. They hold no
   rule of their own: validation, archive-first, `reassign_to`, origin and audit are the route's. Grants are the route's
   (`organisation:read` and `organisation:write`); `TestMCPHTTPGrantParity` checks every tool against its route and against
   real tokens, and the schemas refuse unknown members. Every removal is marked destructive.
2. **A merchant is not an MCP object type**, and the UI has no merchant assignment. A merchant has no record of its own and
   is keyed by a provisional name key (ADR-0053 point 7, operator decision D-MER 1a, task M1); its routes need the
   admin-only merchants grant, which no organisation tool declares. The tools' `object_type` is
   `transaction|commitment|account`. This waits for the merchant entity.
3. **The Go client** gains the 29 operations (`ListDefinitions`, `CreateDefinition`, `ArchiveDefinition`, `SetObjectCategory`, ...) keyed by
   kind, and their `notImplemented` rows are gone; the OpenAPI contract tests cover the operations and the types. The CLI commands for the same operations were built in O4 (ADR-0053).
4. **The UI** is `/organisation` (management of the three kinds: tree of categories with two levels, find by name, show
   archived, rename or move, archive with Undo and no confirmation, delete with a replacement chosen when the definition is still
   assigned, bucket members) and an **Organise** disclosure on an occurrence of the monthly view (a commitment), on a bank
   account of the connections page, on the bank transaction of a payment match, and on **/commitments**, a list of every
   recorded commitment (name, amount with currency, schedule, validity; the API gives no next due date, so none is shown) shown
   only with `organisation:read` and `commitments:read`, so a commitment with no occurrence in the month on screen can be organised. The disclosure reads the object's
   assignments only when opened, so a long list costs no request per row.
5. **Visible origin and override.** Each category and label says whether it is the person's choice, automatic by a rule or by a model (with the source)
   and whether it is inherited from the pending transaction the booking replaced. Choosing a category makes it the person's
   (the screen says so for an automatic one); "No category" is an explicit choice; "Hand back to automatic" is separate and
   absent for an automatic or an inherited one (the server refuses it: `review_inherited`). A removed label is listed with Put back.
6. **Saved means answered.** Every change runs as a request; the screen says "Saving…" while it runs and "Saved: ..." only once the
   server answered, and a refusal or lost connection says "nothing was changed". A stale version reloads the list.
   Archived definitions are not offered in pickers (the server refuses them: `definition_archived`).
7. **Bucket filter on the monthly view.** A select narrows the occurrences to the commitments in a bucket (the bucket's members
   are read exactly, not guessed). A bucket holds no money, so the totals keep covering the whole month and the note says
   so; while the members load no row is shown, and when they cannot load every row is shown with an alert.

## Limits and the tasks that own them

- **Category and label filters** of lists and a category or label column: the API had no inverse query. **O3** (ADR-0059) adds
  the server filters and the list filters, so this limit moves there.
- **Slicing the outlook by bucket**: **O3** (ADR-0059) adds the outlook bucket slice; only the monthly view is filtered in this
  increment. Buckets in several currencies are never added together.
- **Bulk assignment** is not built.
- **Merchants** (point 2): merchant organisation is **O5**, on the merchant records of ADR-0058.
- **CLI** commands: built in **O4** (ADR-0053).
- Verified with a stateful fake of the API in component tests and a stubbed API in the browser. The journey spec
  (`web/e2e/journey.spec.ts`) runs against the real binary with its embedded UI, not stubs. The screens were not run
  against a live bank, and no screenshot review by a person has happened.
