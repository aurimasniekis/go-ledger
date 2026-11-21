// Package fx is Ledger's currency-rate cache and conversion service (FX1,
// ADR-0022).
//
// A [Provider] port fetches exchange rates from one rate source (decision
// D11 selects Frankfurter; its adapter is
// internal/providers/frankfurter, ADR-0046). A [Cache] port keeps every rate Ledger has received,
// with its provider, the source key the provider reports, the currency pair,
// the exact rate, the date the rate applies to and when Ledger retrieved it;
// internal/store/fxrates implements it on SQLite.
//
// [Service] converts money with provenance. It never invents a rate: it does
// not invert a pair, triangulate through a third currency, interpolate
// between dates or default to 1. A conversion it cannot make is reported as
// [StatusMissing]; one it can only make with a rate older than the freshness
// policy allows is [StatusStale]; both carry the reason. When the provider
// is down or its quota is exhausted, the service keeps answering from the
// cache and says so ([Conversion.RefreshError], [Conversion.Age]). When a
// cached rate is recent enough, it does not call the provider at all.
//
// Only [StatusOK] conversions are acceptable for totals (decision D5: items
// without an acceptable rate are excluded and flagged). All arithmetic is
// exact ([money.Decimal]); the converted value is rounded half-even to the
// target currency's minor units, keeping the unrounded product alongside.
package fx
