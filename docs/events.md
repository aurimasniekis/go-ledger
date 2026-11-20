# Event catalog

Ledger publishes events to Pulse (NATS JetStream) through a transactional outbox. This page is the catalog: the envelope, the subject and type rules, and every event Ledger emits. Publishing is optional (`events.pulse.enabled`, default off). **While it is off, nothing is recorded: the outbox write is a no-op** (events are still validated, so mistakes surface in every mode). Turning it on later does **not** replay changes made while it was off; only changes made while enabled are published.

Payloads are stored **in plain form on the bus**. They never carry secrets, credentials, IBANs or account numbers, raw bank payloads, or free-text descriptions. Each event declares an allow-list of fields; the outbox refuses anything else (`internal/outbox`, ADR-0012).

## Envelope

| Field | Value |
| --- | --- |
| id | ULID generated at enqueue; also the idempotency key. Every retry reuses it. |
| type | `<domain>.<entity>.<action>.v<N>`, for example `ledger.<entity>.<action>.v1`. The domain is always `ledger`. The version belongs to the type; add fields compatibly, a breaking change gets `v2`. |
| source | `events.pulse.source` (default `ledger`), wired into the outbox writer with `events.pulse.enabled`. |
| time | `occurred_at`, UTC, set at enqueue (inside the state-changing transaction) and kept for every retry. |
| subject | `evt.ledger.<entity>.<action>[.<key>]`. |
| payload | A JSON object holding only the allow-listed fields of the event. |

Subject rules (the Pulse rules): `evt.` prefix, at least four dot-separated tokens, every token lowercase `[a-z0-9_-]+`, at most 256 bytes, no wildcards. The optional `<key>` is a single further token whose shape each event declares: either no key, or the lowercase ULID of the entity. Anything else (an IBAN-shaped value, free text, wildcards) is rejected, because subjects are as visible on the bus as payloads.

Delivery is at-least-once: the Pulse `EVENTS` stream deduplicates a repeated id only within its fixed 2-minute window. A retry after that window (or after a crash between publish and acknowledgement) can deliver the event twice, so consumers must treat the id as the idempotency key.

## Outbox lifecycle

An event row is inserted in the same transaction as the change it announces, so a rolled-back change leaves no event. Rows are append-only: the envelope never changes; only `state` (`pending` → `published` or `failed`), `attempts`, `next_attempt_at`, `published_at`, `failed_at` and `last_error` do. Retries run with backoff for up to 24 hours. Published rows are kept 7 days, failed rows 30 days, pending rows are never purged or deleted. A row in a terminal state (`published` or `failed`) is immutable: a trigger rejects any further update, so delivery history cannot be rewritten or its purge postponed.

## Events

No product event is emitted yet: the outbox ships ahead of the features that raise events (PU1). Each feature adds its entry to `outbox.Registry` and a row to the table below in the same change.

| Subject | Type | Trigger | Payload (field: kind, * required) |
| --- | --- | --- | --- |
| _none yet_ | | | |

### Adding an event

1. Register an `outbox.Definition` (entity, action, trigger, fields). Declare the subject key shape (`Key`: none or `KeyULID`). Field names that are credentials, account identifiers, raw payloads or free text (matched as whole names or whole `_`-separated words, so `credit_limit` or `merchant_name` are fine) are rejected at registration; the typed allow-list, size limit and review are the real protection.
2. Call `Outbox.Enqueue` with the caller's transaction inside `store.DB.WriteTx`.
3. Add the event to the table above: subject, type, trigger, payload schema.
