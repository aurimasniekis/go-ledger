// Package outbox is Ledger's transactional outbox. [Outbox.Enqueue] writes an
// event row inside the caller's write transaction, so the event exists exactly
// when the state change it announces commits. Publishing to Pulse is a separate
// background step (PU2); this package owns the envelope, the subject and type
// rules, the per-event payload allow-list and the retention purge.
//
// Outbox rows are append-only: the envelope (id, subject, type, source,
// occurred_at, payload) never changes, so the outbox does not take part in the
// optimistic-concurrency scheme of the entities it announces. See ADR-0012 and
// docs/events.md.
package outbox
