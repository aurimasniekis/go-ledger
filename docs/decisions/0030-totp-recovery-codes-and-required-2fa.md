# ADR-0030: TOTP, recovery codes and required two-factor authentication

Status: Accepted (2026-11-21); the default values in point 10 are **proposed** and wait for an operator decision.
Point 13 (a current second factor for changes to 2FA) added and accepted by the operator's decision D26-1a.

## Context

Task A3. The requirements ask for TOTP and recovery codes shown once at enrolment, a configuration that requires 2FA
for everyone, users without required 2FA kept from protected pages, and an audit entry for every 2FA change. ADR-0026
(sessions, sign-in limits, CSRF, recent authentication) asked that the session start only after the second factor and
that second-factor failures feed the same limits. The configuration already requires a key-encryption key
(`kek.value` or `kek.file`, ADR-0004) that nothing consumed yet. API tokens skip interactive steps such as 2FA by
requirement. There is no UI here (A7).

## Decision

1. **TOTP** follows RFC 6238 with the parameters every authenticator app supports: HMAC-SHA1, 6 digits, 30-second
   steps, a random 160-bit secret. A code is accepted in its own step and one step either side. Every candidate step is
   compared in constant time.
2. **Replay protection.** `user_totp.last_step` holds the last accepted step. A code is accepted only for a step later
   than it, and the step is recorded with one conditional `UPDATE ... WHERE last_step < ?`, so two requests with the same
   code cannot both pass. Confirming an enrolment records its step too, so the confirmation code cannot sign in.
3. **Secrets at rest.** The TOTP secret is sealed with AES-256-GCM under a key derived from the KEK with HKDF-SHA256
   (info `ledger totp secret v1`), with the user ID as additional data, and stored as `v1.` plus base64url of nonce
   and ciphertext. A row copied to another user does not open. The secret is returned once, in the enrolment response
   (base32 and an `otpauth://` URI for a QR code), and never logged or audited. A secret that does not open (the KEK
   changed) fails the sign-in with 500 and a log entry naming the likely cause; it is never treated as a pass.
4. **Recovery codes.** Ten codes of 16 Crockford base32 characters (80 random bits), shown as `XXXX-XXXX-XXXX-XXXX`,
   are issued when TOTP is enabled and when the user regenerates them; each issue replaces every earlier code. They are
   returned once and stored as SHA-256 over the user ID and the normalised code. Using one deletes it. Input is
   case-insensitive and ignores dashes and spaces; `O`, `I`, `L` read as `0`, `1`, `1`. Recovery codes do not
   depend on the KEK.
5. **Enrolment** (`/auth/two-factor`, session routes, recent authentication): `POST /totp` stores a sealed pending
   secret (valid ten minutes) and returns it; `POST /totp/confirm` checks a code against it, makes it the active secret,
   issues recovery codes and ends every other session of the user (as a password change does), in one transaction. An
   enabled secret keeps working until a new one is confirmed, so enrolling again replaces an authenticator.
   `DELETE` disables TOTP and deletes the recovery codes; `POST /recovery-codes` regenerates them; `GET` returns the
   status (never a secret). While TOTP is enabled, confirming a replacement, `DELETE` and `POST /recovery-codes`
   also need the current second factor (point 13).
6. **Two-step sign-in.** For a user with TOTP enabled, a right password answers 202 with a challenge (a random token,
   kept in memory as a hash for five minutes) and no cookie. `POST /auth/sign-in/second-factor` takes the challenge and
   exactly one of a TOTP code or a recovery code, then starts the session exactly as a password sign-in does. A wrong,
   malformed or reused code is 401 `invalid_second_factor`; an unknown, expired or used-up challenge is 401
   `unauthenticated` (sign in again). A restart discards challenges.
7. **Limits.** A second-factor attempt is reserved in the same per-username and per-address limiters as a password
   (ADR-0026), and a failure counts as one. A right password for a user with TOTP **does not clear** the username's
   failures; only a complete sign-in does, so a known password cannot reset the limit on code guesses. A challenge also
   takes at most five wrong codes. With the default limit of 10 failures per 15 minutes, an attacker who knows the
   password gets at most 10 guesses at a one-in-a-million code (three valid codes in the window) per 15 minutes.
8. **Required 2FA.** `auth.require_2fa` (default false). A session whose user has no TOTP carries
   `Principal.EnrollmentRequired`; the server refuses it on every API route with 403
   `two_factor_enrollment_required`, reads included, except routes marked `Access.DuringEnrollment()`: `GET
   /auth/session`, `POST /auth/sign-out`, `POST /auth/reauthenticate` and `GET`, `POST /totp` and `POST
   /totp/confirm` under `/auth/two-factor`. Only session routes may be marked (checked at registration); OpenAPI
   records the mark as `x-ledger-during-enrollment: true` and the contract test compares it with the routes. Password
   sign-in still starts a session, since enrolment needs one; confirming lifts the confinement on the next request. The
   current session's `two_factor.enrollment_required` tells the UI to show enrolment. While it is on, users cannot
   disable their own TOTP (409); they replace it by enrolling again. API tokens are never confined (they skip
   interactive steps by requirement).
9. **Host recovery.** `ledger auth reset-2fa <username>` (with `ledger serve` stopped, instance lock) removes the
   user's TOTP and recovery codes, audited as the system (`by: host`). It is the path for a lost authenticator without
   recovery codes and for a changed KEK. With required 2FA, the user is confined to enrolment at the next sign-in.
