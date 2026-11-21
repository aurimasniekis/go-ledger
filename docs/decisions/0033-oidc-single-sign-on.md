# ADR-0033: Optional OIDC single sign-on, password disablement and break-glass

Status: Accepted (2026-11-21) for the points decision D16 settled; the defaults in point 13 are **proposed** and wait
for an operator decision.

## Context

Task A6. The requirements ask for optional sign-in with one configured OpenID Connect provider (issuer, client ID,
secret from the environment or a file, scopes), the authorization code flow with PKCE, validation of issuer,
audience, nonce and signature, an explicit link made while signed in and keyed by the stable subject (never the email
address), provisioning off by default, a setting that disables password sign-in (linking alone never does), a sign-in
page that redirects to the provider when passwords are off, and a documented break-glass path on the host.

Decision D16 (operator, 2026-11-20) chose: no extra Ledger second factor after an OIDC sign-in by default (trust the
provider, configurable to require it); a global `auth.password.enabled=false` that sends sign-in to OIDC; a host CLI
that re-enables password sign-in or issues a one-time recovery sign-in, audited; unlinking only with recent
re-authentication and a remaining sign-in method. Decision D4 chose multi-user accounts with administrator and
read-only roles, so provisioning needs a role.

Research R13 (and its review) compared OIDC libraries and listed checks that go-oidc leaves to the caller.

## Decision

1. **Libraries.** `github.com/coreos/go-oidc/v3` v3.21.0 (Apache-2.0) for discovery, the JWKS key set and ID token
   verification, `golang.org/x/oauth2` v0.38.0 (BSD-3-Clause) for the authorization URL, PKCE (S256) and the code
   exchange, and `github.com/go-jose/go-jose/v4` v4.1.9 (Apache-2.0; at least v4.1.4 for GO-2026-4945), used by
   go-oidc and by the test provider. All pure Go. The protocol lives in `internal/oidc`; what an identity may do lives
   in `internal/domain/auth`; the routes in `internal/httpapi/oidc.go`.
