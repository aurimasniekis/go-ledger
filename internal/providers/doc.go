// Package providers is the parent of provider adapters: banking data, currency
// rates, language models and merchant enrichment. Each adapter lives in its own
// package, for example internal/providers/enablebanking.
//
// An adapter owns provider authentication, consent, fetching and mapping. Finance
// packages never import an adapter; they receive a provider through a port they
// declare. See ADR-0001.
//
// A banking adapter implements internal/domain/banking.Provider, maps provider
// data to that package's normalized model and classifies every failure with
// its error taxonomy (ADR-0013). Tests use the fake in
// internal/domain/banking/bankingtest instead of an adapter. The Enable
// Banking adapter is internal/providers/enablebanking (ADR-0023). The currency
// rate adapter implements internal/domain/fx.Provider and is
// internal/providers/frankfurter (ADR-0046).
package providers
