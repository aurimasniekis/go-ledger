# ADR-0066: Runtime settings through the Management API (D18 blanket coverage)

Status: Proposed (2026-11-22). Design only: no product code changes with it. The points marked **(control)** carry a recommended default that control confirms or changes when it creates the tasks.

## Context

Decision D18 (option B) makes the Terraform provider manage **all** user-configurable settings and user-defined objects, and the requirements ask for create, read, update and delete of every configurable object through the one public Management API that the UI, the CLI, Terraform and MCP share (requirements, Management API and Terraform sections; "identical validation and grants"). The provider's coverage inventory (terraform-provider-ledger ADR-0005, item B1) found that the API serves none of these:

- matching rules (expr-lang, decision D9; task X2b, not started);
- decision provider and model settings, and consent records (ADR-0060; tasks L7 and X3 own the routes and screens);
- application and planning settings (reporting zone and currency, freshness thresholds, refresh and rate settings);
- authentication options (password and session rules, sign-in limits, required two-factor, the OIDC provider);
- bank integration settings (the Enable Banking applications of decision D10: app mode and developer mode, per connection);
- Pulse publishing and other operational settings.

Today every one of them is a key of the configuration system (ADR-0004): one typed `config.Config` struct filled from defaults, YAML, the environment (with `.env`) and flags, validated once at startup, with secrets accepted only from the environment or a referenced file. The only value already kept in the database is the captured reporting zone (`settings` table, migration `0002_settings`), where an explicit configured value wins and a change is audited (`settings.reporting_timezone_changed`).

Constraints that apply: optimistic concurrency with `version`/ETag and 409 `version_conflict` (ADR-0011, ADR-0015); JSON Merge Patch (ADR-0015, ADR-0017); declared per-route access and a grant vocabulary with derived roles (ADR-0021); the append-only audit log with a fixed detail schema per action (ADR-0025); the hand-written client tied to the OpenAPI document by tests, and CLI commands through it (ADR-0032); MCP tools that are typed calls of API operations (ADR-0038); secrets sealed with the key-encryption key, as TOTP secrets are (ADR-0030); token escalation guarded by the root token of the root account (D25, D32, D34); no secrets in YAML, flags, logs, audit details or responses; tests offline.

This ADR decides which settings move into the database and the API, how they combine with the configuration files, and the shape every implementation task follows. It does not build anything.

## Decision

### 1. Two kinds of setting

1. **Runtime settings** are stored in SQLite, managed through the Management API (and so the UI, CLI, Terraform and, where point 9 allows, MCP), versioned, audited and applied **live**, without a restart. Every user-configurable product setting is a runtime setting unless point 2 names it.
2. **Deployment settings** stay in the configuration system only. They are the settings a running Ledger cannot change about itself, or that would let an API caller reach outside the product. Each has its reason:

| Keys | Why they stay deployment settings |
| --- | --- |
| `database.path`, `database.busy_timeout` | Needed to open the database the runtime settings live in. |
| `http.listen`, `http.management_listen`, `http.read_header_timeout`, `http.read_timeout`, `http.write_timeout`, `http.idle_timeout`, `http.shutdown_timeout`, `http.max_body_bytes` | Bound when the listeners are built, before the database opens; rebinding a listener from a request that arrives on it is not safe. |
| `http.trusted_proxies` | Describes the host's network, and decides which client addresses the per-address sign-in limit sees: an API caller who could widen it could spoof addresses and escape that limit. |
| `log.format` | The log handler is built before the database opens, and log pipelines parse it. (`log.level` is a runtime setting.) |
| `kek.value`, `kek.file` | The root of secret sealing (point 6). Storing it next to what it seals defeats it. |
| Every `*_file` key (`banking.enable_banking.*.private_key_file`, `auth.oidc.client_secret_file`, `events.pulse.creds_file`, `events.pulse.ca_file`, `decisions.providers.*.api_key_file`, `merchants.enrichment.remote.api_key_file`) | A host file path. The API never accepts a path: a caller could otherwise make Ledger read, and send to a server of the caller's choosing, any file the process can read. The value behind each path can instead be written through the API as a write-only secret (point 6). |
| The secret environment variables (`*_PRIVATE_KEY`, `AUTH_OIDC_CLIENT_SECRET`, `DECISIONS_PROVIDERS_*_API_KEY`) | Still accepted from the environment, as today; they pin the secret (point 3). |

   **(control)** Recommended reading: these are not user-configurable product settings in the sense of D18, so keeping them in configuration is not a scope reduction; they stay visible read-only (point 8). If control reads any of them as a reduction, it asks the operator with a `scope-reduction` human action before the tasks start.

