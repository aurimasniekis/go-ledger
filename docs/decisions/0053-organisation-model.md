# ADR-0053: Organisation model: categories, labels, buckets and their assignments

Status: Accepted (task O1, 2026-11-22). Decision D12 was taken on 2026-11-20. This increment is the model, the store
and the Management API; the UI and the MCP tools are task O2, the Terraform data sources task T3, and the rule and
model writers of suggestions task L6.

## Context

A person wants to organise what the bank and their commitments show: what a transaction was for, what kind of
commitment it is, which accounts and commitments belong together when the outlook is sliced. Corrections they make must
never be undone by a bank refresh, a rule run or a model suggestion, and what they organise with must not rot when they
rename or retire it.

## Decision (D12)

1. **Categories** are single-valued per object with **two levels**: a category has at most one parent and a parent has
   no parent. The database enforces it with triggers as a backstop to the domain check. **Labels** are many-valued and
   flat. **Buckets** are user-defined groupings of **commitments and accounts** used to slice the outlook; a bucket holds
   no money, allocates none and is not a budget envelope (a test pins that no organisation table has an amount,
   currency or status column). Categories and labels apply to **transactions, commitments, accounts and merchants**.
2. **Definitions** (`categories`, `labels`, `buckets`) are versioned objects with ULIDs, ETag/If-Match concurrency and
   a cursor-paginated, filterable list (`archived`, `name[prefix]`, and `parent_id` for categories). Names are
   trimmed, white space collapsed and unique ignoring case (among siblings for categories), archived ones included; an
   archived definition can be renamed, which frees its name. The id never changes, so a rename reaches every assignment.
3. **Lifecycle: archive first.** Archiving keeps the definition and every assignment to it (history stays readable and
   says `archived`), and stops anything new from using it, by the person or by a rule or model (409
   `definition_archived`; automatic proposals are reported as kept). A category is archived after its children and
   unarchived after its parent (409). **Delete** is for a definition nothing is assigned to; one that is still assigned
   is refused (409 `definition_in_use`, with the count) unless `reassign_to` names an active definition of the same
   kind. The assignments then move to it in the
   same transaction, and the audit event records how many. The person's own rows move as the person's (who and when
   become the mover's); a rule's or model's row keeps its origin, source, who set it and when, and only its definition
   changes: the person's decision concerns the definition, not each object's classification, and a rule or model
   assignment stays correctable by the person and by a stronger automatic origin. A category with children is never deleted. Where the person
   removed a label from an object, that remembered removal goes with the label.
4. **Origin.** Every category and label assignment records its origin: `user`, `rule` or `model`, who/what set it
   (user, token, system and how it came: session, token, mcp, system), when, and for automatic origins a `source`
   (the rule, or provider/model). Precedence is user over rule over model (`organisation.CanReplace`, pure and
   tested exhaustively): nothing automatic replaces a user assignment, a model never replaces a rule's, a rule or model
   replaces its own origin or a weaker one.
5. **The person's word survives.** The person's "no category" is a stored assignment (a row without a category) and a
   label the person removed is a stored removal; automatic runs respect both and report them as kept. Handing the
   decision back is explicit (`DELETE .../category`). Assignments live in their own tables keyed by the object, never
   in the bank tables, so a refresh, a re-import or a pending item booking in place does not touch them; tests re-import
   and check the rows are identical, and that assigning leaves every bank fact byte-for-byte unchanged. A booking that
   arrives under a new key supersedes the pending row: like merchant decisions (ADR-0051) its category and labels are
   inherited read-time along the `superseded_by` chain (marked `inherited`), nothing is copied, and the booking's own
   decision replaces the inherited one, unless the booking's own row is automatic and the inherited one is the person's:
   the person's word wins (`organisation.CanReplace`, per category and per label, removals included). Resetting a
   category that is only inherited is refused with 409 `review_inherited`, as for merchant decisions: reset it on the
   pending transaction.
6. **Referential integrity.** The typed object columns carry the foreign keys: a bank transaction or account with a
   user decision or a bucket membership blocks a purge of the connection's bank facts (`ErrPurgeBlocked`), as payment
   matches and merchant decisions do; assignments a rule or model made are derived and are deleted by the purge first;
   a deleted commitment takes its assignments with it: by cascade when the row is removed, and by an explicit delete in
   the same transaction when the row is kept as deleted (D31, an occurrence that only deleted payments refer to), so no
   bucket, category or label refers to a commitment no read shows. Definitions are referenced with `ON DELETE
   RESTRICT`.
7. **Merchant name keys are PROVISIONAL**, pending an operator decision on a merchant entity (see limitations). **Merchants have no record of their own yet** (ADR-0051 identifies the merchant of a transaction, it keeps no merchant
   table). A merchant is therefore named by its **key**: the name with white space collapsed and lower-cased (at most 200
   characters). This is an assumption, listed under limitations: if a merchant entity is introduced later (task E2 or
   D15 reuse), assignments migrate to its id. Because a key derives from bank text, the access log replaces the key in
   the path (`/organisation/merchant/{object_id}`), the audit log does not identify a merchant at all (a digest of a
   guessable name can be reversed), and every merchant-keyed organisation route needs the admin-only `merchants` grant
   (`merchants:read` to read, `merchants:write` to change), as the merchant enrichment does (ADR-0051).
8. **API** (grants `organisation:read` and `organisation:write`; the read-only role holds read, since this is the person's
   own labelling and not bank text): `/categories`, `/labels`, `/buckets` (list, create, get, PATCH merge patch with If-Match,
   `POST .../archive`, `POST .../unarchive`, `DELETE ...?reassign_to=` with If-Match), `/buckets/{id}/members` (list,
   `PUT` and `DELETE` per object), and per object `GET /organisation/{object_type}/{object_id}`,
   `PUT|DELETE .../category` and `PUT|DELETE .../labels/{label_id}`. Assignments are last-write-wins (no If-Match), like
   merchant decisions: one person, one decision per object, the audit log keeps the history. Rules and models write
   through the store's `ApplyAutomatic`, which is deliberately not an HTTP route in this increment.
9. **Audit** (names are free text and never recorded): `organisation.definition_created|updated|archived|unarchived|
   deleted` (kind, via, changed field names, reassigned count), `organisation.assignment_changed` (kind, change, origin,
   the origin it replaced, via) and `organisation.automatic_applied` (one event per object an automatic run changed;
   none when it changed nothing).

## What remains and limitations

- **Outlook slicing by bucket** is not built: buckets exist, hold commitments and accounts and can be listed by
  member, but no outlook or monthly view reads them yet. That belongs with the outlook work that consumes buckets.
- **Rules and the model writers** (the callers of `ApplyAutomatic`) are L6 and the rule engine; nothing automatic is
  wired. The precedence is implemented and tested at the store.
- **Merchant key** is PROVISIONAL (point 7), not a decision of the operator. A name-derived key changes when a name is
  corrected or spelled differently (assignments are orphaned silently), is undefined for uncertain or rejected
  resolutions, collides for different merchants with one name, has no Unicode normalisation, no foreign key and no
  lifecycle. The operator must choose between a minimal merchant entity (id, display name, alias keys; assignments keyed
  by its id) and keeping name keys with a documented migration, before O2, L6 or E2 build on it.
- **UI, MCP tools, Go client and CLI methods** (O2) and **Terraform data sources** (T3): the routes are usable through
  the client's raw call and the OpenAPI document until then.
- **Bulk assignment**, category reporting (spend by category) and a category tree read are not built.
- Reassigning on delete keeps a rule's or model's row automatic (origin, source, who and when unchanged); only the
  person's own rows are recorded as moved by the person.
