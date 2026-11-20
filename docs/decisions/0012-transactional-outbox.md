# ADR-0012: Transactional outbox and event catalog

Status: Accepted (2026-11-20)

## Context

Ledger publishes events to Pulse so other tools can react. An event must neither be lost nor sent for a change that rolled back, and payloads sit unencrypted on the bus. This ADR covers the outbox (PU1); the publisher (PU2) is separate.

## Decision

1. **Same transaction.** `outbox.Outbox.Enqueue(ctx, tx, event)` takes the caller's `*sql.Tx` from `store.DB.WriteTx` and never begins or commits one. A rollback or panic removes the row with the change.
2. **Envelope persisted at enqueue**: ULID id (from `internal/logging`; the publisher passes it as the Pulse id), type `ledger.<entity>.<action>.v<N>`, source, UTC `occurred_at`, subject `evt.ledger.<entity>.<action>[.<key>]`. Subject and type are validated in Go to the go-pulse rules (prefix, at least four `[a-z0-9_-]+` tokens, at most 256 bytes) and again by column checks. The go-pulse dependency is not added here.
3. **Per-event allow-list.** Events must be registered in an `outbox.Registry` with typed fields (string, int, bool; no floats, amounts are decimal strings plus a currency). Enqueue rejects unregistered events, undeclared fields, wrong kinds, missing required fields and strings over 256 bytes. Registration rejects field names containing credential, account-number, raw-payload or free-text fragments. This is a guard against careless catalog entries, not proof of privacy: reviewers still check each new event.
4. **Append-only.** Triggers make the envelope columns immutable, make terminal states final and refuse deleting pending rows. The outbox therefore does not take part in optimistic concurrency (D17) of the entities it announces. Status columns (`state`, `attempts`, `next_attempt_at`, `published_at`, `failed_at`, `last_error`) are for the publisher.
5. **Retention.** `outbox.Purge` deletes published rows after 7 days and failed rows after 30 days (D17); the caller schedules it. Retry backoff (up to 24 h) is PU2's.
6. **Catalog** in `docs/events.md`. No product events exist yet, so it lists the format and the extension steps.

## Consequences

- Migration `0003_outbox.sql` (renumber if another migration lands first).
- `last_error` must be written by the publisher without credentials or payload data.
- Allow-list changes are code changes, visible in review and in the catalog.
