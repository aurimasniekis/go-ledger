# ADR-0021: Users, roles and the grant vocabulary

Status: Accepted (2026-11-20)

## Context

Decision D4 chose full multi-user support with administrator and read-only roles, with the UI optimised for one owner. The requirements ask for distinct permissions for at least those two roles, API tokens whose grants never exceed their creator's, an MCP server that enforces the same grants as the HTTP API, and audited role changes. Sign-in sessions (A2), API tokens (A4), the audit log (A5) and OIDC provisioning (A6) are separate tasks; this one (A1) has to give them a model to build on without deciding their questions.

## Decision

1. **Grant vocabulary** (`internal/domain/authz`). A grant is `resource:action` (for example `users:write`). Actions are `read` and `write`; write implies read of the same resource. Each resource is declared once with the actions it has and whether it is administrative (`AdminOnly`). There are no wildcards: a grant must be in the vocabulary to parse. The spelling is public (tokens, Terraform, MCP) and a name keeps its meaning. Resources arrive with their features; today they are `users` (read, write) and `audit` (read; the A5 read API).
2. **Roles** are derived from the vocabulary, not stored as grant lists: `administrator` holds every grant; `read-only` holds `read` on every resource that is not administrative, and never `write`. A user has exactly one role. A new role needs code and a migration (the `users.role` column is checked).
3. **Principal.** Every authenticated request carries an `authz.Principal` (kind user or token, user ID, token ID, role, effective grants). A token's effective grants are its stored grants intersected with its owner's current role (`authz.TokenPrincipal`), so demoting or disabling the owner narrows or stops the token on the next request. `Grants.Covers` is the no-escalation check for token creation.
4. **Every API route declares its access** when registered: `httpapi.HandleAPI(pattern, access, handler)` with `Requires(grant)`, `Authenticated()` or `Public()`. `Handle` refuses `/api` paths, so there is no default and no route can be added unguarded. A state-changing method guarded by a read grant panics at registration. OpenAPI states the same access as `x-ledger-access` on each operation, and the contract test fails when the two differ.
5. **Authentication is pluggable**: `httpapi.Authenticator` returns the principal, `ErrNoCredentials` (try the next one) or `ErrInvalidCredentials` (401). Authenticators must resolve the principal from current data on every request (`users.Store.Principal` refuses disabled users). Without any authenticator every non-public route answers 401: secure by default until A2/A4 install theirs.
6. **MCP** tools (P-MCP1) declare the grant of the API operation they map to and check it with `Principal.Allows`; they reuse the same vocabulary, not a parallel one.
7. **Users** (`internal/domain/users`, `internal/store/users`, migration `0005_users`): username (lower case, unique), optional display name, role, disabled flag with `disabled_at`, version (M1). Users are **disabled, never deleted**, so audit entries and token ownership keep a real target. The last enabled administrator cannot be demoted or disabled (checked inside the write transaction). The Management API offers list, create, get and merge-patch (`/api/v1/users`, `users:read` / `users:write`), plus `GET /me` and `GET /grants` for any authenticated principal.
8. **Audit port.** Writes record `audit.Event`s through `store.AuditWriter` inside their own transaction, so a change and its audit entry commit together. Events carry identifiers and a fixed set of non-secret details only. Until A5 adds the audit table, `store.LogAudit` writes each event as a structured `audit` log line.

## Why

- One vocabulary checked in one place keeps HTTP, tokens and MCP consistent: the requirement is "identical validation and grants".
- Declared-per-route access with no default, plus the hand-written permission matrix test in `cmd/ledger`, makes a forgotten check a failing build rather than an open endpoint.
- Deriving roles from the vocabulary means a new resource is automatically readable by read-only users (when not administrative) and never writable by them.
- Intersecting token grants with the owner's role at request time makes demotion and disabling immediate without rewriting tokens.
- Disable instead of delete preserves the audit trail and avoids dangling ownership.

## Alternatives considered

- **Free-form permission strings or wildcards** (`*:read`): rejected for now; wildcards would silently widen tokens when resources are added. A4 can add an explicit root token (all grants) without them.
- **Stored per-role grant lists**: rejected; nothing needs editable roles, and it would let a read-only role be given writes by mistake.
- **Authorization as optional middleware** (`WithAPIMiddleware`): rejected; a route could be registered without it.
- **Hard delete of users**: rejected; see above. If retention later requires erasure, it needs its own decision.

## Consequences and open points

- The user API cannot be used in a running server until A2 (sessions) or A4 (tokens) installs an authenticator; `ledger serve` logs that. The first administrator will come from the A4 bootstrap (or an A2 CLI); not part of this change.
- Audit events go to the log, not a table, until A5; a line logged for a transaction that then fails to commit is not withdrawn. A5 implements `store.AuditWriter` and `serve` switches to it.
- Recent re-authentication for user management (A2) is not enforced yet.
- OIDC provisioning (A6, D16) is not presupposed: nothing here creates users from an identity provider or chooses a default role.
- User changes publish no outbox events; add them if the event catalogue (docs/events.md) wants them.
- Terraform coverage of users (D18) is open; the API has no DELETE, so a Terraform resource would disable on destroy or be omitted.
- Migration numbering: this change adds `0005_users` and the fixture `0004_bank_facts`. Other branches adding migrations at the same time take the next free number at merge time.
