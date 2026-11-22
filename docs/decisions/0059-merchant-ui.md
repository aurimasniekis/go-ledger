# ADR-0059: Merchant screens and the merchant client

Status: Accepted (task E3, 2026-11-22). Builds on ADR-0051, ADR-0054 and ADR-0058. The wording and layout choices are this
task's own, within those ADRs; none is an operator decision.

## Decisions

1. **Screens.** `/merchants` lists merchant records (name, other aliases, an Archived or Merged mark; a name or alias
   filter over the loaded records, and a switch for archived ones). `/merchants/<id>` is one record: rename, archive and
   unarchive (with Undo), delete, add and remove an alias (removal offers Undo, which adds it back), and merge another
   merchant into it. They are linked from the Account menu under "Merchants" and need `merchants:read`; every change
   needs `merchants:write`, and without it the controls are absent and the page says why.
2. **Nothing is shown as saved before the server says so.** Every change disables its controls and says "Saving…" while
   pending; the result notice appears after the answer. A failed change shows a plain sentence and says "Nothing was
   changed": `merchant_alias_taken` (merge the two merchants, or remove the alias from the other), `merchant_archived`,
   `merchant_in_use` (archive or merge instead), `version_conflict` (the record is reloaded; nothing is retried
   automatically), no connection, and a missing grant. A name or alias error also appears beside the field.
3. **Merge preview.** There is no preview endpoint, so the preview is composed from what the client already holds: which
   merchant is merged and which remains, the alias names that move, and the rules of ADR-0058 point 6 in words (the other
   things that move are counted by the server and reported after the merge). The merge needs an explicit acknowledgement
   that it cannot be undone in place, and its If-Match is the remaining merchant's version. Only active merchants are
   offered as the one to absorb. The notice after the merge gives the server's counts (aliases, decisions, source answers,
   category and label assignments, and assignments left where they were).
4. **The merchant of a transaction.** A "Merchant" disclosure on a bank transaction (shown in the payment match card) reads
   `GET /transactions/{id}/merchant` only when opened. A name is shown only when the API says it may be (`fallback` false):
   the person confirmed or corrected it, or a source identified it with high confidence. Otherwise the neutral presentation
   says why (uncertain, ambiguous, none, unavailable, not looked up, rejected); bank text is never put in the name's place.
   A low-confidence suggestion is listed as a suggestion that is not used until confirmed. Confidence is an integer per
   mille written as a percentage without floating point. The person can confirm, correct (by name, or by choosing an active
   merchant record), reject, and take their decision back; an inherited decision is not offered for clearing and the
   transaction that holds it is named (ADR-0051 point 5).
5. **Attribution.** The attribution of a source (`GET /merchants/sources`) is shown wherever an identity from that source is
   shown (reliable or confirmed); a corrected name is the person's own and carries none (ADR-0054 point 5).
6. **No logo is ever loaded.** `logo_ref` is opaque, the page's content policy allows images from the page's own origin and
   inline data only, and fetching a third party's image would tell it which merchant a transaction belongs to. The neutral
   icon is always used. Cost: no logos until a decision on a logo proxy or cache (not built; ADR-0054 point 4 says Ledger
   never downloads a logo).
7. **Go client.** `pkg/ledgerclient` gains methods for every merchant operation (records, aliases, merge, a transaction's
   merchant, its decisions, the sources); the thirteen rows are gone from the client's `notImplemented` table. CLI commands
   and MCP tools for merchants are not part of this task: they belong with their slice of P-MCP2.

## Limits

- The merchant list filters by name and alias in the browser, over the records loaded so far (the API filters only by
  archived). It loads every page, so a very large merchant list is slow to filter.
- The Merchant disclosure is on the payment match card only; other places that show a bank transaction do not have it yet.
- Merchant assignment to categories and labels (task O5) is a separate task and not part of these screens.
- Tests are offline: the browser tests stub the API, and nothing here contacts a bank, a model or a logo host.
