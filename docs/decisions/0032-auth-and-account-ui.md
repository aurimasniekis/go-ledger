# ADR-0032: Auth and account UI

Status: Accepted (2026-11-21)

## Context

Task A7. The server side of sign-in, sessions, CSRF, re-authentication (ADR-0026), API tokens (ADR-0029),
TOTP, recovery codes and required 2FA (ADR-0030) and users and roles (ADR-0021) exists; this task adds the screens.
Task U1 (the app shell and shared state primitives) is not built yet, so the shell here is deliberately minimal.

## Decision

1. **Where things are.** Feature code lives in `web/src/features/{auth,account,tokens,admin}`. Shared primitives
   are in `web/src/components` (`Field`, `FormError`, `QueryState`, `ConfirmDialog`, `ShowOnceDialog`,
   `ui/dialog`, `ui/input`) and the API client in `web/src/lib/api.ts`.
2. **API client.** `apiFetch` sends `X-CSRF-Token` on every non-GET/HEAD request once a session is known (the token
   comes from sign-in or `GET /auth/session` and is held in memory only), sets the JSON or merge-patch content type, and
   keeps the envelope's `errors[]` and `Retry-After` on `ApiError`. On 403 `reauthentication_required` it asks a
   registered handler (the re-authentication dialog) and, if the password check passes, repeats the request once.
   Declining leaves the action undone and shows the server's message.
3. **Session gate.** `RequireSession` is the layout of every protected page. Signed out goes to `/sign-in` with the
   place the user was going; a session whose `two_factor.enrollment_required` is true is sent to
   `/account/security` and the navigation shows nothing else. Any response with `two_factor_enrollment_required` or
   `unauthenticated` refetches the session, so the gate reacts when the server's view changes. The server remains
   the authority: the gate only avoids screens that would answer 401 or 403.
4. **Field-associated errors.** A `Field` renders a real label and ties its hint and error to the control with
   `aria-describedby` and `aria-invalid`. `splitErrors` maps `errors[]` to request fields (a nested name such as
   `schedule.day` matches its top-level field) and returns whatever matches no field as a form-level message. A
   form-level error is an `role=alert` that takes focus.
5. **Secrets are shown once.** The token and the recovery codes are held in the state of the page that created them
   only while `ShowOnceDialog` is open, and dropped by its Done button. Token creation is a plain call, not a
   mutation, so the response never enters the query cache. The dialog ignores Escape and outside clicks (a stray key
   must not lose something that cannot be shown again), offers Copy and Download, says plainly when copying fails and
   has no way to reopen. TOTP enrolment mutations use `gcTime: 0`.
6. **Pending, failed, stale.** A button shows its pending state and is disabled while a write is in flight; a success
   message appears only after the server answers. `QueryState` shows loading, a failed load with a retry, a failed
   refresh over earlier data (a Stale badge with the time of the last good fetch) and the time of the last update.
7. **Destructive actions.** Disabling or re-enabling a user is reversible, so it needs no confirmation and offers Undo.
   Revoking a token, ending a session, turning 2FA off and replacing recovery codes cannot be undone and go through an
   `AlertDialog` that stays open with the error when the server refuses.
8. **QR code.** No QR dependency is added: a QR encoder is not in the dependency list and ADR approval is needed for a
   new library. Enrolment shows the base32 setup key and the `otpauth://` link and says there is nothing to scan.
   Adding a small encoder (with its own ADR note) is the follow-up if a QR code is wanted.
9. **TanStack Form.** Not installed, and not added here. The forms are short (one to four fields); controlled inputs
   plus `Field` and `splitErrors` cover them. Introduce it, with an ADR note, with the first substantial form
   (commitment editing).
10. **Timestamps** are shown in the browser's locale and zone. The reporting timezone (an open decision) applies to
    dates in reports, not to these instants.

## What U1 should take over

The minimal `Shell` in `router.tsx` (header, skip link, `AccountNav`), the `RequireSession` layout, and
`QueryState` / `FormError` as the shared state primitives. `ReauthProvider` should stay inside the signed-in layout.
The navigation needs a small-screen pattern (it only wraps today).

## Open product questions (not decided here)

- Whether re-authentication should also ask for a second factor (ADR-0030).
- Whether a read-only user should see their own username (the user API is administrator-only; the shell shows no name).
- Administrator reset of another user's 2FA, and password-reset by email, do not exist; the host command covers them.
- OIDC sign-in is A6; this UI has no OIDC button.

## Consequences

- Playwright covers only the signed-out screens: the e2e server starts with an empty database, and creating a user
  needs the host CLI with the server stopped. A sign-in e2e needs a seeded database (follow-up).
- The sign-in page cannot tell the user which attempt count is left; it shows the server's `Retry-After` when limited.
