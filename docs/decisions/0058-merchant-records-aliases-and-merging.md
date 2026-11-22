# ADR-0058: Merchant records, aliases and merging

Status: Accepted (task M1, 2026-11-22). Operator decision D-MER 1a (2026-11-22): a minimal merchant record (id, display
name, alias names); confirmations and corrections (E1, D15) attach to it; organisation assignments (O1) key on the
merchant id with a foreign key; the merchant screens (E3) manage it, including merging duplicates. It supersedes the
provisional name keys of [ADR-0053](0053-organisation-model.md) point 7 and amends
[ADR-0051](0051-merchant-model-and-corrections.md) and [ADR-0054](0054-merchant-enrichment-sources.md). The screens
are task E3, not this one.

## Context

E1 identified the merchant of each transaction but kept no merchant of its own; O1 therefore named a merchant by a
case-folded name key, so renaming a merchant or meeting a new spelling lost its assignments, and two spellings could
never be one merchant. The PR #124 reviews also asked that a person's decision about one transaction re-resolve the
other transactions of the same merchant, and that a change of confirmations reach every older "none" answer, not one
batch.

## Decision

1. **Merchant record** (`merchants`, migration 0030): a stable ULID id, a display name (1 to 200 characters, trimmed,
   white space collapsed, Unicode NFC), `created_at`/`updated_at` (UTC), `archived_at`, `merged_into_id` and a
   version. Archived keeps the merchant and everything that refers to it, but nothing new is attached to it by id (a
   correction naming it, an organisation assignment: 409 `merchant_archived`); its aliases still resolve names to
   it. A merged merchant is archived with `merged_into_id` set, so its id still resolves.
2. **Aliases** (`merchant_aliases`): a name and its **key**, the name as matched: Unicode NFC, white space collapsed,
   lower case (`merchants.AliasKey`, `golang.org/x/text/unicode/norm`). A key names one merchant at most (UNIQUE;
   409 `merchant_alias_taken`). Origin `user` (the API), `decision` (a confirmation or correction named it) or
   `upgrade` (migration 0030). Creating a merchant makes its display name the first alias; a rename adds the new name
   as an alias unless the merchant has it, and is refused when another merchant has it (merge them instead).
3. **Decisions attach to records.** `merchant_reviews.merchant_id` (FK, RESTRICT) is set for a confirmation or a
   correction and NULL for a rejection (CHECK). A correction by name and a confirmation resolve the name through its
   alias key; when no alias has it, a record is created with the name as its first alias (audited
   `merchant.created`, origin `decision`). A correction may name a record by `merchant_id` instead (active only);
   the decision then records the display name. The decision keeps the name as written. A confirmation stays applied
   when the source reports another spelling of the same merchant (the answer resolves to the same record), not only
   the very name confirmed.
4. **Enrichment resolves a name to an alias, then a merchant.** `merchant_enrichments.merchant_id` (FK, SET NULL:
   enrichment is derived data) is the record an identified name resolves to through its alias key, or NULL; the
   answer keeps its own provenance (`source`, `confidence`). Adding an alias resolves identified answers with that
   key; removing it clears them. The resolved merchant shows the record's current display name, so a rename shows
   everywhere. Enrichment never creates a record.
5. **Organisation assignments key on the id** (supersedes ADR-0053 point 7). `category_assignments` and
   `label_assignments` gain `merchant_id` (FK, RESTRICT) and, for a merchant, `object_id = merchant_id` (CHECK).
   `/organisation/merchant/{object_id}` takes the merchant id (400 for a name; 404 for an unknown id). The merchants
   grant still gates these routes (names derive from bank text, ADR-0051 point 9), and the audit target of a merchant
   assignment is now the merchant id (not derived from a name).
6. **Merge** (`POST /merchants/{id}/merge`, If-Match of the remaining merchant): in one write transaction the merged
   merchant's aliases, the decisions and answers that refer to it and its organisation assignments move to the
   remaining one. Where both have an assignment for the same slot (the category, or the same label), the remaining
   one's stands unless the other's is the person's own and the remaining one's is automatic (a person's word
   outranks a rule's or a model's, as in ADR-0053); assignments not moved are counted (`kept`). The merged merchant is
   archived with `merged_into_id`; merchants merged into it earlier now point at the remaining one. The remaining one
   must be active; the merged one may be archived but not merged. One audit event `merchant.merged` (target the
   remaining merchant) carries `merged_from` and the counts.