10. **Proposed defaults (not decided by the operator):** one step of skew; ten recovery codes; enrolment valid ten
    minutes; challenge valid five minutes and five wrong codes. Rationale: one step either side is RFC 6238's
    recommendation for network delay; ten codes is the common practice; the other values bound memory and guessing
    without hurting a user who is typing. Only `auth.require_2fa` is configurable; the others are constants.
11. **Audit.** `two_factor.enabled` (method, replaced, sessions_ended), `two_factor.disabled` (method, by self or
    host), `two_factor.recovery_codes_regenerated` (count), each with `verified_with` (`totp` or `recovery_code`)
    when point 13 asked for the current factor, and sign-in events with method `totp` or
    `recovery_code` (a recovery-code success records how many remain; a failure has reason `invalid_second_factor`).
    Starting an enrolment that is not confirmed changes no second factor and is not audited.
12. **Schema.** Migration `0013_totp`: `user_totp` (active and pending sealed secrets, `last_step`) and
    `user_recovery_codes` (hashes). Both are unversioned bookkeeping (never API objects). Fixture
    `0011_commitment_occurrences` covers the previous version.
13. **A current second factor for changes to 2FA (decision D26-1a).** When TOTP is enabled, replacing the
    authenticator (`POST /totp/confirm` while one is enabled), disabling it (`DELETE`) and regenerating the recovery
    codes (`POST /recovery-codes`) need, besides recent re-authentication, exactly one of `current_code` (a code
    from the enabled authenticator) or `current_recovery_code` (an unused recovery code) in the JSON body
    (`CurrentSecondFactor` in OpenAPI; `auth.CurrentFactor` in the service). It applies to every session,
    whatever the sign-in or re-authentication method: an OIDC session that skipped Ledger's second step because
    Ledger trusts the provider (D16), or that re-authenticated through the provider, must give it too. A first
    enrolment (TOTP not enabled) needs none, and neither does the host (`ledger auth reset-2fa`); tokens cannot reach
    these routes (session-only).
    - Missing, empty or both given: 400 `validation_failed` on `current_code`. Wrong, reused or malformed: 400
      `invalid_second_factor` on the field given, audited as `auth.reauthentication_failed` (method `totp` or
      `recovery_code`, reason `invalid_second_factor`). Nothing changes on any refusal.
    - The check uses the sign-in machinery: the attempt is reserved in the per-username and per-address limits
      (point 7) and a failure counts, so a stolen session cannot guess the code (429 `rate_limited` once blocked,
      first block audited with reason `locked_out`); a TOTP code's step is recorded as used (replay protection, so
      the confirmation's own code cannot also disable it in the same step); a recovery code is consumed.
    - When replacing, the new authenticator's code is checked first, so a mistyped new code uses up no recovery
      code; the current factor is checked before the change is written.
    - The web UI asks for the code in the replacement form and in the turn-off and replace-codes dialogs, with a
      switch to a recovery code; errors are shown on the field.

## Why

- Sealing with a KEK-derived key keeps a copy of the database (a backup, a stolen file) from yielding working
  authenticators. TOTP secrets cannot be hashed: the server needs them to compute codes. HKDF gives each kind of secret
  its own key from the one KEK; it does not stretch a weak KEK, so the KEK must be long and random (the configuration
  text says so). A password KDF for the KEK was rejected: it would cost every start and the KEK is not typed by a person.
- 80-bit recovery codes make a fast hash sufficient: brute-forcing one from its SHA-256 hash is out of reach, so
  checking a code needs no Argon2id (which would cost up to ten 64 MiB hashes per attempt).
- Keeping the challenge in memory, not in `sessions`, means no row exists that could be mistaken for a session, and
  nothing about it survives a restart. The cost is that a restart between the two steps asks for the password again.
- Refusing reads too during required enrolment is the requirement ("blocks access until satisfied"); an allow list is
  safer than a deny list as routes are added.
- Point 13: recent re-authentication proves only the password (or the provider). Without the current factor, a
  person who learned the password and reached a session (or a provider account) could remove or replace the second
  factor and so defeat it; asking for it means changing 2FA needs what signing in needs. Recovery codes are accepted
  because they are the documented fallback for a lost authenticator; whoever has neither uses the host reset.
  Counting failures in the sign-in limits keeps the code from being guessed through these routes.
- Ending other sessions when TOTP is enabled follows the password-change rule: a session opened without the second
  factor (possibly by someone else) should not outlive its enabling.

## Consequences

- **Changing the KEK breaks enrolled authenticators** until the user signs in with a recovery code and enrols again, or
  the host runs `ledger auth reset-2fa`. There is no KEK rotation yet.
- Re-authentication (`POST /auth/reauthenticate`) still checks only the password (or the provider, ADR-0033), also for
  users with TOTP. Changes to the second factor itself ask for it as well (point 13); other sensitive actions
  (password change, token creation, user management) do not, and whether they should is still open.
- There is no administrator route to reset another user's 2FA; the host command covers the single-person deployment.
- UI pages are static assets: the server guarantees that a confined session reaches no protected data, and the A7
  screens must route to enrolment using `two_factor.enrollment_required`.
- A6 (OIDC) must decide per D16 whether an OIDC sign-in is confined by `auth.require_2fa` (D16: by default it trusts the
  IdP).
- A4 (API tokens) must not set `EnrollmentRequired` on token principals.
- Migration number: written as `0012`, renumbered `0013_totp` at merge because A4's `0012_api_tokens` landed first.
  `0012_api_tokens` is the fixture of the previous version (added at merge).
