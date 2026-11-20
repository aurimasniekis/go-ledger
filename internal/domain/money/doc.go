// Package money holds Ledger's exact monetary primitives.
//
// A [Decimal] is an exact decimal number: an arbitrary-precision integer
// coefficient and a non-negative scale (the number of digits after the
// decimal point). The scale is part of the value, so "12.50" stays "12.50"
// and a source amount survives parsing, storage and formatting unchanged.
//
// A [Money] is a Decimal with an ISO 4217 [Currency]. Arithmetic and
// comparison are defined only within one currency: combining two currencies
// returns [ErrCurrencyMismatch], and a total over several currencies returns a
// [MixedCurrencyError]. [Totals] keeps one total per currency, which is the
// primary figure Ledger shows; a converted aggregate is a separate, secondary
// step with its own rate provenance (decision D5, ADR-0005).
//
// Rounding happens only at presentation and conversion, half-even
// ([Decimal.RoundHalfEven], [Money.ForDisplay]). Storage never rounds.
//
// Nothing in this package uses binary floating point. A test type-checks the
// package and fails on any float or complex value (nofloat_test.go).
package money