3. **Fixed policy is not turned into settings here.** The matching safeguards and windows (D9, D39: ±3 days, 7-day suggestion window, 10 % band), the outbox retry and retention limits (D17), the refresh retry count and daily budget, and the rate refresh limit (D-FX-R) are operator decisions written in code, not configuration today. Making any of them configurable is new behaviour that needs its own operator decision; this ADR neither adds nor drops them. **(control)** confirm.

### 2. Precedence: configuration pins, the database fills in

4. The effective value of a runtime setting is, highest first: **flags, environment (real, then `.env`), YAML, the stored API value, the built-in default.** The configuration layers keep their ADR-0004 rules (empty means unset).
5. A key set by any configuration layer is **pinned**. The API shows it with its source and `pinned: true`; a write that names a pinned key is refused with 409 `setting_pinned` (new error code) naming the key and the layer (`flag`, `environment`, `dotenv`, `yaml`), never the value. Unpinned keys of the same group can still be changed.
6. The stored value of a key that becomes pinned is kept, not deleted, and applies again when the pin goes away. The reporting zone follows the same rule it follows today (explicit configuration wins, warning, audit entry), so existing deployments behave exactly as before.
7. Why this order: nothing an existing deployment sets changes meaning; the operator keeps a break-glass path for every runtime setting that does not need the API (set the variable, restart; for example `LEDGER_AUTH_PASSWORD_ENABLED=true` after an OIDC mistake, next to the existing `ledger auth recovery-sign-in`); and a Terraform plan against a pinned key fails loudly instead of drifting silently.

### 3. Groups and objects

8. Runtime settings are grouped by the component that consumes them. **Singleton groups** have one representation each, at `/api/v1/settings/{group}`:

