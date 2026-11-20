// Package banking is the boundary between Ledger's finance logic and
// banking-data providers (Enable Banking first, ADR-0013).
//
// It declares, on the consumer side (ADR-0001):
//
//   - [Provider], the port every banking-data adapter implements: consent
//     start and completion, institutions, accounts, balances and paged
//     transactions;
//   - [Capabilities], what one configured provider application supports,
//     including its access [Mode] (decision D10: per connection);
//   - the error taxonomy ([Error], [ErrorKind]) that tells reconnect-required
//     apart from retryable, rate-limited and permanent failures;
//   - the normalized bank facts ([Account], [Balance], [Transaction]) and the
//     idempotency keys they are stored under ([KeyTransactions]);
//   - [FactStore], the persistence port that internal/store/bankfacts
//     implements.
//
// Bank facts are provider-owned. Nothing here models user corrections,
// categories or payment matches: those live in their own packages and tables
// and refer to bank facts, never the other way round.
//
// Adapters live in internal/providers/<name> and import this package; this
// package and every other finance package import no adapter. Tests drive the
// boundary with the deterministic fake in the bankingtest sub-package.
//
// Account identifiers, provider session references and consent codes are
// [Sensitive]: they print, log and marshal as a mask, and only an explicit
// Reveal returns them. Errors from this package never carry them, nor
// transaction descriptions or raw provider payloads.
package banking
