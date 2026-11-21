# Single sign-on (OpenID Connect)

Ledger can let people sign in with one OpenID Connect identity provider (ADR-0033). It is off by default. Passwords
keep working unless you turn them off.

## Set it up

1. Register Ledger at your provider as a confidential client using the authorization code flow, with the redirect
   URI `https://<Ledger's address>/api/v1/auth/oidc/callback`. Ledger always uses PKCE (S256).
2. Configure Ledger (YAML keys shown; every key also has a `LEDGER_` variable, see `ledger config env`):

   ```yaml
   auth:
     oidc:
       enabled: true
       name: Home ID                       # shown on the sign-in page and in audit entries
       issuer: https://id.example.com/realms/home   # exactly as in the discovery document
       client_id: ledger
       client_secret_file: /run/secrets/ledger-oidc  # or LEDGER_AUTH_OIDC_CLIENT_SECRET; never in YAML
       redirect_url: https://ledger.example.com/api/v1/auth/oidc/callback
   ```

3. Each person signs in as usual and links their identity (`POST /api/v1/auth/oidc/link`, then the browser visits
   the returned URL). From then on the provider signs them in. A link is keyed by the provider's issuer and subject,
   never by email address.

Identities that are not linked get no access. To create accounts for them on first sign-in instead, set
`auth.oidc.provisioning.enabled: true` and choose `auth.oidc.provisioning.role` (`read-only` by default).

By default Ledger trusts the provider's own authentication and does not ask for a Ledger TOTP code after an OIDC
sign-in. Set `auth.oidc.require_2fa: true` to ask for it (and to apply `auth.require_2fa` to OIDC sessions).

## Turning passwords off

`auth.password.enabled: false` refuses every password sign-in and password re-authentication and sends the sign-in
page to the provider. It needs `auth.oidc.enabled: true`. Linking an identity never turns passwords off by itself.
With passwords off, people confirm sensitive actions by re-authenticating at the provider
(`POST /api/v1/auth/oidc/reauthenticate`), and nobody can remove their own link (it would be their only way in).

## Break-glass: the provider is down, or you are locked out

On the host, with `ledger serve` stopped:

```sh
ledger auth recovery-sign-in <username>          # --ttl 15m by default, at most 1h
```

It prints a one-time link. Start `ledger serve` and open the link in a browser before it expires. The link signs the
user in once, without the provider and without a second factor, whether or not passwords are enabled. Treat it like a
password: anyone with it can sign in as that user until it is used or expires. Issuing and using it are audited.

Other host commands:

- `ledger auth unlink-oidc <username>` removes a user's link (for example when their provider account was replaced);
  they link the new identity after signing in.
- To bring passwords back, set `auth.password.enabled: true` and, for an account without a password,
  `ledger auth set-password <username>`.
- `ledger auth reset-2fa <username>` removes a lost authenticator (ADR-0030).

Changing `auth.oidc.issuer` leaves every existing link unusable: use a recovery sign-in, unlink and link again.