| Group | Keys (today's configuration keys) | Grant | Sensitive (point 11) |
| --- | --- | --- | --- |
| `reporting` | `reporting.timezone`, `reporting.currency` | `settings` | no |
| `banking` | `banking.redirect_url`, `banking.consent_ttl`, `banking.stale_after`, `banking.refresh.enabled`, `.interval`, `.request_cooldown`, `.max_backoff` | `settings` | `redirect_url` only |
| `rates` | `rates.enabled`, `.base_url`, `.source`, `.refresh_interval`, `.stale_after`, `.timeout` | `settings` | `base_url` only |
| `merchant_enrichment` | `merchants.enrichment.remote.*` except `api_key_file`; `api_key` write-only | `integrations` | yes |
| `auth` | `auth.password.*`, `auth.session.*`, `auth.sign_in.*`, `auth.require_2fa` | `settings` | yes |
| `oidc` | `auth.oidc.*` except `client_secret_file`; `client_secret` write-only | `settings` | yes |
| `pulse` | `events.pulse.enabled`, `.url`, `.source`, `.name`; `creds` write-only (the .creds content), `ca_pem` (public, readable) | `integrations` | yes |
| `mcp` | `mcp.enabled`, `.requests_per_minute`, `.burst`, `.max_request_bytes` | `settings` | no |
| `logging` | `log.level` | `settings` | no |

   **Keyed objects** (a fixed set of keys, no create or delete; `DELETE` resets to the defaults):
   - `/api/v1/decision-providers/{provider}` (`openrouter`, `openai`, `anthropic`, `typesafe`): `api_key` write-only, `allowed_upstreams` (OpenRouter). Grant `integrations`, sensitive.
   - `/api/v1/decision-tasks/{task}` (the ADR-0060 registry): `enabled`, `provider`, `model`, `capability`, `timeout`, `monthly_requests`, `retries`, `auto_apply`, `fields`, `fallback`, with the ADR-0060 validation (capability, excluded fields never allowed, auto-apply only for categorisation). Grant `integrations`, sensitive (it decides what leaves the process).

   **Collections** (create, read, update, delete, ULID identifiers per ADR-0011):
   - `/api/v1/bank-applications`: the Enable Banking applications of D10. Members: `name` (the key every connection records, 1 to 64 of `[a-z0-9_-]`), `provider` (`enablebanking`), `mode` (`app` or `developer`), `environment` (`production` or `sandbox`), `app_id`, `base_url`, `enabled`, `private_key` write-only, and the read-only `public_key_sha256` derived from it (so the person can compare with the Control Panel without the key leaving). The two configured slots (`banking.enable_banking.app`/`developer`) appear in the list as read-only members with `source: config`. Renaming or deleting an application that a connection records is 409 `conflict` (disable it instead); the existing `GET /bank-applications` and `/bank-applications/{application}/institutions` keep their shape and their name parameter. Grant `integrations`, sensitive.
   - `/api/v1/decision-consents`: list, grant (`POST`), get, revoke (`POST /{id}/revoke`); a consent is never edited. Members per ADR-0060 point 5 (provider, purpose, fields, terms version) plus the attestations the research asks for (for example OpenRouter "ZDR only"). Grant `integrations`, sensitive; the granting actor must be a person or a token acting as one (D42 3a), never `system` (ADR-0060 N5).
   - `/api/v1/rules`: the expr-lang rules of D9, built by **X2b**, which owns the rule language, evaluation and the dry-run endpoint. This ADR fixes only what the API must look like so that Terraform and MCP can rely on it: a collection with ULIDs, `version`, merge-patch, `expression` compiled and refused at write time (400 naming the position, never echoing bank text), `purpose` (`matching` or `categorisation`), `confirming` (D9: only a rule the person marks confirming may auto-confirm, and it still passes every D9 safeguard), `priority`, `enabled`, archive-first like organisation definitions. Grant `rules` (administrator-only: expressions hold bank-text fragments, which D29 keeps from read-only users).

### 4. Representation and OpenAPI shape

9. Every settings resource follows ADR-0011 and ADR-0015:
   - `GET` returns the **effective** values as top-level members with the configuration types (durations as strings with a unit, enums, lists), plus read-only `version`, `updated_at`, `sources` (a map from member to `default`, `api` or one of the configuration layers), `pinned` (the pinned members) and `apply_state` (point 17).
   - `PATCH` is `application/merge-patch+json` with `If-Match` (or `version` in the body); `null` removes the stored value, so the member falls back to configuration or the default. Read-only members in a patch are 400. An empty patch changes nothing and keeps the version (ADR-0017 rule).
   - `DELETE` on a singleton or keyed object needs `If-Match` and resets every unpinned member to its default (the Terraform destroy of a settings resource).
   - A singleton exists from the start with version 1, so Terraform creates it by reading it and patching (adopt), and imports it by its group name.
   - **Write-only secrets**: a member such as `client_secret` is `writeOnly: true` in OpenAPI 3.1 and is never returned. Its read-only companions are `client_secret_set` (bool), `client_secret_set_at` and `client_secret_source` (`api`, `environment`, `file`, or absent). Writing `null` clears a stored secret. A secret's value is never compared, hashed into a response or echoed in an error.
   - `GET /api/v1/settings` lists the groups with version, `updated_at` and whether anything is pinned. `GET /api/v1/settings/deployment` (grant `settings:read`) returns the deployment settings of point 2 with their sources; secrets as `set`/`unset` only. It is the read-only view Terraform and the UI use for them.
   - Errors: field problems are 400 `validation_failed` with per-field errors using the configuration key names; a combination that breaks a cross-field rule (for example `password.enabled=false` without a usable `oidc` group) names every field involved. New codes: `setting_pinned` (409) and `secret_unreadable` (409, point 6).

### 5. One validation, one effective configuration

10. The effective runtime configuration **is `config.Config`**. The loader gains the stored layer between the defaults and YAML (two phases at startup: load configuration, open the database, read the stored layer, merge, validate the whole). Every API write builds the would-be effective configuration from the stored values plus the patch, runs the **same** validation that `ledger config check` and startup run (ADR-0004 rules, including the cross-field and network checks), and only then commits. So the API, the CLI, Terraform, MCP and the configuration files accept exactly the same values, and `config.example.yaml` stays the reference for every key (each key's description says whether it is a runtime setting).
11. **Startup with invalid stored settings** (for example after an upgrade tightened a rule) is refused with a message naming the group and the host command that fixes it. New host commands, which open the database directly and so need the server stopped (instance lock, ADR-0019): `ledger settings show [group]` and `ledger settings reset <group> [--key k]`, both audited with actor `system`. `ledger config check` shows the stored layer and the source of every key when the database is readable.

### 6. Secrets in the database

12. A secret written through the API is sealed with a key derived from the KEK (the ADR-0030 sealing, a separate derivation label per purpose) and stored in `integration_secrets` (owner type, owner key, secret name, sealed value, `set_at`, `set_by`). It is registered with the log redactor when loaded. Rotating the KEK makes stored secrets unreadable: the owning resource then reports `client_secret_set: true` with state `unreadable` (409 `secret_unreadable` when an operation needs it) until the secret is written again; nothing falls back silently.
13. A secret given in the environment or a referenced file pins that secret: the API shows the source and refuses to write it (409 `setting_pinned`).
14. Banking credentials are never collected: the only bank-related secret is the provider **application's** private key, which belongs to the operator's Enable Banking application, never a bank login.

### 7. Live apply

15. A settings service owns an immutable snapshot of the effective configuration (an atomic pointer). After a write **commits**, it builds and publishes the next snapshot and notifies the components that subscribed to the groups that changed. Requests in flight finish with the snapshot they started with.
16. Every runtime setting applies without a restart. Each component task implements its own reconfiguration: the refresh scheduler and rate service take new intervals at their next tick; sign-in limits, session timeouts and `require_2fa` apply from the next request (a shorter timeout ends sessions that are already older; a longer one never extends a session past what it was issued with); the OIDC verifier, the Enable Banking adapter registry, the Pulse publisher (reconnect), the decision providers and the remote merchant source are rebuilt from the new snapshot.
17. A rebuild that fails after the commit (for example OIDC discovery unreachable) does not roll the setting back: the group reports it in its read-only `apply_state` (`applied` or `failed`, with an error code and time, never a value), where a component already has a status (rates status, bank connection state) it shows there too, the previous working instance is not kept, and the UI shows the component as failed. Readiness still gates only on the database (D17); Ledger has no general integration status endpoint today, and this ADR does not add one. Validation (point 10) refuses everything it can check offline beforehand.

### 8. Grants, recent authentication and the root token

18. Three new resources in the vocabulary (ADR-0021), all administrator-only, each with `read` and `write`: `settings` (application, planning, authentication and operational groups), `integrations` (everything that sends data out or holds an integration secret: bank applications, decision providers, tasks and consents, merchant enrichment, Pulse), and `rules`. Read-only users see none of them; the effects they need (a converted total, a freshness badge) keep coming through the feature that shows them.
19. **Sensitive writes** (the "Sensitive" column of point 8, every write-only secret, every egress URL, every consent) need, from a session, a recent sign-in (the existing re-authentication window), and from a token, **the root token of the root account** (the D25/D32 rule; others get 403 `root_token_required`). Reason: these writes can weaken sign-in, redirect bank or identity traffic to another server, or widen what leaves the process; a leaked ordinary administrator token must not be enough. **(control)** confirm; the alternative is the grant alone.
20. **Audit** (ADR-0025, same transaction as the change): `settings.updated` and `settings.reset` (group, field names), `integration_secret.set` and `integration_secret.cleared` (owner, secret name), `bank_application.created`, `.updated`, `.deleted` (name, mode, field names), `decision_provider.updated`, `decision_task.updated` (field names), the existing `decision_consent.granted`/`revoked`, `rule.created`, `.updated`, `.archived`, `.deleted`. Details carry names, never values (a value may be a URL with a token in it, or a secret). The existing `settings.reporting_timezone_changed` keeps its from/to and is now raised with the person's identity when the zone changes through the API. Token writes are also covered by `token.write_used`.
21. There is no separate revision table: the audit log keeps who changed which fields when. **(control)** confirm; a value history would be a later addition (it needs a rule for which values may be kept).

### 9. Storage and migrations

22. One forward-only migration in the core task adds `setting_groups` (ULID `id`, unique `name` checked by pattern (`reporting`, `decision_tasks.categorisation`, ...), `stored` JSON with `json_valid` and only the members set through the API, `version`, `created_at`, `updated_at`): one row per singleton group and keyed object, created on first use. It follows the versioned convention, so `TestMutableTablesAreVersioned` covers it. The captured reporting zone moves from `settings` into the `reporting` group in the same migration's follow-up code (the `settings` table stays for server bookkeeping).
23. The secrets task adds `integration_secrets` (listed in `unversionedTables`: its owner carries the version, and every secret change bumps it). The bank-applications task adds `bank_applications` (versioned). Rules are X2b's migration.

### 10. Client, CLI, MCP and Terraform parity

24. **Client and CLI** (ADR-0032): every new operation gets a `ledgerclient` method in the task that adds the route (the client's contract test already fails otherwise), and a CLI command: `ledger settings list`, `get`, `set` and `reset <group>` (`set key=value ...`, secrets from `--<name>-file` read on the client machine or from standard input, never a flag value), `ledger bank-applications ...`, `ledger decision-providers ...`, `ledger decision-tasks ...`, `ledger decision-consents ...`, and `ledger rules ...` (X2b).
25. **MCP** (ADR-0038): read tools for every group and object (secrets appear only as set/unset); write tools for `reporting`, `banking` (without `redirect_url`), `rates` (without `base_url`), `logging`, and rules (`confirming` marked destructive). **No MCP write tools for the sensitive writes** of point 19 (authentication, OIDC, integrations, secrets, consents, egress URLs): the requirement asks for the *applicable* MCP operations, and a model-driven client is exactly where a prompt-injected change to sign-in or data egress must not be one call away. **(control)** confirm.
26. **Terraform** (in terraform-provider-ledger, after the go-ledger pin moves; ADR-0005 B1): one resource per singleton group (`ledger_settings_reporting`, `ledger_settings_auth`, ...; destroy resets), `ledger_decision_provider`, `ledger_decision_task` (destroy resets), `ledger_decision_consent` (replace on change, destroy revokes), `ledger_bank_application`, `ledger_rule`, and a `ledger_deployment_settings` data source. Secrets use `*_wo` with `*_wo_version`; a pinned member is reported as a plan-time error, not as drift.

## Why

- **Configuration pins** rather than the database winning: existing deployments, container setups and the generated examples keep their meaning; the operator always has a path back that needs no working sign-in; and a conflict is explicit (409 with the source) instead of a value silently ignored. Rejected: the database wins (an environment variable would silently stop working); one home per key with keys removed from YAML (breaks every existing deployment and the "YAML, environment and .env configuration" requirement for those keys).
- **`config.Config` as the effective model**: one set of types, defaults, descriptions and validators, so the API cannot accept what startup would refuse and the example file remains the single reference. Rejected: a parallel settings schema (two validations drift).
- **A JSON document per group** rather than typed columns: settings are not financial facts; their rules already live in the configuration validators and are needed there anyway; a key added to `config.Config` needs no migration. The JSON holds only API-set members, so defaults can change between versions.
- **Sealed secrets in the database** next to environment and file secrets: D18 requires secrets to be write-only through the provider, which needs somewhere to write them; the TOTP sealing already exists and is tested.
- **No host paths through the API** and the network-bound keys staying in configuration: these are the exceptions where the API itself would become the attack surface.
- **Root token for sensitive writes**: consistent with D25/D32/D34; Terraform runs with the root token anyway (ADR-0005 point 4).

## Consequences

- Terraform B1 is unblocked group by group; every new route also lands in the client, the CLI and the OpenAPI document, and the coverage table in the provider's ADR-0005 must be re-derived when the pin moves.
- Startup reads the database before the configuration is final; tools that only parse configuration (`config env`, example generation) do not.
- Every component that consumes a runtime setting must handle live change. That is the main cost and the main source of review findings; each task owns its component's test of a change applied without a restart.
- A KEK rotation now affects integration secrets as well as TOTP (documented in the KEK key's description and docs/backup-restore.md).
- A backup now carries integration secrets (sealed). docs/backup-restore.md must say so.
- Still open, and listed for control: points 2 (deployment-setting exceptions), 3 (fixed policy), 19 (root token), 21 (no revision table) and 25 (MCP write scope). L7 keeps the review UI and the decision settings screens; X3 keeps the TypeSafe consent texts; X2b keeps the rule language and engine.

## Implementation tasks

One worker each, in go-ledger unless marked. "Sensitive" means point 19 applies.

| ID | Task | Depends on |
| --- | --- | --- |
| S1 | Core: `setting_groups` migration; stored layer in the loader with per-key sources; settings service with snapshot, subscriptions and `apply_state`; `GET /settings`, `/settings/deployment`, generic group GET/PATCH/DELETE; grants `settings`, `integrations`, `rules`; codes `setting_pinned`, `secret_unreadable`; audit actions; client methods; precedence documented in the configuration docs and key descriptions; the `logging` group live end to end as the first consumer. | none |
| S1b | `mcp` group live (rate limiter and endpoint switch); `ledger settings` CLI (list, get, set, reset); host `ledger settings show`/`reset` with the instance lock; startup refusal for invalid stored settings; `config check` showing the stored layer. | S1 |
| S2 | Sealed secrets: `integration_secrets`, KEK-derived sealing per purpose, write-only OpenAPI pattern and companions, redactor registration, pinning by environment or file, unreadable state, sentinel-secret tests over responses, logs, audit and errors; root-token and recent-auth gate for sensitive writes. | S1 |
| S3 | `reporting` and `banking` groups: move the captured zone, reclassification warning and audit with the person's identity, scheduler and consent TTL applied live. | S1 (S2 for the gate on `redirect_url`) |
| S4 | `rates` and `merchant_enrichment` groups: rate service and remote merchant source rebuilt live; merchant `api_key` write-only. | S1, S2 |
| S5 | `auth` and `oidc` groups: live sign-in limits, sessions and `require_2fa`; OIDC verifier rebuild; `client_secret` write-only; lock-out validation; tests that the environment break-glass still works. | S1, S2 |
| S6 | `pulse` group: live reconnect, `creds` write-only, `ca_pem`; nothing recorded while off (existing rule). | S1, S2 |
| S7 | Bank applications: `bank_applications` table, CRUD, configured slots read-only, `private_key` write-only with `public_key_sha256`, rename/delete rules against connections, adapter registry rebuilt live, both modes tested offline with test doubles. | S1, S2 |
| S8 | Decision providers and tasks (the settings half of L7): keyed objects, ADR-0060 validation, `api_key` write-only, decision service rebuilt live. | S1, S2 |
| S9 | Decision consents API (shared with L7/X3): grant, list, revoke; fields and attestations; person-only actor. | S1, S2 |
| S10 | MCP tools for settings per point 25, with the parity test. | S3, S4 (and S8, S9 for their read tools) |
| S11 | Settings UI, general: admin Settings area for reporting, banking, rates, merchant enrichment, Pulse, MCP and logging: sources, pinned badges, secret set/replace/clear, pending-save and failed states, reclassification warning. | S3, S4, S6 |
| S12 | Settings UI, sign-in and integrations: auth and OIDC (with the lock-out guard), bank applications. Decision settings screens stay in L7. | S5, S7 |
| X2b | Existing task, acceptance extended: the rules API of point 8 (collection, client, CLI, MCP, audit, grant `rules`). | S1 (for the grant), X2 |
| L7 | Existing task, narrowed: review UI and decision settings screens on S8 and S9. | S8, S9, L6 |
| TS1 | terraform-provider-ledger: settings group resources and `ledger_deployment_settings` data source. | S3, S4, S5, S6 |
| TS2 | terraform-provider-ledger: `ledger_bank_application`. | S7 |
| TS3 | terraform-provider-ledger: `ledger_decision_provider`, `ledger_decision_task`, `ledger_decision_consent`. | S8, S9 |
| TS4 | terraform-provider-ledger: `ledger_rule`. | X2b |
