# ADR-0029: API tokens and the bootstrap command

Status: Accepted (2026-11-21). Points 1-7 and 10 at first; point 8 accepted by the operator (decision D24); point 9
replaced by the operator's decision D25-1 (only a root token of the root account may create tokens), accepted; the
re-enabling amendment accepted (decision D25-2). Point 9 amended by A4e with containment rules (control defaults
that follow D25; see the amendment "Token-created tokens are contained").

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
   (any role, within their own grants); creation also needs recent re-authentication, and a root token of the root
   account may create tokens too (point 9). Everyone's tokens:
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
8. **Accepted (D24): a password change does not revoke API tokens.** Setting a password ends the user's other
   sessions (ADR-0026) but leaves their tokens active; disabling the user stops them at once. Revoking is explicit
   (`DELETE /auth/tokens/{id}`, or an administrator's `DELETE /tokens/{id}`).
9. **Accepted (D25-1): only a root token of the root account may create tokens.** Sessions keep their rule (any role,
   within its grants, with recent re-authentication). Among tokens, only a **root token owned by the root account**
   may call `POST /auth/tokens`; every other token is 403 `forbidden`. This lets the Terraform provider manage
   tokens with that one root token.
   - **The root account** is the first user created on the database: the earliest `created_at`, ties broken by
     insertion order (`users.Store.RootAccountID`). That is exactly the account the bootstrap flow makes on a fresh
     database (`ledger token create --create ROLE <username>`, point 6, or `ledger auth set-password --create`);
     on a database whose first user came from elsewhere (OIDC provisioning, ADR-0033), that user is the root
     account. Users are never deleted (ADR-0021), so it never changes and no flag is stored (no migration).
   - **Disabled root account.** It stays the root account; nothing passes to another user. Its tokens fail
     authentication like any disabled owner's (401), so while it is disabled no token can create tokens. Sessions of
     other users still can, and so can the host (`ledger token create`, for any user). Re-enabling it revives its
     unrevoked, unexpired tokens (amendment below), including this power. **Demoted**, its root token holds only the
     read-only role's grants (point 4), so it can create only tokens within them and no root token. A revoked or
     expired root token stops at once.
   - **Limits.** The new token is owned by the root account (the route creates the caller's own tokens) and bounded
     by the creating token's effective grants, which for a root token are its owner's current role. An administrator
     root account's root token may therefore create a root token as well (each with `tokens.RootWarning`).
   - **Enforcement.** `tokens.Service.Authenticate` marks the principal (`authz.Principal.CanCreateTokens`) only
     for a root token whose owner is the root account; the route's access rule
     (`httpapi.SessionOrTokenCreator`, `x-ledger-access: session-or-root-token`) refuses every other token
     before the handler runs or anything is audited; `tokens.Service.Create` checks again from current data (the
     stored token is active, root and owned by the root account), so a forged flag gains nothing. Recent
     re-authentication does not apply to the token (point 3).
   - **Audit.** `token.created` names the creating token as the actor (kind `token`, `actor_token_id`) and in
     the detail `parent_token_id`; the request is also `token.write_used` (point 7).
   - **API description and client.** `POST /auth/tokens` inherits the document's security (bearer or session
     cookie); session-only operations now declare the session cookie alone. The Go client has
     `CreateMyToken` (`pkg/ledgerclient`). Listing and revoking own tokens under `/auth/tokens` stay
     session-only; tokens use `/tokens` with `tokens:read`/`tokens:write`.
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
- Point 9 (as first proposed, a token could never create tokens) left no path for a Terraform `tokens` resource. The
  operator chose one narrow exception (D25-1). Restricting it to the root account's root tokens keeps chaining out of
  reach of any narrower token: "grants <= creator" is always the root account's current role, which a root token
  already follows, so the exception adds no grant that token did not hold. Defining the root account as the first
  user follows the bootstrap flow without a stored flag; refusing to move it to another user when it is disabled
  avoids a silent transfer of power, at the price that a disabled root account suspends token creation by tokens
  until it is re-enabled (sessions and the host still work). The rejected alternatives were a stored "root"
  flag on a user (a migration and a new thing to administer) and any administrator's root token (wider than the
  decision).

## Consequences

- Demoting a token's owner narrows it at once. With the existing `Grants.Intersect` a write grant the new role does
  not hold is dropped entirely, not reduced to read: a `commitments:write` token of a demoted administrator loses
  commitments access. That is ADR-0021's rule, unchanged here.
- `ledger token create` needs `serve` stopped (instance lock). Day-to-day tokens are created through the API or
  the UI; listing and revoking from the host, and management through the public API with a token, belong to M2.
- A3 (TOTP) must keep exempting token principals from the second factor, and A6 (OIDC) adds no token path.
- Every token created by a token traces back through `parent_token_id` to a root token of the root account, and
  from there (`token.created` with actor `system` or a user) to how that one was made.
- The UI (create with show-once dialog, grants picker, root warning; list; revoke) is A7. User documentation for
  bootstrap, rotation, least privilege and the power of root and long-lived tokens is A8.
- Migration number: `0012`, after the commitment occurrences (`0011_commitment_occurrences`, ADR-0027); the fixture
  `0011_commitment_occurrences` covers the previous version.

## Amendment: re-enabling a user (Accepted, decision D25-2)

