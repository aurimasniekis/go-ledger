// Package providers is the parent of provider adapters: banking data, currency
// rates, language models and merchant enrichment. Each adapter lives in its own
// package, for example internal/providers/enablebanking.
//
// An adapter owns provider authentication, consent, fetching and mapping. Finance
// packages never import an adapter; they receive a provider through a port they
// declare. See ADR-0001.
package providers
