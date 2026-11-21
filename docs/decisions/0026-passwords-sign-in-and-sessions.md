# ADR-0026: Passwords, sign-in, sessions, CSRF and recent authentication

Status: Accepted (2026-11-21); the default values in point 9 are accepted (operator, 2026-11-21, D22).
Amended (2026-11-21) by decision D23-1b: known browsers are exempt from the per-username lockout (see the amendment at
the end).

## Context

Task A2. The requirements ask for Argon2id password hashing, a minimum length in bytes without composition rules, the
same response for an unknown account and a wrong password, sign-in rate limits per account and per address,
server-side sessions in a `Secure`, `HttpOnly`, `SameSite` cookie with configurable idle and absolute timeouts,
listing and ending sessions (effective on the next request), ending other sessions on a password change, CSRF
protection for every state-changing request while keeping bearer-token workflows, and recent re-authentication for
credential changes, token creation and user management. Sign-in success and failure are audited (ADR-0025). The
operator has not decided timeouts, limits or the minimum length (decisions.md has no entry for them; D17 covers
outbound timeouts only).

## Decision

1. **Hashing.** Argon2id from `golang.org/x/crypto/argon2` with the second recommended option of RFC 9106 section 4:
   t=3, p=4, m=64 MiB, a 128-bit random salt and a 256-bit tag, stored as a PHC string
   (`$argon2id$v=19$m=65536,t=3,p=4$salt$tag`) in `user_passwords` (migration `0010_auth_sessions`). The
   parameters travel with each hash: a hash made with other parameters still verifies and is replaced at the next
   successful sign-in. Parameters read from a stored hash are bounded (at most 4 GiB, t at most 64) and the string must
   re-encode exactly, so a corrupted row cannot cause unbounded work. At most two hashes run at once in `ledger serve`
   (128 MiB), queued behind the request's context.
2. **Policy.** A new password must be at least `auth.password.min_length` bytes and at most 1024 bytes. Nothing else:
   no composition rules, no character classes.
3. **Uniform failure.** Sign-in looks the account up, then always verifies one hash: the account's, or a dummy hash
   made with the current parameters when the account is unknown or has no password. The dummy hash is made when the
   service is built (at `ledger serve` start), so the first unknown username after a start costs no more than any
   other. A disabled account with the right password, a wrong password, an unknown account, an account without a
   password, an invalid username, a submitted name longer than any username (it matches no account; it is not
   truncated to one) and an over-long password all return the same 401 `invalid_credentials` with the same body and
   headers. A hash with old parameters is replaced only after a successful sign-in, never for a disabled account, so
   a correct password for a disabled account costs no extra hash. The tests count hash verifications and hashes made
   instead of measuring time, so they hold on any machine. The audit entry (`auth.signin_failed`)
   distinguishes `invalid_credentials` from `account_disabled`, is anonymous when no account matched, and never holds
   the submitted username.
4. **Rate limits.** Two in-memory sliding windows count *failed* attempts: per submitted username (lower-cased, also
   for usernames that do not exist, so the limit does not reveal which accounts exist) and per client address (an IPv6
   address counts as its /64). Each attempt is **reserved** in both limiters, under the limiter's lock, before any
   hashing: it counts against the limit while it is in flight, then becomes a failure or, on success, is dropped. So
   concurrent requests cannot all pass the check before any of them has failed: at most the limit's number of
   passwords per username and per address is ever checked within a window, however many requests arrive at once,
   and at most that many `auth.signin_failed` entries are written (plus one `locked_out`). A key whose failures and
   attempts in flight reach the limit gets 429 `rate_limited` with `Retry-After` before any hashing, even with the
   right password (`Retry-After: 1` when only attempts in flight block it; those refusals are not audited). A
   successful sign-in clears its username's count, never the address's. Re-authentication attempts are reserved and
   counted in the same limits. The first refusal of each block is audited once (`locked_out`), not every refused
   attempt, so an attacker cannot grow the append-only audit log at will. Each limiter keeps at most 10,000 keys:
   when full it drops keys with nothing counted, then the stalest key that is not blocked, and only then the stalest
   blocked one; keys with an attempt in flight stay. The client address is the peer address unless the peer is in
   `http.trusted_proxies`; then it is the right-most `X-Forwarded-For` hop that is not a trusted proxy.
   **Behind a reverse proxy, the proxy must be listed in `http.trusted_proxies`.** Otherwise every client has the
   proxy's address, and the per-address limit becomes one lockout shared by everyone: 30 failures from anyone in 15
   minutes refuse every sign-in, the owner's included. `ledger serve` logs a warning, once per process, for the
   first request carrying `X-Forwarded-For` from a loopback or private peer that is not trusted.
