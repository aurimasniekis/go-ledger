# ADR-0029: API tokens and the bootstrap command

Status: Accepted (2026-11-21) for the format, storage, enforcement and auditing (points 1-7 and 10); points 8 and 9
are **proposed** and wait for an operator decision.

## Context

Task A4. The requirements ask for API tokens sent as `Authorization: Bearer <prefix>_<random>` with a fixed,
scanner-detectable prefix (decision D2: `ldgr_`); shown once; stored as a hash with the identifying prefix and
trailing characters, a name, an owner, grants, an optional expiry (default none) and last use; grants never above the
creator's; an explicitly created root token whose full power is explained; listing and immediate revocation; a
first-token bootstrap path without the web UI that prints the token once; and token requests that skip interactive
steps such as 2FA, with each state-changing use audited saying so. ADR-0021 defined the grant vocabulary and
`authz.TokenPrincipal` (a token's grants limited by its owner's current role); ADR-0025 the audit log and the
`token.created`, `token.revoked` and `token.write_used` actions; ADR-0026 sessions, CSRF and recent
re-authentication, and asked A4 to put the token authenticator first, require recent re-authentication for token
creation and decide whether a password change revokes tokens.

## Decision

1. **Format.** `ldgr_` followed by 43 characters drawn uniformly (rejection sampling over `crypto/rand`) from
   `[0-9A-Za-z]`: about 256 bits, no characters that break double-click selection or need URL escaping. Anything
   else is not a token (`tokens.WellFormed`), checked before any lookup.
2. **Storage** (migration `0012_api_tokens`, table `api_tokens`). Only the SHA-256 of the token is stored (a fast
   hash is enough for 256 random bits, as for session tokens), with `display_prefix` (`ldgr_` and the first four
   random characters) and `last_chars` (the last four), shown together as a hint (`ldgr_AbCd…wXyZ`). Also the name
   (1-100 bytes), owner, root flag, grants (canonical, comma-separated), `created_at`, `expires_at` (null = never,
   the default), `last_used_at` (written at most once a minute) and `revoked_at`. Revocation keeps the row, so audit
   entries and lists still point at it. Rows are never edited through the API, so the table has no `version`
   column (`unversionedTables`). A stored grant that is not in the vocabulary is dropped when read, which can only
   narrow a token.
3. **Authentication.** `httpapi.TokenAuth` runs **before** the session cookie. A request without an
   `Authorization` header falls through to the cookie; a request with one is judged by it alone: anything but a single
   `Bearer` header holding a valid, unrevoked, unexpired token of an enabled owner is 401, even when a valid session
   cookie is also present. Every failure is the same 401. Every request reads the token and its owner, so revocation,
   expiry, disabling the owner and role changes apply to the next request. Bearer requests are CSRF-exempt by principal
   kind (ADR-0026) and exempt from recent re-authentication.
4. **Grants.** A token's effective grants are its stored grants intersected with its owner's current role
   (`authz.TokenPrincipal`); a **root** token's are the owner's role's grants, so it gains grants added to the
   vocabulary later. On creation every requested grant must be one the creator holds (`Grants.Allows`, write implies
   read), and only a creator holding every grant of the vocabulary (an administrator) may ask for a root token;
   otherwise 403 `forbidden` listing what is missing. Unknown grants, no grants without `root`, grants with `root`,
   and an expiry not in the future are 400. A root token is only created when asked for explicitly
   (`"root": true` or `--root`), and every interface shows `tokens.RootWarning` with it.
5. **Routes.** A user's own tokens: `GET/POST /auth/tokens`, `GET/DELETE /auth/tokens/{id}`, all `SessionOnly`
   (any role, within their own grants); creation also needs recent re-authentication. Everyone's tokens:
   `GET /tokens` (filter `owner_id`) and `GET /tokens/{id}` need `tokens:read`, `DELETE /tokens/{id}` needs
   `tokens:write`. `tokens` is a new administrative resource (read, write). Revoking an already revoked token is 204 and
   records nothing. The creation response (201, `Cache-Control: no-store`) is the only place the token appears:
   `{"secret", "token", "warning"}`.
6. **Bootstrap.** `ledger token create [--name N] (--grant G... | --root) [--expires T] [--create ROLE] <username>`
   works directly on the database, under the instance lock like `ledger auth set-password` (stop `serve` first). The
   host acts for the owner: the token is bounded by the owner's role exactly as if they had created it. `--create`
   makes the owner on a fresh database (no password until `auth set-password`); the grants are checked against the new
   role first, so a refused token leaves no user behind. Standard output receives the token and nothing else (one
   line, so it can be piped into a secret manager); the summary, the root warning and the "shown once" notice go to
   standard error. The audit actor is `system`.
