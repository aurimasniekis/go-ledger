# ADR-0023: Enable Banking adapter

Status: Proposed (2026-11-21)

Numbering: 0021 (A1) and 0022 (FX1) are claimed by open pull requests; control assigned 0023 to B3. No migration.

## Context

Task B3 implements the first real banking provider behind the [ADR-0013](0013-banking-provider-boundary-and-bank-facts.md) port, feeding the [ADR-0020](0020-bank-import-engine.md) import engine. Inputs:

- **D10** (operator): *app mode* is an unrestricted production application (contract and KYB) and *developer mode* is a restricted production application limited to the operator's own linked accounts. Neither is the sandbox. Applications coexist, and each connection records which application and mode it uses.
- **D20**: no live integration checks in the initial build, and offline tests need no secrets.
- **D17**: retries only for idempotent reads, at most 3, and Retry-After is honoured. The engine does the retrying.
- **Research R1** and Enable Banking's public OpenAPI spec, version `1.0.0-1c03cf81` (read 2026-11-20 from the operator-approved public documentation; nothing was run against the API).

R1 found that Enable Banking's documentation uses neither "app mode" nor "developer mode". Its own terms are the environment (`SANDBOX`/`PRODUCTION`), restricted versus unrestricted production activation, and shared versus dedicated infrastructure. The mode names below are the operator's definitions mapped onto those terms, not Enable Banking vocabulary.

## Decision

1. **Package.** `internal/providers/enablebanking` implements `banking.Provider` for one configured application. All Enable Banking specifics live there: authentication, endpoints, paging, ISO 20022 code mapping and error classification. Domain and store packages do not import it (depguard and the boundary test).
2. **Authentication.** The adapter builds an RS256 JWT with the standard library: header `typ=JWT`, `alg=RS256`, `kid=<application id>`; claims `iss=enablebanking.com`, `aud=api.enablebanking.com`, `iat` and `exp`. Lifetime defaults to 1 h (at most 24 h, the documented maximum), and a token is reused until 5 minutes before it expires. The key is RSA, at least 2048 bits, in PKCS#8 or PKCS#1 PEM. There is no new dependency.
3. **Endpoints.**
   - `GET /aspsps`: institutions. `maximum_consent_validity` becomes `Institution.MaxConsentValidity`.
   - `POST /auth`: start consent. `valid_until` is capped at now + the bank's maximum minus 1 minute, because Enable Banking refuses longer and the session expires at exactly the value sent. `psu_type` is always sent (`personal` when the request names none). A `valid_until` that is not in the future after the cap is refused as `invalid_request` without a call. When the bank's maximum is unknown (`MaxConsentValidity` is 0) the value is not capped [U]: Enable Banking may refuse a too-long value, and that error is surfaced, not retried.
   - `POST /sessions`: complete consent. A redirect that carries an error is refused without a call.
   - `GET /sessions/{id}`: the session's status and accounts. A status other than `AUTHORIZED` is a reconnect.
   - `GET /accounts/{uid}/details`: account details. Results are cached in memory per session and uid, filled from the `POST /sessions` response, so a refresh does not spend one of the bank's few background fetches on details it already has.
   - `GET /accounts/{uid}/balances` and `GET /accounts/{uid}/transactions`: balances and transactions.
   - `DELETE /sessions/{id}`: end the session. A session that is already closed, expired, revoked or unknown is not an error.
   - `GET /application` backs `Provider.Verify`, which checks the kid, the environment and the active flag.
4. **Modes.** The mode comes from configuration and sets `Capabilities.Mode` and `AccountAccess` (`all` or `linked_only`). The adapter does not filter accounts itself: in developer mode Enable Banking strips unlinked accounts, and an empty list is passed through as an empty list, never an error. The documentation names no other difference between restricted and unrestricted applications, so none is encoded [U]. Whether an application is restricted cannot be checked through the API: `GET /application` does not report it [V spec].
5. **Transactions.**
   - A zero `From` sends `strategy=longest` and no dates, because Enable Banking refuses `date_to` without `date_from` [U: observed in the schema description, not stated by the specification]; `To` is then applied to the page locally. Otherwise the adapter sends `date_from` and `date_to`, plus `continuation_key`.
   - An empty page that carries a key is passed through, and the engine keeps paging.
   - `entry_reference` is the entry reference and `transaction_id` is provenance only.
6. **Mapping.**
   - **Account.** ExternalID is `identification_hash` and the handle is the per-session `uid`. The display name is `details`, else `product`: the schema's `name` is the account holder's name, which is personal data. IBAN, BBAN and other schemes go into `banking.Sensitive`. CPAN, documented as "masked or plain", is always reduced to `****` plus its last four characters. A missing or unparsable currency becomes `XXX`.
   - **Balance codes.** CLBD, OPBD, ITBD and PRCD map to booked; CLAV, OPAV, ITAV and FWAV to available; XPCD to expected; anything else to other. The provider's code is kept.
   - **Status.** BOOK maps to booked; PDNG, HOLD and SCHD to pending; CNCL and RJCT to cancelled; anything else to other.
   - **Amount.** The amount is exact and signed by `credit_debit_indicator`: DBIT is negative, and a debit amount that already carries a minus sign is accepted. A negative CRDT amount, or a missing indicator, contradicts itself. The adapter then leaves the amount invalid, so the engine drops and counts the row (`transactions_rejected`) instead of guessing a direction. A balance without a usable amount is handled the same way (`balances_rejected`). The instructed amount (`exchange_rate.instructed_amount`) takes the sign of the amount.
   - **Description and counterparty.** The description is the `remittance_information` lines joined with a newline. The counterparty is the creditor's name for a debit and the debtor's name for a credit.