5. **Sessions.** A session is a row in `sessions` keyed by a ULID, holding the SHA-256 hash of a 256-bit random
   token. The cookie `__Host-ledger_session` carries the token: `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`,
   no `Domain`, expiring with the absolute timeout. The `__Host-` prefix makes browsers refuse the cookie unless it
   has exactly those `Secure`, `Path` and `Domain` attributes, so a sibling subdomain or a plain-HTTP page cannot
   set or overwrite it. Every request looks its session up (no cache), so a deleted session fails on its
   next request with 401. A session ends (its row is deleted) on sign-out, when the user ends it from another session,
   when its idle or absolute timeout passes (deleted when next presented, and in bulk at each sign-in), and, for every
   session of the user except the caller's current one, when the password is set. Disabling a user stops their sessions
   on the next request (the principal is resolved from current data, ADR-0021). `last_seen_at` is written at most once
   per minute (a quarter of the idle timeout if shorter), so the idle timeout is exact to that interval. The user agent
   (256 bytes) and address at sign-in are kept to help a user recognise their sessions.
6. **CSRF.** Two independent checks for every state-changing request of a user principal: (a) net/http's
   `CrossOriginProtection` (Sec-Fetch-Site, then Origin against Host) refuses cross-origin browser requests; (b) the
   request must carry `X-CSRF-Token` equal to `HMAC-SHA256(key = session token, "ledger csrf v1")` (OWASP's
   HMAC-based token pattern), compared in constant time. The token is returned by sign-in and `GET /auth/session`
   and is never stored. The server enforces this for every user principal, not per handler: an authenticator must
   implement `httpapi.CSRFVerifier` or its users cannot make state-changing requests at all (fail closed). Bearer-token
   principals are exempt by design (a browser never attaches an `Authorization` header on its own). Sign-in itself
   applies check (a) (login CSRF). `SameSite=Strict` is a third layer, not relied on.
7. **Recent authentication.** A route declares `Access.WithRecentAuth()` (OpenAPI `x-ledger-recent-auth: true`, checked
   by the contract test). For a session principal it requires a password check (sign-in or `POST /auth/reauthenticate`)
   within `auth.session.reauth_window`, else 403 `reauthentication_required`. It applies now to `POST /users`,
   `PATCH /users/{id}`, `PUT /users/{id}/password` and `PUT /auth/password`; token creation (A4) and other credential
   changes (A3 2FA, A6 OIDC link/unlink) must use it. API tokens are exempt: they skip interactive steps by design
   (API tokens requirement) and their writes are audited as `token.write_used` (ADR-0025). A wrong re-authentication
   password is 403 `invalid_credentials` (the session stays valid).
8. **Access rule `session`.** `httpapi.SessionOnly()` is for routes that act on the caller's own sign-in state
   (sign-out, own sessions, re-authentication, own password). It is the one rule besides a write grant allowed on a
   state-changing method; any role may use it, API tokens get 403.
9. **Accepted (operator, 2026-11-21) defaults; each is configurable:**
   `auth.password.min_length` 12 bytes; `auth.session.idle_timeout` 30m; `auth.session.absolute_timeout` 12h;
   `auth.session.reauth_window` 10m; `auth.sign_in.account_max_failures` 10 and `address_max_failures` 30 per
   `auth.sign_in.window` 15m. Rationale: 12 bytes is above NIST SP 800-63B's 8-byte floor for multi-factor use and
   below its 15 for password-only use (2FA is A3 and optional); 30m/12h follow OWASP's guidance for higher-risk apps
   while allowing a working day; 10 per account keeps online guessing far below a useful rate while a typing owner
   rarely meets it.
