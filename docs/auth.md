# Authentication, API tokens and recovery

This page is the operator guide for signing in to Ledger, for the API tokens that automation uses, and for getting back in
when normal sign-in fails. The design and its reasons are in the decision records: [ADR-0021](decisions/0021-users-roles-and-grants.md)
(roles and grants), [ADR-0026](decisions/0026-passwords-sign-in-and-sessions.md) (passwords and sessions),
[ADR-0029](decisions/0029-api-tokens-and-bootstrap.md) (API tokens), [ADR-0030](decisions/0030-totp-recovery-codes-and-required-2fa.md)
(two-factor), [ADR-0033](decisions/0033-oidc-single-sign-on.md) (OIDC and break-glass) and [ADR-0036](decisions/0036-auth-and-account-ui.md)
(the screens). For setting up the identity provider, see [oidc.md](oidc.md).

Examples use placeholders: `admin` for a username, `ledger.example.com` for Ledger's address and `id.example.com` for an
identity provider. Host commands read the same configuration as `ledger serve`.

## Sign-in methods

| Method | Who it is for | Notes |
| --- | --- | --- |
| Password | Everyone with an account | Argon2id. Minimum length `auth.password.min_length` (12 bytes by default), no composition rules. |
| Password plus TOTP or a recovery code | Accounts with two-factor authentication | Required for everyone when `auth.require_2fa` is on (ADR-0030). |
| OpenID Connect | Accounts linked to the configured provider | Optional, one provider (ADR-0033). Off by default. |
| Recovery sign-in link | The host, when every other method fails | One use, short-lived, issued on the host (see [Break-glass](#break-glass-on-the-host)). |
| API token | Unattended automation | A bearer token, never a session. Skips interactive steps by design (see [API tokens](#api-tokens)). |

A session lasts until it has been idle for `auth.session.idle_timeout` (30 minutes by default) or has existed for
`auth.session.absolute_timeout` (12 hours by default). Sensitive actions need a fresh password check within
`auth.session.reauth_window` (10 minutes by default). Failed password sign-ins are limited per username and per client
address, 10 and 30 failures per 15 minutes by default.

A browser that has signed in successfully keeps a device cookie for 90 days (`auth.sign_in.device_cookie_ttl`, 2160h by default), at most 20 per user (decision D30). Its failed
attempts count against the cookie instead of the username, so a username locked out by someone else's guesses does not
lock out the owner's known browsers. The cookie never bypasses the password or the second factor. Changing a user's
password deletes that user's device cookies.

## OIDC linking and unlinking

Linking and unlinking are done by a signed-in person from Account security or the API. Both need a session with recent
authentication.

- **Link.** `POST /api/v1/auth/oidc/link` returns the provider's authorization URL. Open it in the same browser. When
  the provider sends the person back, the identity (issuer and subject, never the email address) is linked to this
  account, provided the session is still live. An account has one link: a second link is refused with 409 until the
  first is removed. An identity already linked to another account is refused.
- **Linking never turns passwords off.** Password sign-in stays available until `auth.password.enabled` is set to `false`.
- **Unlink.** `DELETE /api/v1/auth/oidc/link` removes the link. It is refused with 409 unless the account would keep a
  sign-in method: it must have a password **and** password sign-in must be enabled. Sessions already started stay open
  until they time out or are ended.
- **Passwords off.** With `auth.password.enabled: false` nobody can remove their own link, since it would be their only way
  in. A host unlink is the way out.
- **Host unlink.** `ledger auth unlink-oidc admin` removes a user's link from the host, for example after their provider
  account was replaced. The user then links the new identity after signing in another way.
- **Provider change.** Changing `auth.oidc.issuer` leaves every existing link unusable; sign-in answers `not_linked`. Use a
  recovery sign-in, unlink each account and link again.
- **Provisioning** (`auth.oidc.provisioning.enabled`, off by default) creates an account on first sign-in with the role
  `auth.oidc.provisioning.role` (`read-only` by default) and no password. Accounts are never matched by email address.

## Break-glass on the host

Use these when the identity provider is down, passwords are off, or nobody can sign in. They run on the machine that holds
the database, with `ledger serve` **stopped**: each command takes the instance lock and refuses to run while the server
holds it. Each one is audited as done by the host.

| Situation | Command | Effect |
| --- | --- | --- |
| Get one person into the web UI once | `ledger auth recovery-sign-in admin` (`--ttl` 15m by default, at most 1h) | Prints a one-time link and replaces any unused one. Start `ledger serve` and open the link before it expires. The page asks for a confirmation: merely fetching the link does not use it, so a link previewer cannot spend it. Pressing the button signs the person in once, without the provider and without a second factor, with passwords on or off. The session still follows `auth.require_2fa`. |
| Give an account a password | `ledger auth set-password admin` (or `--create administrator` on a new database) | Reads the password from the first line of standard input. Ends the user's other sessions and deletes their device cookies. Pipe the password in: a terminal echoes it. |
| Remove a stuck OIDC link | `ledger auth unlink-oidc admin` | Removes the link; the user links again after signing in another way. |
| Remove a lost authenticator | `ledger auth reset-2fa admin` | Deletes the user's TOTP secret and recovery codes. With `auth.require_2fa` on, the user must enrol again at the next sign-in. |

Things to know:

- The printed link starts with the origin of `auth.oidc.redirect_url`. If that is unset, the output contains the placeholder
  `<Ledger's address>` in place of the origin: replace it with the address your users open before using the link.
- The recovery link carries its secret in the URL. Treat it like a password: anyone holding it can sign in as that user
  until it is used or expires. Ledger keeps only its hash. A reverse proxy that logs query strings would keep the link in
  its logs, which is why it is short-lived and single-use.
- To bring password sign-in back, set `auth.password.enabled: true` in configuration and restart `ledger serve`. Give
  accounts that have no password one with `set-password` first.
- Changing the key-encryption key (`kek.value` or `kek.file`) makes enrolled authenticators unreadable. The fix is `reset-2fa`, or a recovery
  sign-in and enrolment again (ADR-0030).
- Host commands do not revoke API tokens. See [Revoking](#revoking-and-disabling).

## API tokens

A token is a bearer credential: `Authorization: Bearer ldgr_…`. It is 43 random characters after the `ldgr_` prefix, shown
**once** when it is created, and stored only as a SHA-256 hash with a short hint (the prefix with the first four and the last four characters).
If the header is present it is the only credential judged: an invalid or revoked token is refused even when a valid session
cookie is also sent. Ledger never logs the header or the token.

### Bootstrap: the first token on a new database

The first user on a database is the **root account** (the earliest `created_at`). On a new database, the host creates it
and its first token in one step. The token is written to standard output alone, so pipe it straight into your secret store
and nothing else:

```sh
ledger token create --create administrator --root --name bootstrap --expires 2160h admin \
  | your-secret-store put ledger-root-token
```

- `--create ROLE` creates the owner when it does not exist. The account has no password until `ledger auth set-password admin`.
  The grants are checked against the new role first, so a refused token leaves no user behind.
- `--root` makes a root token (see [The power of root and long-lived tokens](#the-power-of-root-and-long-lived-tokens)).
  Use `--grant resource:action` (repeatable or comma-separated) instead for a narrower token. Give either `--root` or
  `--grant`, not both.
- `--expires` takes an RFC 3339 time or a duration such as `2160h`. Without it the token never expires.
- The summary, the root warning and the "shown once" notice go to standard error.
- The host command needs the server stopped. Day-to-day tokens are created through the API or the web UI.

### Creating tokens through the API

A signed-in person creates tokens for themselves with `POST /api/v1/auth/tokens` (session, recent authentication):

```json
{"name": "ci", "grants": ["commitments:read"], "expires_at": "2027-06-30T00:00:00Z"}
```

The 201 response holds the token in `secret` once, with `Cache-Control: no-store`. A token may hold only grants its creator
holds. Only an administrator can ask for a root token. Listing your own tokens uses `GET /api/v1/auth/tokens`; revoking one uses `DELETE /api/v1/auth/tokens/{id}`.
Administrators list and revoke everyone's tokens with `GET /api/v1/tokens` and `DELETE /api/v1/tokens/{id}` (grants
`tokens:read` and `tokens:write`); no route ever returns a token value after creation.

**Tokens cannot create tokens**, with one exception: a root token owned by the root account (decision D25). It can create
other tokens, bounded by its own grants. This lets a single root token of the root account manage tokens, for example from
the Terraform provider.

### Least privilege: grants

A grant is `resource:action`. `write` implies `read` on the same resource. There are no wildcards: an unknown grant is refused.

| Grant | What it allows | Read-only role |
| --- | --- | --- |
| `commitments:read`, `commitments:write` | Recurring commitment definitions | read |
| `audit:read` | The audit log | no (administrator only) |
| `banking:read` | Bank-sourced text: raw transaction descriptions and counterparty names (decision D28 5b; administrator only, decision D29, ADR-0035 point 9) | no (administrator only) |
| `connections:read`, `connections:write` | Bank connections: connect, reconnect, refresh, disconnect, and read accounts, balances and freshness (task B4, ADR-0040) | no (administrator only) |
| `decisions:read`, `decisions:write` | Model suggestions (with the provider and model that said them) and in-app alerts: read, accept, correct, reject, revert, dismiss (task L6c, ADR-0065). Accepting or correcting a suggestion also writes an assignment, so it needs `organisation:write` too (and `merchants:write` for a merchant) | no (administrator only) |
| `integrations:read`, `integrations:write` | Settings of everything that sends data out or holds an integration secret: merchant enrichment and Pulse publishing (ADR-0066); bank applications, decision providers, tasks and consents follow. Secrets are write-only: no response, log or audit entry ever holds one. The generic settings routes of these groups need `settings:read` or `settings:write` as well | no (administrator only) |
| `merchants:read`, `merchants:write` | The merchant identity of bank transactions: the lookup source's enrichment and the person's confirm, correct or reject decision about it (task E1, ADR-0051); merchant names derive from bank text | no (administrator only) |
| `organisation:read`, `organisation:write` | Categories, labels and buckets, and assigning them to transactions, commitments, accounts and merchants (task O1, ADR-0053); the person's own labelling, not bank text | read |
| `rates:read`, `rates:write` | Cached exchange rates with provenance and freshness (read), and asking the rate provider for fresh rates (write) (task FX2c, ADR-0048; administrators only since decision D-FX-R, task FX4). Converted totals in the outlook and month views stay visible to roles that can read those views: they do not need this grant | no (administrator only) |
| `rules:read`, `rules:write` | Matching rules (ADR-0066; the rules API is task X2b). An expression can hold fragments of bank text, which decision D29 keeps from read-only users | no (administrator only) |
| `settings:read`, `settings:write` | Runtime settings (ADR-0066): read each group's effective values with the source of every one, the read-only deployment settings, and change the members no configuration layer pins. Members that weaken sign-in, redirect traffic or send data out (`Sensitive` in the registry), every secret and the reporting zone are refused until their gates land (tasks S2 and S3) | no (administrator only) |
| `tokens:read`, `tokens:write` | Everyone's tokens: list and revoke. Never a token value. | no (administrator only) |
| `users:read`, `users:write` | User accounts, roles, disabling and passwords | no (administrator only) |

- A **read-only** user holds `read` on every resource that is not administrator-only, and no write. Read-only users and their
  tokens never see bank text.
- A token's effective grants are its stored grants **intersected with its owner's current role**. Demoting or disabling the
  owner takes effect on the next request. A write grant the owner no longer holds is dropped entirely, not reduced to read.
- Give automation the smallest grant set that works, one token per consumer, and an expiry.

### Rotation and expiry

- **Expiry.** A token with `expires_at` in the past is refused with 401. Tokens never expire unless one is set.
- **Rotation** is manual: there is no rotate endpoint. Create a replacement with the same grants, put it in place,
  check its `last_used_at` in the token list, then revoke the old one. **Revoking a root token of the root account also revokes every token it created through the API**, so rotate such a token only after you have recreated what depended on its children.
- **Finding unused tokens.** `last_used_at` is written at most once a minute. A token whose last use is old or empty is a
  candidate for revocation.
- **Password changes do not revoke tokens** (decision D24). Revoke them explicitly after a suspected compromise.

### Revoking and disabling

- **Revoke** (`DELETE` on the token) stops the token at once, for good. A revoked token cannot be reused.
- **Disable the owner** stops all their tokens at once, but nothing is revoked: **re-enabling the owner revives every token
  that was not revoked and has not expired** (decision D25). Disabling is a suspension, not a revocation. To end a leaked token,
  revoke it.
- The host command cannot list or revoke tokens yet. If no administrator session or token is available, disabling the owner
  is the only host-side stop, and it is reversible.

### Cascade to child tokens

A root token of the root account stores its children's lineage. When it creates a token:

- The child's expiry is the requested one or the parent's, whichever is **earlier**. A child cannot outlive its parent.
- Revoking a token revokes all its descendants in the same transaction, each audited with `cascaded_from`.
- Creation rechecks the parent, so a parent revoked while a child is being created leaves no child.

Tokens made by a session or by the host have no parent and do not cascade.

## The power of root and long-lived tokens

**A root token holds every grant its owner's role holds, now and in future versions of Ledger.** It is created only when
asked for explicitly (`"root": true` or `--root`), only by an administrator, and every interface shows a warning with it.
Because it follows the owner's role, a root token of an administrator can read bank text, administer every user and token,
read the audit log and change finance data. Demoting the owner narrows it at once; disabling the owner suspends it.

Treat a root token like a password for the whole instance:

- **Tokens skip recent authentication and the second factor by design.** Their writes are audited as `token.write_used`
  with `interactive_steps: skipped`. Routes that need recent authentication for a session do not ask a token for it.
- **Only the root token of the root account can escalate accounts** (decision D32). Creating an administrator, promoting a
  user to administrator, re-enabling a disabled administrator and setting any password (`PUT /api/v1/users/{id}/password`)
  are refused to every other token with 403 `root_token_required`, even one holding `users:write`. A `users:write` token can
  still create non-administrators, edit profiles, and demote, disable and rename users other than the root account. Sessions are unchanged
  (recent sign-in).
- **The root account and the root token are protected the same way** (decision D34). Demoting, disabling or renaming (changing the username of) the root (first)
  account (decision D35 for the rename; `PATCH /api/v1/users/{id}`) and revoking the root token (`DELETE /api/v1/tokens/{id}`, which also ends its child
  tokens) are refused to every token except a root token of the root account, with 403 `root_token_required`, even for a token holding
  `users:write` or `tokens:write`. A session may do any of these.
- **The root token of the root account** is the only token that can create tokens through the API. Its power is therefore the
  power to make any other token, bounded by its own grants.
- **Long-lived tokens** outlive sign-outs, password changes and session timeouts. They end only by revocation, by expiry, by a
  demotion that removes their grants, or by disabling the owner (until it is re-enabled).

Practical rules:

- Prefer a token with a narrow grant set and an expiry over a root token. Use `--root` only for tooling that must manage tokens.
- Keep tokens in a secret store. A copy of the database holds only hashes, so it cannot be used as a token, but a leaked
  token works until it is revoked.
- Rotate on a schedule and whenever someone with access leaves. Revoke tokens of a decommissioned consumer.
- Keep the root account's password and two-factor set-up protected, because a password can always be used to make a
  session that creates tokens.

## Known limits

- Failed password sign-ins lock a username, for everyone not known by a device cookie, until its failures age out of the 15-minute window. An attacker who knows the username can keep it locked; restarting `ledger serve` clears the counts (ADR-0026).
- The root account is the earliest-created user. Ledger does not pin it in a setting. Check `created_at` on the first users
  before relying on it after a clock change at bootstrap (ADR-0029).
- A recovery code is spent before a second-factor change is written. If that write then fails, the code is lost and the
  change did not happen; regenerate codes (ADR-0030).
- OIDC supports one provider, no logout at the provider, no group or claim mapping to roles, and is tested against a local fake
  provider rather than a real one (ADR-0033).
- There is no administrator route to reset another user's two-factor set-up; use `ledger auth reset-2fa`.
- Token rotation is manual and the host cannot list or revoke tokens yet.
