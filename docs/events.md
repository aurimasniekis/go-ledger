# Event catalog

Ledger publishes events to Pulse (NATS JetStream) through a transactional outbox. This page is the catalog: the envelope, the subject and type rules, and every event Ledger emits. Publishing is optional (`events.pulse.enabled`); with it off, events are still recorded in the outbox and purged by age.

Payloads are stored **in plain form on the bus**. They never carry secrets, credentials, IBANs or account numbers, raw bank payloads, or free-text descriptions. Each event declares an allow-list of fields; the outbox refuses anything else (`internal/outbox`, ADR-0012).

## Envelope

| Field | Value |
| --- | --- |
| id | ULID generated at enqueue; also the idempotency key. Every retry reuses it. |
| type | `<domain>.<entity>.<action>.v<N>`, for example `ledger.<entity>.<action>.v1`. The domain is always `ledger`. The version belongs to the type; add fields compatibly, a breaking change gets `v2`. |
| source | `events.pulse.source` (default `ledger`). |
| time | `occurred_at`, UTC, set at enqueue (inside the state-changing transaction) and kept for every retry. |
| subject | `evt.ledger.<entity>.<action>[.<key>]`. |
| payload | A JSON object holding only the allow-listed fields of the event. |

Subject rules (the Pulse rules): `evt.` prefix, at least four dot-separated tokens, every token lowercase `[a-z0-9_-]+`, at most 256 bytes, no wildcards. The optional `<key>` is one or more further tokens, for example a lowercase entity id.

Delivery is at-least-once: the bus deduplicates a repeated id only within a short window, so consumers should treat the id as the idempotency key.

## Outbox lifecycle

An event row is inserted in the same transaction as the change it announces, so a rolled-back change leaves no event. Rows are append-only: the envelope never changes; only `state` (`pending` → `published` or `failed`), `attempts`, `next_attempt_at`, `published_at`, `failed_at` and `last_error` do. Retries run with backoff for up to 24 hours. Published rows are kept 7 days, failed rows 30 days, pending rows are never purged.

## Events

No product event is emitted yet: the outbox ships ahead of the features that raise events (PU1). Each feature adds its entry to `outbox.Registry` and a row to the table below in the same change.

| Subject | Type | Trigger | Payload (field: kind, * required) |
| --- | --- | --- | --- |
| _none yet_ | | | |

### Adding an event

1. Register an `outbox.Definition` (entity, action, trigger, fields). Field names that suggest secrets, account numbers, raw payloads or free text are rejected at registration.
2. Call `Outbox.Enqueue` with the caller's transaction inside `store.DB.WriteTx`.
3. Add the event to the table above: subject, type, trigger, payload schema.
