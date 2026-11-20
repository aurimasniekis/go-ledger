# ADR-0016: Pulse publisher

Status: Accepted (2026-11-20)

## Context

[ADR-0012](0012-transactional-outbox.md) stores events transactionally. Something has to send them to Pulse (NATS JetStream) without making Pulse a dependency of the application: Pulse is optional, may be down for hours, and must never gate startup or readiness (decision D17).

## Decision

1. **Dependency.** `github.com/aurimasniekis/go-pulse` v0.1.7 (Apache-2.0, pure Go; the tree builds with `CGO_ENABLED=0`, which CI already tests). Only `internal/pulsebus` imports it. The publisher (`internal/outbox`) talks to a small `Bus` interface, so every test uses a fake and none needs Pulse or NATS.
2. **Worker.** `outbox.Publisher` is started by `ledger serve` only when `events.pulse.enabled` is true; disabled means no goroutine and no connection. It polls for due pending rows (2 s), publishes in order, and stops on shutdown after closing the connection. `serve` waits for it before closing the database.
3. **Lazy connect, Ledger-owned backoff.** It dials only when a row is due. The SDK reconnects by itself once connected but fails fast on the first dial, so a failed dial is retried by the publisher with exponential backoff and jitter (1 s doubling to 1 min); queued rows are untouched meanwhile. Startup never dials.
4. **Identity on every attempt.** Each publish passes the row's stored id (`WithID`, the Pulse idempotency key), `occurred_at` (`WithTime`) and source. Retries therefore are the same event. A `PubAck.Duplicate` counts as success. Delivery is at-least-once: the broker's deduplication window is 2 minutes, so a retry after it, or after a crash between publish and recording, can deliver twice; consumers de-duplicate on the id.
5. **Per-attempt deadline** of 5 s, connect deadline 10 s. A publish outside the credentials' grants times out instead of being refused, so three timeouts in a row log a hint about a missing publish grant for `evt.ledger.>`.
6. **Retry and failure.** A failed attempt is rescheduled with delay 5 s × 2^(n−1), capped at 15 min, multiplied by a jitter factor in [0.5, 1). A pending row older than 24 h is marked `failed` (a sweep at every pass, so rows also expire while Pulse is unreachable or Ledger was down; such a row is never sent late). A malformed subject fails at once. The stored `last_error` is one bounded line; the SDK errors never carry credentials, and the creds and CA paths are scrubbed from error text.
7. **Purge.** Hourly, `outbox.Purge` (7 days published, 30 days failed).
8. **Observability, not readiness.** Metrics `ledger_events_published_total`, `_duplicate_total`, `_failed_total`, `_retries_total`, `ledger_events_pending` and `ledger_pulse_connected` have no labels, so cardinality is fixed. Connect, disconnect and failure transitions are logged with `job` and `job_id`. `Publisher.Status()` is ready for a status endpoint; none exists yet. `/readyz` still checks only the database.
9. **Configuration** (ADR-0004): `events.pulse.{enabled,url,creds_file,ca_file,source,name}`. The URL must be `tls://`. Credentials are a **path** to a `.creds` file from an operator-approved secret source; the path is not logged. TLS verification is never weakened (`WithNATSOptions` is not used). `source` is validated at load even when disabled; url and creds_file are required only when enabled. Files are not opened at load.
10. **Allow-list tightening (PU1 re-review N1).** Free-text and people names (`note`, `text`, `body`, `holder`, `debtor`, `creditor`, `counterparty`, `card`) are forbidden as whole words anywhere in a field name; bare `name` and `message` are forbidden as exact names only. `merchant_name` and `error_message` still pass.

## Consequences

- One Ledger process per database: two would both publish; the broker de-duplicates only inside its window.
- Events whose 24 h window passes are failed and kept 30 days for the operator; there is no replay command yet.
- Events leave the machine in plain form: enabling is a consent step (see docs/events.md).
