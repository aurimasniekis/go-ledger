# ADR-0024: Recurring commitment definitions

Status: Accepted (2026-11-21)

## Context

Task C1 records recurring commitments (services, servers/VPS, domains, subscriptions, debt repayments): name, kind,
an exact amount with its currency, when they fall due, the dates they run between, a priority and notes, with a CRUD
API and audit. The requirements keep recurring definitions apart from dated occurrences, keep paid history stable
across schedule edits and keep user data apart from bank facts (ADR-0013).

The operator's decisions that apply (work/decisions.md):

- D5: any ISO 4217 code, decimal amounts that keep the source scale, no implicit conversion.
- D6: rules are monthly on day N, every N months, and annual on a date; a day past the end of a month clamps to its
  last day; an annual February 29 falls on February 28 in years that are not leap years; edits apply from an
  effective date forward, regenerate only unpaid future occurrences, and paid history is immutable.
- D14: priority has three levels, essential / important / flexible.

When C1 was planned it was PARTIAL on D6. D6 has since been answered, so the definition side is built in full here.
Occurrence generation and how edits reach occurrences remain task C2.

## Decision

1. **The definition is its own table, `commitments` (migration 0007).** It holds the definition only: no occurrence,
   due date, payment or bank columns. Occurrences, payment links and status are derived later (C2, C3) and go in
   their own tables, which refer to `commitments` with `ON DELETE RESTRICT`. A test pins the column list.
2. **Kinds:** `service`, `server` (servers and VPSs), `domain`, `subscription`, `debt_repayment`. **Priority:** the
   D14 scale, nullable ("not prioritised yet"), so Ledger never has to make up a default. The table CHECKs both
   lists; adding a value means rebuilding the table.
3. **Amount:** a positive `money.Money` (ADR-0005), stored as decimal text plus currency, scale kept as entered. It is
   never converted or summed in SQL.
4. **The schedule spec is versioned JSON.** Every value carries `"version"`. Version 1 is
   `{"version":1,"frequency":"monthly","interval":N,"day":D}` (interval 1 to 120 months, day 1 to 31) or
   `{"version":1,"frequency":"annual","month":M,"day":D}` (a day that exists in M in a leap year, so 29 February is
   allowed). Members that do not belong to the frequency are refused. The D6 calendar rules are part of the
   version 1 spec: day 31 means "the last day" in short months, and an annual 29 February means 28 February in
   common years. A monthly interval counts from the month of `start_date`. A later spec (weekly rules, say) is
   added as version 2 beside it. Version 1 never changes meaning, so stored version 1 values never need a
   migration. The column CHECKs that the value is a JSON object with an integer version. Code checks the members.
5. **Dates:** `start_date` is required, `end_date` is optional and inclusive, and `end_date >= start_date` holds in code
   and in SQL.
6. **API:** `/api/v1/commitments` supports list (filters `kind`, `priority`, `currency`), create, get, merge-patch
   PATCH and DELETE, following ADR-0011/0015/0017 (ETag versions, If-Match or the body version, 428/409 with
   reconciliation). A PATCH is applied with RFC 7396 to the writable members, so `amount` and `schedule` merge
   member by member. The merged result is then validated as a whole. Grants are `commitments:read` and
   `commitments:write` (ADR-0021). The resource is not administrative, so the read-only role can read it.
7. **Validation errors are reported per field, all at once.** The shared error envelope gains an optional
   `errors` array of `{field, message}`. `field` and `message` still name the first problem, so existing clients keep
   working. Nested members are named with dots (`schedule.day`, `amount.currency`). Single-error responses look the
   same as before.
8. **Audit** goes through `store.AuditWriter` in the write transaction: `commitment.created` (kind, currency),
   `commitment.updated` (names of changed fields), `commitment.deleted` (kind). Details never carry the name, the
   notes or amounts. A change that alters nothing writes nothing and is not audited.
9. **DELETE removes the row.** No occurrences exist yet. Once they do, their foreign keys make deleting a commitment
   with history fail with 409 conflict (`commitments.ErrInUse`), and setting `end_date` is how a commitment is
   stopped.

## Why

- A separate definition table with a versioned spec decides nothing about occurrence storage, payment state (D7) or
  how edits reach occurrences (D6 edits, C2). Those tasks add their own tables without touching this one.
- JSON for the spec (rather than one column per rule member) lets version 2 add members without a table rebuild,
  while Go parsing keeps it strict. A column per member was rejected because each new rule form would be a rebuild.
- A nullable priority was chosen over a default, because a default would invent a policy D14 did not set.
- Reporting every invalid field was chosen over the first error only, because the acceptance criterion asks for
  validation errors per field, and a form can show them all at once.
- A hard delete is acceptable now because nothing refers to a definition yet. Soft-delete or archive semantics were
  not specified (D11's archive-first rule covers categories, labels and buckets), so none were invented.

## Consequences

- C2 must read the schedule through `commitments.ParseSchedule` / `Schedule`, apply the D6 clamping and Feb 29 rules,
  and decide (and document) what the first occurrence is when `start_date`'s day is after the scheduled day.
- C2 must implement D6 edits: apply a definition change from an effective date forward, regenerating only unpaid
  future occurrences. The definition table keeps only the current terms. If C2 needs history of earlier terms, it
  adds a revisions table rather than reinterpreting this one.
- Occurrence tables must refer to `commitments(id)` with `ON DELETE RESTRICT` so that DELETE becomes 409 once
  history exists.
- Left open and not built here: the per-commitment amount tolerance of D9 (task X2), leeway and grace details (D14,
  DF2), categories, labels and buckets on commitments (O1/O2), outbox events for commitment changes, the Terraform
  resource and MCP tools (T-series and P-MCP tasks), and the UI (C4).
- Migration number: this branch uses 0007 and adds the version 6 fixture that TestFixtureCoverage requires. If
  another migration is merged first, renumber to the next free number and keep the fixture for the previous version.
