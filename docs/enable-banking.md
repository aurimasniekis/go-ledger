# Enable Banking setup

Ledger reads bank accounts, balances and transactions through [Enable Banking](https://enablebanking.com/) (ADR-0023).
This page covers what you need to set up for each of the two access modes, what each mode can and cannot see, and the
steps only a person can do. For the rest of Ledger's auth and configuration, see [auth.md](auth.md) and
[ADR-0004](decisions/0004-configuration.md).

Status on this branch: the adapter, the configuration slots and the background refresh are built and tested offline
against synthetic fixtures. The connection flow (starting consent, the redirect back to Ledger and the connection
screens) **arrives with B4**. Nothing here has been run against a live bank (decision D20).

## The two modes

Ledger has two access modes (decision D10). They are Ledger's names. Enable Banking's documentation does not use
"app mode" or "developer mode"; it describes the same things as a production application's activation state
(restricted or unrestricted). Neither mode is the sandbox.

| Ledger mode (configuration slot `banking.enable_banking.app` or `banking.enable_banking.developer`) | Enable Banking term | Environment | Accounts a connection can reach | Who it is for |
|---|---|---|---|---|
| `app` | Production, **unrestricted** (full activation) | `production` | Every account the bank user authorises | Use beyond your own accounts. Needs a signed contract, KYB and manual review by Enable Banking. |
| `developer` | Production, **restricted** (activated by linking accounts) | `production` | **Only** accounts linked to the application in the Enable Banking Control Panel. Unlinked accounts are removed from responses without an error. | Your own linked accounts, individual non-commercial use. |
| (not a mode) | Sandbox | `sandbox` | Simulated banks and the Mock ASPSP | Tests and fixtures only. Ledger's test suite does not need it. |

Facts from the public documentation that the two modes share (read 2026-11-20, [research R1](decisions/0023-enable-banking-adapter.md)):

- Both modes use the same API host and the same authentication. The environment belongs to the application, not to the URL.
- Each application has its own application id (sent as the JWT `kid`) and its own RSA key. Ledger can hold several applications at once.
- The documentation names no other difference between restricted and unrestricted applications (for example in rate limits or history depth). None is assumed here. Whether an application is restricted cannot be read through the API: `GET /application` does not report it.

### Coexistence

Both modes can be configured at the same time, one application each. Every bank connection records the application
(its Ledger name) it was made with, so each connection's mode is known. Connections made with one application are
refreshed with that application's key. The mode and its account-access consequence are meant to be visible on each
connection; that display **arrives with B4**.

Changing an application's Ledger name (`name`) orphans the connections recorded under the old name.

## Set up the Enable Banking side (a person does this)

These steps happen in the Enable Banking Control Panel and at the banks. Ledger cannot do them, and no credentials
belong in this repository.

1. **Create an Enable Banking account** in the Control Panel (email sign-in; optional authenticator app).
2. **Register an application** for each mode you want. Choose environment `Production` for both `app` and `developer`.
   - Redirect URLs: add the consent return address that your Ledger instance uses. The exact path is set by the
     connection flow, which **arrives with B4**; use the value that flow documents, with your own host, for example
     `https://ledger.example.com/<path from B4>`.
   - Note the **application id** (a UUID). It is not a secret, but Ledger never logs it.
3. **Create the application key.** Either let the Control Panel generate an RSA key pair in your browser (the private
   key is saved locally and is not sent), or generate one yourself and upload only the public key. Use at least
   2048 bits. Ledger accepts PEM in PKCS#8 form (block type `PRIVATE KEY`) or PKCS#1 form (block type `RSA PRIVATE KEY`).

   ```sh
   # Run in a private directory (umask 077). Keep the file out of the repository.
   umask 077
   openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out ledger-app-key.pem
   ```

   The private key is the one secret Ledger needs. Store it in an operator-approved secret source.
4. **Developer mode only: link accounts.** Use the application's activation by linking accounts flow in the Control Panel
   and confirm the link at your bank. Linking does not authorise API access by itself: Ledger still needs the normal
   consent flow for each bank connection. An account that is not linked does not appear, and Ledger shows no error for it.
5. **App mode only: production approval.** Enable Banking reviews the application before it is unrestricted. The
   documentation asks for an application description, a data protection contact, a privacy policy URL and a terms of
   service URL, and it requires a signed contract, KYB and a billing account. Enable Banking's Terms of Service
   "Restriction of Use" section was **not read** for this page; read it before relying on either mode.

Public references: [Quick start](https://enablebanking.com/docs/api/quick-start/),
[Control panel guide](https://enablebanking.com/docs/api/control-panel/),
[Linked accounts (restricted production)](https://enablebanking.com/docs/api/linked-accounts/),
[FAQ](https://enablebanking.com/docs/faq/), [Sandbox](https://enablebanking.com/docs/api/sandbox/),
[API reference](https://enablebanking.com/docs/api/reference/) and the
[OpenAPI description](https://enablebanking.com/docs/api/reference/enablebanking-api.yaml) (version `1.0.0-1c03cf81`).

## Configure Ledger

Each mode has its own slot. Enable only the ones you use. Every key has an environment variable; secrets go only in
the environment or in a file, never in the YAML file or a flag.

```yaml
banking:
  enable_banking:
    base_url: "https://api.enablebanking.com"   # shared by both modes
    app:                                         # unrestricted production (app mode)
      enabled: true
      name: "app-main"                           # optional; default enablebanking-app
      app_id: "00000000-0000-4000-8000-000000000000"   # placeholder: your application id
      environment: "production"
      private_key_file: "/run/secrets/enablebanking-app.pem"
    developer:                                   # restricted production (developer mode)
      enabled: true
      name: "dev-linked"                         # optional; default enablebanking-developer
      app_id: "00000000-0000-4000-8000-000000000001"   # placeholder
      environment: "production"
      private_key_file: "/run/secrets/enablebanking-developer.pem"
```

The same settings as environment variables (use the key only from a secret store or an `.env` file with restricted
permissions):

```sh
LEDGER_BANKING_ENABLE_BANKING_APP_ENABLED=true
LEDGER_BANKING_ENABLE_BANKING_APP_APP_ID=<application id>
LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY_FILE=/run/secrets/enablebanking-app.pem
# or, instead of the file: LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY=<the PEM text on one line, with \n for line breaks>
LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_ENABLED=true
LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_APP_ID=<application id>
LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_PRIVATE_KEY_FILE=/run/secrets/enablebanking-developer.pem
```

Rules the configuration enforces (checked by `ledger config check` and at `ledger serve` startup):

- When a slot is enabled, `app_id` (a UUID) and a key source (`private_key` or `private_key_file`) are required.
- Application names must be unique and 1 to 64 lower-case letters, digits, `-` or `_`.
- `base_url` must be `https`. Plain `http` is allowed only for a loopback test server.
- `environment` is `production` or `sandbox`. Use `sandbox` only for a sandbox application; it is not a mode.

Check the configuration without starting the server (it makes no network call):

```sh
ledger config check --config /etc/ledger/config.yaml
ledger config env   # every key with its variable, default and secrecy
```

At `ledger serve` startup Ledger parses each enabled key. A bad key stops startup, with an error that names the
configuration key and never the key material or the application id. Startup logs each application's name, mode,
environment and account access. It makes **no** request to Enable Banking. On this branch `ledger serve` does not call
`Verify` (the `GET /application` check of key, environment and active flag); wiring that check into a status page
is part of B4.

Background refresh (`banking.refresh.*`) runs against every enabled application. Its interval, backoff and daily
budget are described in [ADR-0028](decisions/0028-background-refresh-scheduler.md).

## What each mode changes

| Topic | `app` (unrestricted production) | `developer` (restricted production) |
|---|---|---|
| Accounts returned | All accounts the bank user authorises | Only accounts linked in the Control Panel. Unlinked accounts are dropped silently; an empty list is a normal result, not an error. |
| Setup before first connection | Production approval, contract, KYB | Link accounts in the Control Panel first |
| Consent (bank authorisation) | Same flow, same limits | Same flow, same limits |
| Data source | The bank, live | The bank, live (the same data for a linked account) |
| Ledger's view of the difference | `account_access = all` | `account_access = linked_only`, shown on the connection (B4) |

Data availability is the same for both modes as far as the documentation shows. These points apply to every
connection, whatever the mode:

- **Consent expires.** The bank sets the consent lifetime, capped at the bank's maximum (often 180 days). Enable Banking
  does not renew it: when it passes, the user must authorise again. A session can also end early (`EXPIRED_SESSION`,
  `REVOKED_SESSION`, or the bank requiring a new sign-in). Ledger reports these as a connection that needs reconnecting;
  it keeps the last good data and its age until then.
- **Re-authorising changes identifiers.** A new session has new account `uid`s. Ledger matches accounts by the
  stable `identification_hash`, not by `uid`, so history is not duplicated.
- **History depth varies by bank.** A new connection may return a limited window at first (some banks give the full
  history only some time after authorisation, and others only the last ~90 days). Ledger requests the longest history
  the bank offers on the first import.
- **Pending and booked.** Pending transactions can change or disappear. Ledger keeps their status and does not treat
  them as settled; a pending row is not a payment.
- **Balances.** A bank supplies only some balance types. A balance that the bank does not give is shown as unknown,
  never as zero.
- **Background fetches are limited.** Many banks allow only about four background fetches a day when the user is not
  online. A rate limit (`ASPSP_RATE_LIMIT_EXCEEDED` or HTTP 429) waits at least 6 hours, or the `Retry-After` the
  bank gives, and the scheduler stays within the daily budget.
- **PSU headers are not sent yet.** Some banks need PSU headers (for example the user's IP address) on a user-triggered
  request and reject requests without them, and most banks allow more background fetches when they are sent. The adapter
  does not send them on this branch; how user-triggered refreshes carry them is part of B4.

## Limitations

- Nothing here has been checked against a live bank or a live Enable Banking account (D20). Behaviour is taken from the
  public documentation and offline fixtures written from its OpenAPI schemas.
- "App mode" and "developer mode" are Ledger's names (D10), not Enable Banking's. If Enable Banking changes its
  terms or activation rules, update this page and the mode table.
- The Terms of Service "Restriction of Use" section was not read (research R1), so the limits on developer mode's use
  are not confirmed.
- Pricing and quotas are not documented in the pages read; Ledger does not assume a limit beyond the bank's own.
- The connection flow, the connection screens and the mode display are built in B4 and are not described in detail here.
