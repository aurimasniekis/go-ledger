// Package enrichment identifies the merchant behind a bank transaction (task
// E2, ADR-0054) and hands the answer to the merchant store (E1, ADR-0051).
//
// Decision D15 fixes the policy. The baseline is a local curated mapping plus
// the person's confirmations, fully offline. A confirmed mapping is reused only
// as a suggestion for similar transactions: its confidence stays below the
// reliability threshold, so it is held back until the person confirms it. A
// provider-neutral remote source exists and is off; no remote source is
// approved. A failing source never fails or delays an import: lookups run after
// a refresh, errors become an "unavailable" answer (which never replaces an
// earlier answer), and nothing here is financial.
package enrichment
