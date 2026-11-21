// Package frankfurter is the Frankfurter exchange-rate adapter (FX2, ADR-0046).
//
// It implements [fx.Provider] on Frankfurter's v2 rates route
// (GET /v2/rates). Decision D11 selects Frankfurter, including its historical
// rates. The adapter owns everything specific to it: the URL shapes, the
// response shape (an array of date, base, quote and rate objects), the
// status codes and how each is classified for Ledger.
//
// Frankfurter needs no key and publishes no quota; it is a free public
// service, so the adapter is deliberately gentle: one request per base
// currency, a small request timeout, a bounded response, no automatic
// retries (the [fx.Service] pauses calls after a failure and the refresh job
// backs off) and a Retry-After on a 429 is passed on and honoured. A cached
// answer is never discarded here: failures are reported and the cache is
// served by the service.
//
// Rates are parsed from the response text and never pass through float64,
// so the value stored is exactly the digits Frankfurter published. A rate
// written with an exponent, a non-positive rate, a quote for a currency that
// was not asked for or a date later than the one asked for makes the whole
// answer a permanent failure: nothing from it is cached.
package frankfurter