2. **One provider.** `auth.oidc.*`: `enabled` (default false), `name` (shown and audited), `issuer` (exact, https;
   http only on loopback), `client_id`, `client_secret` (environment only) or `client_secret_file`, `redirect_url`
   (Ledger's external address plus `/api/v1/auth/oidc/callback`), `scopes` (must include `openid`),
   `require_2fa`, `clock_skew`, `provisioning.enabled` and `provisioning.role`. The client secret is added to the
   log redactor and never logged. Ledger is a confidential client: a secret is required.
3. **Flow.** `GET /auth/oidc/authorize` creates a pending request (256-bit state, nonce, PKCE verifier) kept in memory
   for ten minutes, sets the state in a `__Host-ledger_oidc` cookie (Secure, HttpOnly, **SameSite=Lax** because the
   callback is a top-level navigation from the provider's site, where a Strict cookie is not sent) and redirects to
   the provider. The callback needs a pending state that matches the cookie; a state works once.
4. **Validation.** go-oidc checks the signature against the provider's keys, the issuer, that the audience contains the
   client, and expiry; Ledger adds what go-oidc leaves out: the nonce (constant-time), an audience of **exactly** this
   client (OIDC Core 3.1.3.7, errata set 2, rejects untrusted extra audiences; R13 review finding 1), azp when present,
   an iat neither in the future nor before the request started, a non-empty subject of at most 255 bytes, a present ID
   token, and for re-authentication a fresh auth_time. `clock_skew` (default 1 minute) applies to all times.
   A sign-in session counts as recently authenticated only as far as the provider says: its `AuthenticatedAt` is
   `min(now, auth_time)`, and an ID token without auth_time counts as not recent (security review of PR #47, F1). A
   silent single-sign-on round trip therefore never opens the recent-authentication window; the user re-authenticates
   at the provider (`POST /auth/oidc/reauthenticate`) for sensitive actions. The auth_time travels with a second-factor
   challenge when `auth.oidc.require_2fa` is on.
   Userinfo is never called: identity comes from the ID token only. Access and refresh tokens are discarded.
5. **Provider access.** Its own HTTP client: D17 timeouts (10 s connect, 30 s total), responses capped at 1 MiB, no
   redirects followed. Discovery is fetched at the first sign-in (an unreachable provider never stops `ledger serve`
   from starting), cached for an hour and fetched again after that or after a failure; keys are fetched again when a
   token's key is unknown (rotation). A provider that lists its PKCE methods without S256 is refused.
6. **Links.** `oidc_links` holds at most one link per account and one account per identity, keyed by the exact
   (issuer, subject) pair. The email address the provider reports is kept for display only. Linking starts from a
   signed-in session with recent authentication (`POST /auth/oidc/link`), and the callback links only if that session
   is still live. Changing the configured issuer leaves existing links unusable, visibly (`GET /auth/oidc/link` shows
   them, and sign-in answers `not_linked`).
7. **Sign-in.** A linked identity of an enabled account starts an ordinary A2 session (method `oidc`), so cookies,
   CSRF, timeouts, listing and termination are unchanged. An identity linked to no account is refused
   (`not_linked`, audited as `auth.signin_failed` reason `identity_not_linked`) unless provisioning is on. Responses
   that fail validation are audited as anonymous `auth.signin_failed` (`invalid_credentials`); the failed check is
   logged, never a token.
8. **Provisioning** (off by default) creates an account with `provisioning.role` and no password, then the link
   (`user.created` and `oidc.linked` by provisioning, as the system). The username comes from `preferred_username`,
   then the email's local part (reduced to the username alphabet, with `-2` to `-9` when taken), and finally
   `oidc-` plus 16 hex characters of a hash of issuer and subject. Accounts are never matched by email address.
   Provisioning runs one at a time in the process.
9. **Second factor (D16).** By default an OIDC session skips Ledger's TOTP and is not confined by `auth.require_2fa`:
   the provider's authentication is trusted. With `auth.oidc.require_2fa` a user with TOTP gets the same five-minute
   challenge as after a password (the callback redirects to `/sign-in#second_factor=<challenge>`; the fragment never
   reaches a server or a Referer), and `auth.require_2fa` confines OIDC sessions too. `sessions.method` records how a
   session started.
10. **Password disablement (D16).** `auth.password.enabled=false` (it needs `auth.oidc.enabled`) refuses password
    sign-in and password re-authentication with 403 `password_sign_in_disabled` before any lookup, and the server
    answers the UI's sign-in page `/sign-in` with a redirect to the provider (not while it shows an `oidc_error` or
    `recovery_error`, so a failure cannot loop). Linking never changes it. `GET /auth/methods` tells the UI what to
    offer. Re-authentication with the provider (`POST /auth/oidc/reauthenticate`: `prompt=login`, `max_age=0`, a
    fresh auth_time and the linked identity) makes sensitive actions possible without passwords.
11. **Unlinking (D16).** `DELETE /auth/oidc/link` needs recent authentication and a remaining sign-in method: a
    password on the account **and** password sign-in enabled; otherwise 409. Sessions already started stay.
12. **Break-glass (D16).** `ledger auth recovery-sign-in <username>` (with `ledger serve` stopped, instance lock)
    issues a one-time sign-in link valid for `--ttl` (default 15 minutes, at most an hour), replacing any unused one; only
    its SHA-256 hash is stored (`recovery_sign_ins`). `GET /auth/recovery-sign-in?token=` changes nothing: it serves a
    confirmation page (no script, `no-store`, `Referrer-Policy: same-origin`, not framable) so a link previewer or URL scanner cannot use
    the token up (review F2); the page's button posts the token in a form body to the same path, a cross-origin browser
    request is refused, and that POST consumes it whatever the
    outcome, skips the second factor, works whether or not passwords are enabled, and starts a session with method
    `recovery` (still confined by `auth.require_2fa`). Audited: `auth.recovery_sign_in_issued` (by host) and
    `auth.signin_succeeded`/`auth.signin_failed` with method `recovery_sign_in`. Re-enabling password sign-in is a
    configuration change (`auth.password.enabled=true`, with `ledger auth set-password` when the account has no
    password). `ledger auth unlink-oidc <username>` removes a link from the host (`oidc.unlinked` by host).
    **Sign-in starts are limited per address (review F3).** `GET /auth/oidc/authorize` is anonymous and each call stores
    a pending transaction, so a flood could evict real ones. Every start counts against the caller's client address (the
    existing limiter; 20 a minute): beyond that the browser is sent to `/sign-in?oidc_error=rate_limited` with
    `Retry-After`. The limit is per address, so an attacker with many addresses is slowed, not stopped; the store stays
    bounded at 1000 either way.
13. **Defaults (accepted by the operator, 2026-11-21):** pending requests valid ten minutes, at most 1000 in memory
    (the oldest is dropped); discovery cached one hour; 1 MiB response cap; no redirects followed; clock skew 1 minute
    (0 to 5 minutes); default scopes `openid email profile`; provisioning role `read-only`; the recovery link valid 15
    minutes by default and at most an hour; linking needs recent authentication (like other credential changes); the
    host may always unlink.
14. **Schema.** Migration `0015_oidc`: `oidc_links`, `recovery_sign_ins` (both unversioned bookkeeping, never edited
    as API objects) and `sessions.method` (existing sessions become `password`). Fixture `0014_payment_state` covers
    the previous version.

## Why

- go-oidc plus x/oauth2 is small (three modules), pure Go and covers PKCE, discovery and key rotation; zitadel/oidc
  would bring a full provider framework and its own cookie state handling (R13).
- Server-side pending requests keep the PKCE verifier and nonce off the browser; binding the state to a cookie stops a
  callback from being completed in another browser (login CSRF). Keeping them in memory matches the second-factor
  challenges (ADR-0030): a restart only costs a new sign-in.
- Strict audiences and a bounded iat follow the current OIDC Core text rather than go-oidc's looser checks.
- A one-time link from the host works when the provider is down, when passwords are off and when the account never had
  a password, and needs no configuration edit; it can only be issued by whoever controls the database host, who can
  already reset passwords and second factors.
- The recovery token travels in a query string so the printed link works in any browser. Ledger's access log records
  paths only, never queries; a reverse proxy may log queries, which is why the token is single-use and short-lived.

## Consequences

- The session cookie stays SameSite=Strict. It is set on the callback's response, but the redirect that follows still
  belongs to a navigation that started on the provider's site, so browsers may not send the cookie with that first page
  load; the UI's own requests (same-site) carry it. The UI must not treat that first page load's lack of a session as
  signed out without asking `GET /auth/session`.
- OIDC sign-ins do not count towards the password sign-in limits (ADR-0026): nothing is guessed at Ledger, and the
  provider limits its own sign-ins.
- Two browser tabs signing in at once share the state cookie: the older tab's callback fails with `invalid_state` and
  that user starts again.
- An attacker who can reach `/auth/oidc/authorize` can fill the 1000 pending slots and so make a legitimate user's
  pending request drop; the user retries. There is no per-address limit on starting requests.
- The UI (A6c) handles `/sign-in` with `oidc_error`, `recovery_error` and `#second_factor=`, the link outcomes
  (`oidc=linked`, `oidc_error` on Account security), offers the provider from `GET /auth/methods`, and uses
  `POST /auth/oidc/reauthenticate` when `reauthentication_required` arrives and passwords are off. The recovery page
  uses `Referrer-Policy: same-origin` rather than `no-referrer`: a form POST from a no-referrer page sends
  `Origin: null`, which the cross-origin check would refuse in browsers without `Sec-Fetch-Site`. After a
  re-authentication round trip the user repeats the action (the interrupted request is not replayed).
- Not built: RP-initiated logout at the provider (local sign-out always works), back-channel logout, group or claim to
  role mapping, more than one provider, a per-user password disable (D16 calls it optional), an administrator route to
  unlink another user, and interoperability tests with real providers (all tests use a local fake provider).
- A provisioning run that fails between creating the account and the link (a database error) leaves an account without
  a link or password; an administrator can disable it.
