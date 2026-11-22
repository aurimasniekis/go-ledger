# ADR-0054: Merchant enrichment sources

Status: accepted. Builds on [ADR-0051](0051-merchant-model-and-corrections.md) (task E2). Decision D15, taken by the
operator on 2026-11-20, fixes the policy; research is in the knowledge base (merchant/logo enrichment, R8).

## Decisions

1. **Baseline is local and offline.** Two sources always run, in `internal/enrichment`:
   - `curated`: a small built-in list (`DefaultCurated`) matched on whole words of a *matching key*. The key is the
     counterparty (else the description) reduced to lowercase letters-only words, without card-payment noise, so
     "LIDL 1523 VILNIUS" and "Lidl 0042 Vilnius" match alike and "AMAZONIA" is not Amazon. An exact match is 950
     permille, a contained match 850, except that a generic single word (bolt, apple, booking, uber, google) found inside longer text
     ("Bolt Hardware Store") is only 650, a suggestion below the threshold. Two different merchants in one text give an *ambiguous* answer, never a guess.
     The list is a starting point, not a coverage claim, and no logo files ship (the interface uses its neutral icon).
   - `local_mapping`: the person's confirmed and corrected names, reused as **suggestions** for transactions with the
     same matching key. Its confidence is 700 permille, deliberately below the 800 reliability threshold
     (`merchants.DefaultThreshold`), so `Resolve` reports `uncertain` with `needs_review` and shows nothing until the
     person confirms. Nothing is ever auto-confirmed. Conflicting names for one key are ambiguous. The mapping is read
     from `merchant_reviews` joined to the transaction text at the start of each pass: no new table, no migration (so
     there is nothing to renumber). A rejection is not reused as a name, but it stops a curated identity for the same key.
   Local sources are consulted first; the remote source only for what they cannot identify. A key the person has
   already confirmed, corrected or rejected beats a curated identity for it: their suggestion (or an ambiguous
   answer, or none after a rejection) is returned, unless the curated answer names the same merchant. Otherwise the
   best local identification wins (curated 850 to 950 over a suggestion).
2. **A provider-neutral remote adapter exists and is off.** `merchants.enrichment.remote.enabled` defaults to false and
   no remote source is approved (D15: no paid enrichment). The adapter speaks one small contract, not a vendor's: `POST`
   to the configured https URL (plain http only on loopback) with `{"merchant": "...", "country": "LT"}`, answered by
   `{"status": "identified|ambiguous|none", "name", "logo_url", "confidence_permille" (integer), "candidates"}`. A
   vendor needs a thin mapping in front of it or a dedicated adapter, which needs its own decision, because each
   vendor's licence differs (research: Brandfetch's caching terms conflict, Ntropy's terms give it ownership of derived
   data). The bearer key is read from a file, sent only to the configured URL, registered with the log redactor and never
   logged. Redirects are not followed. Replies are limited to 64 KiB and validated like any answer.
3. **Field policy: what leaves the machine.** `RemoteQuery` has two fields and a new one is a privacy decision needing
   an ADR: the cleaned merchant string and the configured country code if any. The merchant string is the counterparty
   name alone: description text is never sent and is no fallback (decision C-E2-forms-2, superseding C-E2-forms). The name has every token that holds a digit removed (IBANs and account numbers, store numbers), the words iban, bic, swift, account and acct removed, control characters removed, at most 100 characters (decision C-E2-remote); it is split at white space and at the punctuation `, / . ;` before the digit tokens are dropped. Nothing is sent unless the cleaned name starts or ends, as a whole token in any case, with one of the unambiguous company forms uab, mb, sia, ltd, limited, llc, gmbh, inc, plc, corp, srl, sarl, kft and oü (the fixed list `companyFormList` in `internal/enrichment/key.go`) and holds at least one other word with a letter in it. The short ambiguous forms as, co, sa, ab, ag, sp, oy, nv and bv never count, so "Jonas Jonaitis AS" sends nothing; a bare form or a form plus punctuation ("UAB &") sends nothing; "Example Shop UAB", "UAB Example Shop" and "EXAMPLE SHOP LTD" are sent. Names of people, transfers and everything else send nothing (the bank data carries no counterparty kind, so a person's name cannot be recognised reliably; the rule errs towards sending nothing, and some real merchants are not looked up remotely, which is acceptable because the remote source is optional and off by default). Nothing is sent for a transfer either (transfer words such as sepa or transfer in the counterparty or description). A key the person has already
   confirmed, corrected or rejected is never sent. Amounts, dates, IBANs, account holders, balances and transaction IDs
   have no field to travel in. Tests pin the request body and the cleaning rules. Because the merchant string can still
   hold personal data, enabling the source is the operator's explicit act, and `GET /api/v1/merchants/sources` states what
   is sent.
4. **Caching follows the licence.** The operator states it in configuration; Ledger cannot verify it, and the default is
   the most restrictive: `store_logo_ref` false (only the name is kept, the client shows its neutral icon), `retention`
   720h (the 30 days the researched vendors allow at most). Ledger never downloads or copies a logo; a kept logo
   reference must be an https URL without credentials. Each pass first deletes the remote source's answers older than
   the retention (`DeleteAnswersBefore`), so the transaction is looked up again; the person's decisions are never
   deleted. This also runs when the remote source has been turned off (a pass runs at startup and after each refresh), so
   what it cached is still purged. `retention: 0` keeps answers until replaced, for a licence that allows it; once the
   source is off nothing replaces them.
5. **Attribution.** `attribution` (up to 200 characters) is exposed with the source in `GET /api/v1/merchants/sources`
   (grant `merchants:read`, administrator-only like the rest of the merchant routes). A client shows it wherever an
   identity whose `enrichment.source` is that source is shown (UI in task E3).
6. **A failure never blocks an import.** Enrichment runs after a refresh stored its data (`bankimport.AfterRefresh`,
   after payment matching), in a background goroutine bounded to one minute (passes never overlap and shutdown cancels a running one, starts none afterwards, and waits for it after the refreshes and the scheduler have ended, before the database closes), with
   recovered panics, and returns nothing to the refresh. Inside a
   pass a failing remote lookup is an `unavailable` answer, which E1's store never lets replace an earlier answer; it
   only stamps `last_failed_at`. A store failure for one transaction is counted and logged (no names, no bank text),
   never fatal. Pending rows are taken never-looked-up first (newest first), then rechecks by oldest attempt, so a small batch reaches
   every row. `unavailable` answers are looked up again after 24 hours, and `none` ones too, but only when a remote
   source is on or the person's confirmations or rejections changed (or after a start); `identified` and `ambiguous`
   ones are not, until their retention ends or they are replaced.
7. **Never financial.** The package imports no amounts, occurrences, matches or payment state, and the answers only feed
   E1's presentation-only merchant.

## Limits and open points

- The curated list is tiny and English-language brands only; coverage for the person's own region is built from
  confirmations. Extending the list is a code change; a user-supplied curated file is not built.
- Similarity is exact matching-key equality, not fuzzy: a reused mapping covers the same merchant text without
  numbers, not a different spelling. This is conservative on purpose (a wrong identity is worse than none).
- The reliability threshold stays a named default (800 permille); the 700 suggestion level and the 950/850 curated
  levels are policy constants in `internal/enrichment`, not measurements.
- No remote source was tested against a real service: tests use a loopback test server only. The licence settings are
  the operator's statement.
- Enrichment runs after refreshes; there is no on-demand "look up now" route and no list/batch read (E3 and follow-ups).
  A transaction is only enriched once a refresh runs after the upgrade.
- Whether any remote source is ever enabled is still the operator's decision (D15).