Tokens are re-checked on every request against their owner's current state. Disabling a user therefore stops their
tokens at once, but nothing is revoked: **re-enabling the user revives every token that was not revoked and has not
expired.** Disabling is a suspension, not a revocation. Anyone who must also end a token for good revokes it (or
revokes all of the user's tokens) before or after disabling. Ledger does not revoke tokens on disable because that
would make the suspension irreversible and silently break unattended automation on a temporary disable. The operator
accepted this note (D25-2); the user documentation (A8) must repeat it.

## Amendment: token-created tokens are contained (A4e; control defaults that follow D25)

The staff review of A4 showed that D25-1 let a leaked root token mint a never-expiring root child that outlived both
the parent's expiry and the parent's revocation. These rules close that. They are control defaults that follow D25-1,
not new operator decisions; the operator can replace them.

1. **The parent is stored.** A token created through the API by a root token of the root account stores that token's ID
   in `api_tokens.parent_token_id` (migration `0018_token_parents`; fixture `0017_payment_matches`). Tokens made by a
   user session or by the host have none, and so do tokens that existed before the migration (their creator was only in
   the audit log; no link is invented). The `token.created` audit entry still names `parent_token_id`.
2. **A child never outlives its parent.** If the parent has an expiry, the child's `expires_at` is the requested one or
   the parent's, whichever is earlier (a later or missing request is capped, silently, and the response shows the
   resulting expiry). If the parent never expires, the child's expiry is as requested (none by default).
3. **Revoking a token revokes its descendants** (children, their children, and so on) in the same transaction, each
   audited as `token.revoked` with `cascaded_from` naming the revoked token. Already-revoked descendants are left as
   they are. Creation re-checks the parent in the write transaction, so a parent revoked while a child is being created
   leaves no child.
4. Grants are unchanged: a child is bounded by the root account's current role (no escalation), and every request
   still re-checks the owner. Disabling the root account stops its descendants through their owner, as before.

### Limits of the root-account determination

The root account is the user with the earliest `created_at` (ties by insertion order). `created_at` is the service
clock at insert. If the clock steps backwards at bootstrap, before a second user is created, the first user could carry
a later `created_at` than the second and the root account would be the second user. The window is narrow (bootstrap,
with at most one user) and every user is created by the bootstrap commands, the administrator API or OIDC
provisioning, none of which run the clock backwards on purpose. After a restore the root account follows the restored
data. Ledger does not pin the root account in a settings row (that would need a migration and a way to change it); an
operator who suspects a clock step at bootstrap should check the first users' `created_at` before relying on D25-1.

## Amendment: only the root token escalates accounts (A4g; operator decision D32)

A token holding `users:write` could create an administrator and set its password, or reset any password, which made
that grant a full account takeover. Decision D32 narrows it, using the same principal as D25-1 (a root token owned by
the root account, `authz.Principal.CanCreateTokens`, computed on every request from current data).

1. With an API token, these need the root token of the root account; any other token gets 403 with the stable code
   `root_token_required`, before anything is written or audited:
   - `POST /users` with `role: administrator`;
   - `PATCH /users/{id}` that makes a non-administrator an administrator;
   - `PATCH /users/{id}` that re-enables a disabled administrator (this also revives their unrevoked tokens, D25-2, so it
     is the same escalation); enabling a disabled user together with a promotion is covered by the promotion rule;
   - `PUT /users/{id}/password`, for any user including the token owner.
2. Still open to a `users:write` token: creating non-administrators, profile edits, demoting and disabling users,
   re-enabling non-administrators. These cannot raise anyone to administrator.
3. Sessions are unchanged: an administrator's session needs recent sign-in (`reauthentication_required`).
4. Routes checked for the same escalation: second-factor routes (enrol, confirm, recovery codes, disable) and OIDC
   link/unlink act only on the caller's own account and are session-only, so no token can reach them; there is no
   administrator reset of another user's second factor. Token creation is D25-1; revoking a token cannot escalate.
   OIDC provisioning and the bootstrap/host commands run as the host, not as a token.
5. Concurrency: the PATCH check reads the target just before the write, outside the write transaction, so it is pinned
   by the version. When the target is no longer at the version the caller sent, the check answers `version_conflict`
   (409) instead of judging; the store's in-transaction check of the same version then guarantees the write applies to
   the state the check judged. A concurrent demotion, disabling or promotion therefore cannot be raced into a
   re-promotion or re-enable by a non-root token.

## Amendment: the root path is protected from other tokens (A4i; operator decision D34)

Only a root token of the root account (the D25 and D32 meaning of "the root token"), or a session, may demote or disable the root
(first) account or revoke a root token of the root account; one root token of the root account may revoke another. Every other
token, including one holding `users:write` or `tokens:write` and another administrator's root token,
gets 403 `root_token_required` before anything is written:

- `PATCH /users/{root-id}` that demotes the root account, disables it, or renames it (a username change; decision D35, which
  extends this amendment; a no-op such as `disabled: false` on an enabled administrator, the same username, or a profile edit,
  stays open). Like D32 the check is pinned to the version (409 `version_conflict` when the
  caller's version is stale).
- `DELETE /tokens/{root-token-id}` for a root token of the root account, which also ends its children. Revoking any other token is unchanged.
