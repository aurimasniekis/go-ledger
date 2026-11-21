# ADR-0025: Audit log

Status: Accepted (2026-11-21)

## Context

ADR-0021 introduced `store.AuditWriter` and an interim writer that logged events. Task A5 replaces it with a real log. The log must record who did what to which object, survive restarts, be readable by administrators only, and never become a place where secrets or request bodies accumulate.

## Decision

1. **Table.** `audit_log` (migration `0008_audit_log`) is append-only: triggers refuse every UPDATE and DELETE. There is no retention purge; entries are kept until a later decision (D17-style retention and an explicit purge command) says otherwise. Columns: ULID `id`, `action`, `actor_kind` (user, token, system, anonymous), `actor_user_id`, `actor_token_id`, `target_type`, `target_id`, `occurred_at` (UTC, same format as every other timestamp) and `details`. CHECK constraints tie the actor kind to the IDs present (a token actor always carries its token and its owner). Actor and target IDs are plain text rather than foreign keys: the actor may be the system or nobody, and the token table does not exist yet. Users are never deleted, so the IDs stay meaningful. The table is listed in `unversionedTables` (no optimistic-concurrency columns, because it is never edited).
2. **No free-form payload.** `details` is a JSON object of short strings, and `audit.Validate` runs before every insert: the action must be registered in `audit.Definitions`, every detail key must be one the action declares, fields with a closed set of values (sign-in method and reason, MCP outcome, correction kind) accept only those values, and values are at most 256 bytes of printable text. An unknown action or field is a refused write, which fails the change that tried it. Validation errors never repeat a value. A test pins the exact column list and the action names. No column or field holds a request body, password, token, session, bank payload or account number.
3. **Same transaction.** `store.AuditLog` implements `AuditWriter` and writes inside the caller's transaction, so a change and its entry commit or roll back together. `ledger serve` uses it for the users store and the reporting zone; `LogAudit` stays for callers without a database. `Record` writes an entry in its own transaction for events that are not part of a change (a failed sign-in, token use).
4. **Events.** Defined now, raised by the features as they land: `auth.signin_succeeded` and `auth.signin_failed` (method, reason), `two_factor.enabled`, `two_factor.disabled` and `two_factor.recovery_codes_regenerated`, `oidc.linked` and `oidc.unlinked` (provider), `user.*` including `user.role_changed` (raised since A1), `token.created`, `token.revoked`, `token.write_used`, `mcp.tool_called` (tool, outcome; actor carries the token identity), `correction.recorded` and `correction.reversed` (kind), and `settings.reporting_timezone_changed` (from, to; actor system), which is raised now by `reporting.Resolve` and replaces the earlier TODO. Constructors (`audit.SignIn`, `ForToken`, `MCPCall`) keep call sites uniform. A failed sign-in never records the attempted username, since a mistyped password often lands in that field; the actor is the matching user when one is known and anonymous otherwise, and the reason is a stage code.
5. **Token writes.** `httpapi.WithAuditRecorder` records `token.write_used` (token and owner, method, route pattern, never the URL or body) for every state-changing API request made with a token, **before** the handler runs. If the entry cannot be written the request answers 500 and nothing changes: a token write is never unaudited. It is recorded even when the handler then refuses the request, because the attempt is the security-relevant fact; the change itself carries its own entry attributed to the same token. `serve` installs it. The MCP server (P-MCP1) must call `audit.MCPCall` for each state-changing tool call, success or failure.
6. **Read API.** `GET /api/v1/audit`, grant `audit:read` (administrator-only resource, so read-only users get 403; a token needs the grant explicitly). Pagination and filters follow ADR-0011: ID order (oldest first; IDs written by one process increase strictly, even within a millisecond), filters `action` (eq, repeatable, and `[prefix]`), `actor_kind`, `actor_user_id`, `actor_token_id`, `target_type`, `target_id`, `at[gte]` and `at[lt]`. There is no write, update or delete endpoint.

## Why

- A closed vocabulary with per-action fields is the only dependable way to keep secrets out; a generic payload plus redaction would depend on every caller and every redaction rule being right.
- Fail-closed for token writes trades availability of token writes during an audit-write failure for the guarantee the plan asks for (every state-changing token use is audited).
- Oldest-first matches the other lists. Newest-first would suit a log viewer better; `at[gte]` reaches recent entries, and a reverse order can be added without breaking clients.

## Alternatives considered

- Logging only (the interim): lost on log rotation and not withdrawn on rollback.
- A JSON column per action with free-form content: rejected for the secrets risk above.
- Foreign keys to users: rejected because of system and anonymous actors and ordering with the token table.
- Recording after the handler with its outcome: rejected; a crash between the change and the entry would leave an unaudited token write.

## Consequences and open points

- Sign-in, two-factor, OIDC, token and MCP code (A2, A4, A6, P-MCP1) and finance corrections (X1) still have to raise their events; this change provides the vocabulary, writer, constructors and tests of each event through the writer and the read API. Their detail fields may be extended in the same change that raises them.
- Retention of the audit log is undecided; it grows without bound until then. Backups include it.
- `audit_log.id` order equals write order only per process; two processes on one database are already unsupported (instance lock, ADR-0019).
- Migration number: written as `0007`, renumbered to `0008` at merge because the currency-rate cache (`0007_fx_rates`, ADR-0022) landed first; `0007_fx_rates` is the fixture of the previous version.
