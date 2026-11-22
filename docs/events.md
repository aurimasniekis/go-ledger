# Event catalog

Ledger publishes events to Pulse (NATS JetStream) through a transactional outbox. This page is the catalog: the envelope, the subject and type rules, and every event Ledger emits. Publishing is optional (`events.pulse.enabled`, default off). **While it is off, nothing is recorded: the outbox write is a no-op** (events are still validated, so mistakes surface in every mode). Turning it on later does **not** replay: a retention job in `ledger serve` runs whether or not publishing is enabled and, while it is disabled, marks any row still pending from an earlier enabled period as `failed` ("publishing disabled"), so only changes made while enabled are published.

Payloads are stored **in plain form on the bus**. They never carry secrets, credentials, IBANs or account numbers, raw bank payloads, or free-text descriptions. Each event declares an allow-list of fields; the outbox refuses anything else (`internal/outbox`, ADR-0012).

## Envelope

| Field | Value |
| --- | --- |
| id | ULID generated at enqueue; also the idempotency key. Every retry reuses it. |
| type | `<domain>.<entity>.<action>.v<N>`, for example `ledger.<entity>.<action>.v1`. The domain is always `ledger`. The version belongs to the type; add fields compatibly, a breaking change gets `v2`. |
| source | `events.pulse.source` (default `ledger`), passed to the outbox writer together with `events.pulse.enabled` when `ledger serve` starts. |
| time | `occurred_at`, UTC, set at enqueue (inside the state-changing transaction) and kept for every retry. |
| subject | `evt.ledger.<entity>.<action>[.<key>]`. |
| payload | A JSON object holding only the allow-listed fields of the event (at most 256 KiB encoded). |

Subject rules (the Pulse rules): `evt.` prefix, at least four dot-separated tokens, every token lowercase `[a-z0-9_-]+`, at most 256 bytes, no wildcards. The optional `<key>` is a single further token whose shape each event declares: either no key, or the lowercase ULID of the entity. Anything else (an IBAN-shaped value, free text, wildcards) is rejected, because subjects are as visible on the bus as payloads.

Delivery is at-least-once: the Pulse `EVENTS` stream deduplicates a repeated id only within its fixed 2-minute window. A retry after that window (or after a crash between publish and acknowledgement) can deliver the event twice, so consumers must treat the id as the idempotency key.

## Outbox lifecycle

An event row is inserted in the same transaction as the change it announces, so a rolled-back change leaves no event. Rows are append-only: the envelope never changes; only `state` (`pending` → `published` or `failed`), `attempts`, `next_attempt_at`, `published_at`, `failed_at` and `last_error` do. Retries run with backoff for up to 24 hours. Published rows are kept 7 days, failed rows 30 days, pending rows are never purged or deleted (they only expire to `failed`). The retention job (`job=outbox-retention`, hourly, first pass at startup) expires and purges whether or not publishing is enabled. A row in a terminal state (`published` or `failed`) is immutable: a trigger rejects any further update, so delivery history cannot be rewritten or its purge postponed.

## Operator setup

Publishing is off until you turn it on. **Turning it on sends event data off this machine to the Pulse server you configure, in plain form on the bus**; do it only if you accept that. Event payloads are limited to the allow-listed fields in the catalog below (no secrets, IBANs or account numbers, raw bank data or free text).

1. Obtain a `.creds` file for Ledger from your Pulse administrator, with the publish grant for `evt.ledger.>` only. Keep it outside the repository, readable only by the Ledger user. Ledger stores only its path.
2. Configure (YAML, `LEDGER_*` environment or `.env`; see `config.example.yaml`):
   - `events.pulse.enabled: true`
   - `events.pulse.url: tls://pulse.example.com:4222` (`tls://` is required)
   - `events.pulse.creds_file: /path/to/ledger.creds`
   - `events.pulse.ca_file` (optional): a PEM file with an extra CA when the server uses a private CA. Verification is never turned off.
   - `events.pulse.source` (default `ledger`) and `events.pulse.name` (default `ledger`)
3. Restart. A bad or missing creds file, an unreachable server or a missing grant never stops Ledger or its readiness: events queue in the outbox and are retried with backoff for up to 24 hours, then marked failed (kept 30 days). Watch the logs (`job=outbox-publisher`) and the metrics `ledger_events_published_total`, `ledger_events_failed_total`, `ledger_events_pending`, `ledger_events_duplicate_total`, `ledger_events_retries_total` and `ledger_pulse_connected`. Repeated timeouts usually mean the publish grant is missing.
4. Only changes made while enabled are published; enabling does not replay history (see above).
5. **Run one Ledger process per database.** Ledger takes no lock on the database file; two processes would both publish every event (the broker de-duplicates only inside its 2-minute window), and both would run retention. Status changes stay consistent, but consumers would see duplicates.
6. If the server closes the connection for good (for example the credentials expired or were revoked) the publisher drops it and dials again with backoff, so a replaced `.creds` file is picked up without a restart. `ledger_pulse_connected` follows the real connection state, including while the client library is reconnecting. Nothing is buffered while disconnected: a publish during an outage fails at once and is retried by the outbox, so an event is never sent after its row was marked failed.

See [ADR-0016](decisions/0016-pulse-publisher.md).

## Events

Each feature adds its entry to `outbox.Registry` and a row to the table below in the same change.

| Subject | Type | Trigger | Payload (field: kind, * required) |
| --- | --- | --- | --- |
| `evt.ledger.alert.raised.<alert id>` | `ledger.alert.raised.v1` | An alert is newly raised and `alerts.channels` lists `pulse` (ADR-0065). Raising an open or a dismissed alert again sends nothing. Written in its own transaction after the alert is stored; a failure is logged and never loses the alert. | `kind`: string*, `object_type`: string*, `object_id`: string* |

The alert event carries the kind (`consent_expiring`, `commitment_amount_changed`, `unmatched_overdue`) and the object it is about, never the alert's message, an amount or any bank text. The subject key is the lowercase alert id.

### Alert channels

Alerts are in-app only unless the operator selects a channel. `alerts.channels` (YAML list, or comma separated `LEDGER_ALERTS_CHANNELS`, also from `.env`) is empty by default; the only channel is `pulse`, which requires `events.pulse.enabled: true` (startup is refused otherwise, so a channel never silently does nothing). `alerts.consent_horizon_days` (default 14) and `alerts.interval` (default 1h) tune the detectors. They run after every bank refresh that stored something and on that interval; see [ADR-0065](decisions/0065-decision-tasks.md).

### Adding an event

1. Register an `outbox.Definition` (entity, action, trigger, fields). Declare the subject key shape (`Key`: none or `KeyULID`). Field names that are credentials, account identifiers, raw payloads or free text (matched as whole names or whole `_`-separated words, so `credit_limit` or `merchant_name` are fine) are rejected at registration; the typed allow-list, size limit and review are the real protection.
2. Call `Outbox.Enqueue` with the caller's transaction inside `store.DB.WriteTx`.
3. Add the event to the table above: subject, type, trigger, payload schema.