7. **Auditing.** `token.created` (details `grants`, `root`, `expires_at`; never the token), `token.revoked` (the
   actor is the owner, an administrator or a token) and `token.write_used` for every state-changing token request,
   written before the handler runs, now with `interactive_steps: skipped` to record that two-factor and
   re-authentication were not asked for. The recorder is **required**: `httpapi.New` panics when `TokenAuth` is
   installed without `WithAuditRecorder`, and a token's state-changing request on a server without a recorder is
   refused with 500 (fail closed).
8. **Proposed: a password change does not revoke API tokens.** Setting a password ends the user's other sessions
   (ADR-0026) but leaves their tokens active; disabling the user stops them at once. Revoking is explicit
   (`DELETE /auth/tokens/{id}`, or an administrator's `DELETE /tokens/{id}`).
9. **Proposed: a token cannot create tokens.** Token creation is a session route; tokens may list and revoke tokens
   (`tokens:read`/`tokens:write`) but never mint one, and so never chain grants or root.
10. **Logs.** Nothing logs the `Authorization` header, the token or its random part; a sentinel test creates, uses,
    refuses, revokes and reuses tokens and then searches the log, the audit log, every response but the creation's and
    every database row for each token and its random part.

## Why

- A fixed prefix with a long random body is what secret scanners match, and base62 keeps the token one "word". A
  checksum suffix (GitHub-style) was not added: no scanner contract asks for one yet, and it can be appended later as
  a new format without breaking stored hashes.
- SHA-256 rather than Argon2id: the token is not guessable, so slow hashing only costs every request; a stolen
  database still yields no usable token.
- Judging a request by its `Authorization` header alone avoids a confusing fall-back where a revoked token silently
  works as the browser's session.
- Own-token routes under `/auth` as `SessionOnly` let every role make tokens within its own grants (D4 keeps
  read-only users), keep the "credentials of the caller" pattern of ADR-0026, and make recent re-authentication
  meaningful. Administering everyone's tokens is a separate grant so it can be given to a token (for example, an
  incident script that revokes tokens) without the power to create them.
- Point 8: tokens are for unattended automation; revoking them on every password rotation would break it silently,
  and the token list shows every active token with its last use. **Rejected alternative, kept for the operator:**
  revoke all of a user's tokens on a password change (safer after a password compromise, because a stolen password
  plus recent re-authentication can mint a token that outlives the password change). A middle ground would be to
  report active tokens in the password-change response.
- Point 9: chaining makes "grants <= creator" depend on the creating token's lifetime and on future vocabulary (a
  non-root token holding every current grant must not mint a root token). It also leaves no path for a Terraform
  `tokens` resource (T4, decision D18 still open); if D18 wants one, a token-created token would need the creating
  token's grants as its limit and never root.

## Consequences

- Demoting a token's owner narrows it at once. With the existing `Grants.Intersect` a write grant the new role does
  not hold is dropped entirely, not reduced to read: a `commitments:write` token of a demoted administrator loses
  commitments access. That is ADR-0021's rule, unchanged here.
- `ledger token create` needs `serve` stopped (instance lock). Day-to-day tokens are created through the API or
  the UI; listing and revoking from the host, and management through the public API with a token, belong to M2.
- A3 (TOTP) must keep exempting token principals from the second factor, and A6 (OIDC) adds no token path.
- The UI (create with show-once dialog, grants picker, root warning; list; revoke) is A7. User documentation for
  bootstrap, rotation, least privilege and the power of root and long-lived tokens is A8.
- Migration number: `0012`, after the commitment occurrences (`0011_commitment_occurrences`, ADR-0027); the fixture
  `0011_commitment_occurrences` covers the previous version.

## Amendment: re-enabling a user (Proposed, pending the operator)

Tokens are re-checked on every request against their owner's current state. Disabling a user therefore stops their
tokens at once, but nothing is revoked: **re-enabling the user revives every token that was not revoked and has not
expired.** Disabling is a suspension, not a revocation. Anyone who must also end a token for good revokes it (or
revokes all of the user's tokens) before or after disabling. Ledger does not revoke tokens on disable because that
would make the suspension irreversible and silently break unattended automation on a temporary disable. This note is
Proposed until the operator confirms it, together with points 8 and 9; the user documentation (A8) must repeat it.
