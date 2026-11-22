// Package merchants models the merchant identity of bank transactions (task
// E1, ADR-0050): who a transaction was paid to, as far as that is known.
//
// Three things are kept apart and never merged into one stored value:
//
//   - The bank fact: the raw description, counterparty and provider
//     identifiers. This package never reads or changes them.
//   - The [Enrichment]: what a lookup source said (name, logo reference,
//     source, confidence, when). It is provider-side data that can be fetched
//     again, and may be ambiguous, empty or missing because the lookup failed.
//   - The [Review]: the person's decision (confirm, correct or reject). It is
//     user data and always wins over the enrichment.
//
// [Resolve] combines them into the [Merchant] a client shows. An identity is
// shown only when the person decided it or the source is reliable (confidence
// at or above the threshold); everything else is explicit (ambiguous, none,
// unavailable, uncertain, not looked up) and the client falls back to a
// neutral presentation. Merchant identity is presentation only: nothing in
// this package or its store refers to amounts, occurrences, matches or payment
// state, and a test pins that.
package merchants