7. **Management API** (grants `merchants:read`/`merchants:write`, administrator-only like the rest of the merchant
   routes): `GET/POST /merchants`, `GET/PATCH/DELETE /merchants/{id}`, `POST /merchants/{id}/archive|unarchive`,
   `POST /merchants/{id}/aliases`, `DELETE /merchants/{id}/aliases/{alias_id}`, `POST /merchants/{id}/merge`;
   versions with If-Match as everywhere. Delete only when no decision, assignment or merged merchant refers to it, and never a merchant that was itself
   merged (409 `merchant_in_use`; merged ids keep resolving). The transaction merchant read, the review and the enrichment gain `merchant_id`; the review
   request accepts `merchant_id`. Audit actions `merchant.created|updated|archived|unarchived|deleted|alias_added|
   alias_removed|merged` carry ids and counts (a merge: aliases, decisions, enrichments, categories, labels, kept, and the
   replaced automatic assignments), never a name or an alias; decision events gain `merchant_id`.
8. **Re-resolution per merchant** (PR #124 review). A decision (or clearing one) about a transaction flags
   (`merchant_enrichments.recheck`) the identified answers of other, undecided transactions of the same merchant (same
   record, or the same name when it has none); the next enrichment pass looks them up first after never-looked-up
   rows, and any recorded answer clears the flag. **Rechecks of none reach every row**: the service keeps a watermark
   (when the person's confirmations last changed, or the first pass after a start) and every none or ambiguous answer
   older than it is listed, batch after batch over as many passes as it takes; rows answered since are not asked
   about again, and a row whose re-lookup failed is not listed again until the recheck interval has passed since the
   failure (the backoff of any failed lookup). The 24-hour recheck with a remote source is unchanged.

## Migration 0030 (forward-only, deterministic)

The backfill runs in the migration (one transaction, foreign keys checked at the end). The SQL calls
`ledger_merchant_key`, a function the store registers with the SQLite driver that runs `merchants.AliasKey`, so the
keys it writes are the keys the store computes. Alias keys come from the names of confirmed and corrected decisions
and from the merchant keys of O1 assignments; one merchant per distinct key, ordered by key, with the time-zero id
`0000000000M` + the ordinal in 15 digits (no digest of a name, which could be reversed with a dictionary) and one
alias `0000000000A` + the same ordinal. The display name is the name of the most recent decision with the key (ties:
lower transaction id), else the key itself; `created_at`/`updated_at` are the earliest and latest decision or
assignment time. Decisions (other than rejections), identified answers and merchant assignments then point at their
merchant. It is lossless: decision names, versions, provenance and every assignment are kept. It is **refused**
(`ledger_refuse`, nothing changes, the error names how many) when two O1 keys become one key (they differed only in
Unicode composition) for the same category slot or label, or a key is not valid display text; remove the duplicate
with the earlier version, then upgrade. The fixture `0029_deferral_fee_paid.sql` carries representative rows (case
and composition variants, every answer status, a rejection, assignments with and without a decision) and the harness
checks the result; a second test checks the refusal.

## Consequences and limits

- Same-key spellings are one merchant; different keys (`Lidl` and `Lidl Vilnius`) are two until the person merges
  them or adds an alias. Nothing merges automatically.
- Upgrade-made display names may be lower-case keys (a merchant only O1 knew) or a decision's spelling as typed
  (not recomposed); rename them in E3.
- A merge cannot be undone in place (the audit log keeps what moved; a person re-creates and re-assigns).
- The Go client methods, CLI commands, MCP tools and Terraform resources for merchant records are not built here (the
  routes are usable through `Client.Raw`); they belong with the merchant screens (E3) and their slice of P-MCP2 and
  the provider, and are listed in the client's `notImplemented` table. (Update, task E3: the client methods now exist,
  see [ADR-0061](0061-merchant-ui.md); CLI, MCP and the provider remain.)
- `ledger_merchant_key` and `ledger_refuse` must stay registered while migration 0030 ships.
