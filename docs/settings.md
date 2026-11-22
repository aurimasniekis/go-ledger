# Runtime settings

Ledger's settings come in two kinds (ADR-0066).

- **Runtime settings** are stored in the database, managed through the Management API and applied without a restart. Every
  user-configurable product setting is one. They are grouped by the component that uses them: `reporting`, `banking`,
  `rates`, `merchant_enrichment`, `auth`, `oidc`, `pulse`, `mcp`, `logging` and `alerts`.
- **Deployment settings** stay in the configuration files and the environment: the database path, the listeners and their
  timeouts, the trusted proxies, the log format, the key-encryption key and every key that names a secret file. The API shows
  them read-only (`GET /api/v1/settings/deployment`); secrets, file paths and application identifiers appear as set or unset
  only. The matching windows, outbox limits and refresh budget are fixed policy in code, not settings.

Each key's description in `config.example.yaml` and `.env.example` says which kind it is.

## Precedence

The effective value of a runtime setting is, highest first:

1. a command-line flag
2. an environment variable (the real environment, then the `.env` file)
3. the YAML file
4. the value stored through the API
5. the built-in default

A key that any of the first three sets is **pinned**. The API shows it with its source (`flag`, `environment`, `dotenv`,
`yaml` or `file`) and `pinned`, and refuses a write that names it with 409 `setting_pinned`, naming the key and the layer,
never the value. The stored value of a pinned key is kept and applies again when the pin goes away. The environment is always
a way back that needs no working sign-in: set the variable and restart.

## The API

| Request | Needs | Does |
| --- | --- | --- |
| `GET /api/v1/settings` | `settings:read` | Lists the groups with version, last write, whether anything is pinned and the apply state. |
| `GET /api/v1/settings/{group}` | `settings:read` | The effective values with `sources`, `pinned`, `version` (also the ETag) and `apply_state`. |
| `PATCH /api/v1/settings/{group}` | `settings:write` | A JSON Merge Patch (`application/merge-patch+json`) with `If-Match`. `null` removes a stored value. |
| `DELETE /api/v1/settings/{group}` | `settings:write` | Resets every unpinned member to its default, with `If-Match`. |
| `GET /api/v1/settings/deployment` | `settings:read` | The read-only deployment settings. |

- Members are named as in the group's schema; dotted names such as `refresh.interval` are flat members.
- A group always exists. Until it is first written it is synthesised with version 1; the first write names version 1.
- The whole would-be configuration is validated with the rules startup uses before anything is stored; a problem is 400
  `validation_failed` with one error per configuration key.
- Groups of the `integrations` grant (`merchant_enrichment`, `pulse`) also need `integrations:read` or `integrations:write`.
- Every write is audited in the same transaction as `settings.updated` or `settings.reset` with the group and the names of the
  changed members, never a value.
- `apply_state` is `applied`, `failed` (a component could not rebuild after the write; the setting stays stored) or
  `restart_required` (stored and valid, but the group has no live consumer yet, so it takes effect at the next start). Today
  only `logging` is applied live.
- Secrets are never returned. A secret member has read-only companions (`client_secret_set`, `client_secret_set_at`,
  `client_secret_source`).

Until the sign-in gate and sealed secrets (task S2) and the reporting-zone rule (task S3) land, every sensitive member, every
secret and `reporting.timezone` are refused as "not writable through the API yet" (400). The error codes
`secret_unreadable` and `reclassification_required` are reserved for those tasks.