10. **Audit.** New actions (ADR-0025's vocabulary, extended here): `auth.session_ended` (reason `signed_out` or
    `terminated`, target the session), `auth.password_changed` (by `self`, `administrator` or `host`;
    `sessions_ended`, and `device_cookies_revoked` since D23-1b), `auth.reauthenticated` and `auth.reauthentication_failed`. Sign-in success is written in the
    same transaction as the new session; failures with `Record`.
11. **Host CLI.** `ledger auth set-password [--create ROLE] <username>` reads the password from the first line of
    standard input, needs `ledger serve` stopped (instance lock), ends the user's sessions and is audited as the
    system (`by: host`). It is how the first administrator gets a password without the UI. Standard input is read
    as it is, so a terminal echoes the password; the help text says so and suggests piping it in.

## Why

- Argon2id with RFC 9106 parameters is the requirement and the current recommendation; bounding concurrency keeps the
  64 MiB cost from becoming a memory exhaustion vector.
- Counting work instead of timing makes the uniform-response guarantee testable without flaky timing tests.
- Counting failures per submitted username (not per account) avoids an enumeration side channel. **The cost is a
  deliberate lockout that can last indefinitely:** a blocked username gets 429 before any hashing, even with the right
  password, so anyone who knows the username can keep the owner locked out for as long as they like by sending 10
  failed attempts every 15 minutes (from any addresses; with the address limit of 30, a single address can keep three
  usernames locked). There is no way to sign in while that continues, and **restarting `ledger serve` is the only
  recovery**: it clears the in-memory counts, and the attacker can start again. The host CLI does not help, since it
  needs `ledger serve` stopped. This design (per-username lock, no exemption for known devices such as OWASP's
  device cookie) was kept as an accepted risk until decision D23-1b, which adds the device-cookie exemption (see the amendment at the end).
  Persistent limits were rejected for now: a restart clears an attacker's counts too, but persisting would add a
  write per failed attempt.
- Reserving attempts before hashing (rather than counting failures after it) is what makes the limits hold under
  concurrency. Counting after the hash let every request in flight pass the check first; with slow hashes and a cap
  on concurrent hashes, dozens of guesses could be queued past a limit of ten.
- An HMAC-derived CSRF token needs no storage and cannot be read cross-origin; Origin and Sec-Fetch-Site checking alone
  would rely on headers older browsers do not send. Synchroniser tokens stored per session were rejected because
  `GET /auth/session` could not return the token without storing it in clear.
- Deleting session rows (instead of marking them ended) keeps termination simple and the table small; the audit log
  keeps the history.

## Consequences

- `Secure` is always set, so a browser keeps the cookie only over HTTPS or on loopback (`http://localhost` and
  `127.0.0.1` count as secure contexts in current browsers). A non-loopback deployment must use TLS (a reverse proxy),
  and that proxy must be listed in `http.trusted_proxies` (point 4). There is deliberately no switch to drop
  `Secure`.
- Rate-limit counts and the dummy hash live in memory: a restart resets the counts. Two processes on one database
  are already unsupported (ADR-0019).
- A3 (TOTP) turns sign-in into two steps; the session should then start only after the second factor (or carry a
  "second factor pending" state), and `invalid_second_factor` failures should feed the same limits.
- A4 must try the bearer-token authenticator before the session cookie, call `WithRecentAuth` on token creation, and
  decide whether a password change revokes API tokens (it does not today).
- A6 (OIDC) sessions must go through the same `sessions` table so CSRF, timeouts and termination apply unchanged.
- The UI screens (sign-in, sessions list, re-authentication prompt, password change) are A7.
- Migration number: `0010`, after the commitment definitions (`0009_commitments`, ADR-0024); the fixture
  `0009_commitments` covers the previous version.

## Amendment: device cookies exempt known browsers from the per-username lockout (Accepted, decision D23-1b, A2c)

Decision D23-1b: a browser that has signed in successfully before is exempt from the per-username lockout; the
per-address limits stay. This answers the open decision in the second point of Why above and narrows its accepted
risk to browsers that have not signed in before. Implemented with OWASP's "device cookie" pattern (Blocking Brute Force Attacks, Slow down
online guessing attacks with device cookies).