7. **Error classification.** The Enable Banking error code is checked first, because a 401 has several causes; then the HTTP status.

   | Enable Banking code or HTTP status | Kind | Reason or retry |
   |---|---|---|
   | `EXPIRED_SESSION` | reconnect_required | consent_expired |
   | `REVOKED_SESSION` | reconnect_required | consent_revoked |
   | `CLOSED_SESSION`, `SESSION_DOES_NOT_EXIST`, `WRONG_SESSION_STATUS`, `ASPSP_PSU_ACTION_REQUIRED` [U: semantics not documented for account data] | reconnect_required | session_invalid |
   | `ASPSP_RATE_LIMIT_EXCEEDED` or HTTP 429 | rate_limited | RetryAfter from the header (seconds or HTTP date). Without a header, `ASPSP_RATE_LIMIT_EXCEEDED` gets 6 h, the backoff Enable Banking recommends; a bare 429 gets none, so the engine does not retry it. |
   | `ASPSP_TIMEOUT`, `ASPSP_ERROR`, 408, 5xx, network failures, timeouts | retryable | none |
   | Anything else, including `UNAUTHORIZED_ACCESS` (a key or app id problem), `WRONG_TRANSACTIONS_PERIOD` and an undecodable 2xx | permanent | none |

   A `Retry-After` over 7 days, in either form (seconds or HTTP date), is ignored, as if absent: it is not capped. "Retryable" on a non-idempotent call (`POST /auth`, `POST /sessions`, `DELETE /sessions/{id}`) is only a classification: such calls must not be fed through a retry loop (D17: retries are for idempotent reads only). B4 must respect this. Outbound calls use a 10 s connect and 30 s total timeout (D17) in the default client.

   Ledger's own cancellation is `canceled`.
8. **No leaks.** The adapter never logs. Its errors hold only the provider, the operation, the kind, the reason and a code. A response code is kept only if it looks like an error code (upper-case letters, digits and underscores), otherwise it becomes `http_<status>`. The wrapped cause is a fixed sanitized error, never a `*url.Error` (that would print the URL, which holds uids and session ids), never the response message or detail, and never a body. Response bodies are capped at 16 MiB.
9. **Configuration** (ADR-0004). The reflection-based configuration has no lists of structs, so there is **one slot per mode**: `banking.enable_banking.app.*` and `banking.enable_banking.developer.*`, each with `enabled`, `name`, `app_id`, `environment` (`production` by default, or `sandbox`), `private_key` (secret, environment only) and `private_key_file`. `base_url` is shared. `ledger serve` parses the keys at startup: a bad key stops startup with an error that names only the key path. It logs each application's name, mode, environment and account access, and registers the keys and app ids with the redactor. Nothing connects to Enable Banking at startup.

## Why

- Hand-signing RS256 avoids a JWT dependency for a single, fixed algorithm.
- Leaving a contradictory amount invalid reuses the engine's visible `transactions_rejected` path instead of adding a second rejection mechanism, and never guesses which way money moved.
- The details cache and the refusal to retry a 429 without guidance both protect the roughly four background fetches a day many banks allow (R1, section 7).
- One slot per mode delivers D10's per-connection coexistence of both modes without reworking the configuration system.

Rejected:
- A JWT library.
- Filtering accounts in developer mode inside the adapter (Enable Banking already does it, and a local filter could hide accounts it returned).
- Treating every 401 as a reconnect. Many 401s are configuration errors, and a reconnect prompt would not fix them.
- Storing raw payloads.
- A generic list-of-structs configuration.

## Consequences and limitations

- **No live verification (D20).** The fixtures in `internal/providers/enablebanking/testdata` are synthetic, written by hand from the OpenAPI schemas, not recorded. The tests run the adapter against an offline fake that checks the JWT signature and claims, keeps per-session uids, strips unlinked accounts for developer mode, binds continuation keys to the query, and injects every error class. They cover both modes, expired and revoked consent, rate limits, outages, timeouts, cancellation, error-text leaks, and a full refresh through `bankimport` and the SQLite store in which the logs are checked for identifiers and payloads. Behaviour of real banks may differ from the schemas. The B2 follow-up "revisit the lower bound 0 with real B3 fixture data" still waits for real data.
- **One application per mode.** D10 allows "one or more" applications. Two production applications of the same mode (or a sandbox and a production developer-mode application at once) need more configuration slots or a list-capable configuration: a follow-up.
- **No PSU headers.** The port carries no request context for a user who is present, so every call is a background fetch, subject to the bank's daily limit. Sending PSU headers on user-triggered refreshes needs a port change (B4/B6).
- **Unverified points [U].** `BackgroundFetchesPerDay: 4` is per bank and approximate, not an Enable Banking guarantee; The Terms of Service "Restriction of Use" section (unreadable), Enable Banking-side request quotas (none documented), differences between restricted and unrestricted applications beyond account filtering, the account-data meaning of `ASPSP_PSU_ACTION_REQUIRED`, whether `details` counts against a bank's background limit, and `transactions_extended_history` (left at Enable Banking's default, `true`, which may trigger an extra SCA for history older than 90 days).
- **Not built here:** connection endpoints, the consent redirect handler, OpenAPI, grants and audit (B4 and the auth tasks), a periodic `Verify` or readiness signal, and setup documentation (B5).