1. **Issuing.** Every complete *password* sign-in (the password alone, or the password and then the second factor)
   sets `__Host-ledger_device`: a 256-bit random token with the session cookie's attributes (`Secure`, `HttpOnly`,
   `SameSite=Strict`, `Path=/`, no `Domain`), expiring after `auth.sign_in.device_cookie_ttl` (default
   90 days, `2160h`; accepted by the operator, 2026-11-21, D30). The table `device_cookies` (migration `0016_device_cookies`)
   keeps its SHA-256 hash, the user it is bound to, and its creation and expiry times; never the token. The 202
   password step of a two-step sign-in sets nothing. OIDC and recovery sign-ins issue no device cookie: they do not
   use the password limits.
2. **Using.** Sign-in looks the presented cookie up (whenever one is sent, before it is known whether the username
   exists, so the lookup does not reveal that). It is *valid* only when it exists, belongs to the account the
   submitted username names, and has not expired. With a valid cookie, the attempt is reserved and counted in the
   account limiter under the key `device:<cookie id>` instead of `user:<username>`, with the same limit
   (`auth.sign_in.account_max_failures` per `auth.sign_in.window`). The per-address limit applies exactly as before.
   A forged, expired, replaced, revoked or other user's cookie is ignored: the attempt counts against the username as
   if no cookie were sent. The second step of a two-step sign-in counts against the same key as its password step.
   Re-authentication (`POST /auth/reauthenticate`) applies the same rule, so a locked username cannot stop the owner
   from confirming their password in a known browser.
3. **Never a bypass.** The cookie only chooses which failure counter applies. The password is always verified, the
   second factor is always required when enabled, and the uniform-failure rules of point 3 are unchanged.
4. **Rotation.** A complete sign-in replaces the presented cookie: the old row is deleted and a new token is set, in
   one transaction. If issuing fails, the sign-in still succeeds without a new cookie and the old cookie stays valid
   (the rotation either happens whole or not at all). Signing out keeps the cookie: the browser stays known.
5. **Expiry and bounds.** A cookie expires at its `expires_at`; expired rows of a user are deleted when the user
   next gets a cookie. A user keeps at most 20 device cookies; issuing one more deletes the oldest.
6. **Revocation.** Setting a user's password (by themselves, an administrator or `ledger auth set-password`)
   deletes every device cookie of that user in the same transaction; `PUT /auth/password` also clears the calling
   browser's cookie. The `auth.password_changed` audit entry records `device_cookies_revoked`, how many were deleted.
   There is no separate list or revoke endpoint for device cookies.
7. **Audit.** `auth.signin_succeeded`, `auth.signin_failed` and `auth.reauthentication_failed` carry
   `device: known` when the attempt came from a browser with a valid device cookie (and so counted against it).

### Why

- OWASP's device cookie keeps the per-username lock against guessing from unknown browsers while letting the owner
  in from browsers that have proven themselves; it is the pattern the operator chose.
- Failures from a known browser count against that cookie (OWASP's "lockout of the device cookie"), so a stolen
  device cookie gives at most the same number of guesses per window as the username limit, and does not consume the
  username's budget.
- A stored, hashed token (rather than a stateless signed one) needs no new server key, makes rotation real (the old
  token stops working) and makes revocation on a password change a single delete.
- Re-authentication is included because it shares the username limiter: otherwise a locked username would block
  the owner's credential changes in their own session.

### Consequences and limitations

- The per-username lockout still applies in full to browsers that have never signed in (or whose cookie expired,
  was revoked by a password change, or was cleared). Someone who knows the username can still keep such a browser
  locked out; restarting `ledger serve` remains the only other recovery for them.
- The failure counts stay in memory (point 4): a restart clears device-cookie counts as well. The cookies
  themselves are in the database and survive restarts.
- A copied device cookie gives its holder the known browser's own failure budget, never a session: the password
  (and second factor) are still needed.
- The device-cookie lifetime (90 days) and the per-user cap of 20 are accepted by the operator (2026-11-21, D30).
- Migration number: `0016` on this branch (after `0015_oidc`); the payment-matches change also uses `0016`, so
  one of them is renumbered at merge. The fixture `0015_oidc` covers the previous version.
